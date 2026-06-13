package runtime

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/liveevent"
)

func (s *Service) RunOnce(ctx context.Context) error {
	if s.getState() == StateStopped {
		s.setStartedAt(time.Now().UTC())
	}
	var firstErr error
	for _, source := range s.cfg.Sources {
		if err := s.runFull(ctx, source, "startup"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) Run(ctx context.Context) error {
	s.setState(StateRunning)
	s.setStartedAt(time.Now().UTC())

	// The flusher's context is intentionally derived from Background, not the
	// parent ctx: on a signal shutdown the parent ctx is already cancelled before
	// the deferred block runs, so a parent-derived flusherCtx would let the flusher
	// drain and exit BEFORE the terminal "stopped" event is enqueued below. Keeping
	// it independent makes cancelFlusher() the sole stop trigger, so the order
	// "enqueue stopped -> cancelFlusher() -> flusher drains it -> exits" holds.
	flusherCtx, cancelFlusher := context.WithCancel(context.Background())
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		s.runEmitFlusher(flusherCtx)
	}()

	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go s.runIngestWorker(workerCtx, workerDone)

	s.emitDaemonState("running")
	defer func() {
		s.setState(StateStopped)
		s.emitDaemonStateBlocking("stopped")

		cancelFlusher()
		<-flusherDone

		// Cancel the worker explicitly: the early-return paths below (all
		// sources failed, watcher init failed) return while the parent ctx is
		// still alive, so without this cancel <-workerDone would block forever
		// and Run (and its caller) would hang instead of reporting the error.
		cancelWorker()
		<-workerDone
	}()

	var startupOK bool
	var startupErr error
	for _, source := range s.cfg.Sources {
		if err := s.runFull(ctx, source, "startup"); err != nil {
			if startupErr == nil {
				startupErr = err
			}
			continue
		}
		startupOK = true
	}
	if !startupOK && startupErr != nil {
		return fmt.Errorf("daemon startup: every source failed: %w", startupErr)
	}

	watcher, err := newSourceWatcher(s.cfg.Sources, s.rootsBySource())
	if err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}
	// Closure so a reconcile-time rebuild closes the latest watcher, not the
	// original captured value.
	defer func() { watcher.Close() }()
	s.setWatchedPaths(len(watcher.watched))

	if s.cfg.Provider != nil {
		s.cfg.Provider.OnChange(func(_ *config.Snapshot) {
			select {
			case s.reconcileCh <- struct{}{}:
			default:
			}
		})
	}

	ticker := time.NewTicker(s.cfg.interval())
	defer ticker.Stop()

	debounceTimer := time.NewTimer(time.Hour)
	if !debounceTimer.Stop() {
		<-debounceTimer.C
	}
	defer debounceTimer.Stop()
	debounceActive := false

	// pendingFiles is a set so dedup within a debounce window is O(1) per add
	// instead of an O(n) slices.Contains scan on the run-loop goroutine.
	pendingFiles := map[string]struct{}{}
	flushPendingFiles := func(reason string) {
		// slices.Sorted already returns the keys in sorted order, giving a stable
		// ingest order despite unordered map iteration.
		files := slices.Sorted(maps.Keys(pendingFiles))
		clear(pendingFiles)
		s.setPendingFiles(0)
		if len(files) == 0 {
			return
		}
		s.enqueueAuto(ingestJob{mode: "file", paths: files, reason: reason})
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.reconcileCh:
			nw, rerr := newSourceWatcher(s.cfg.Sources, s.rootsBySource())
			if rerr != nil {
				s.cfg.logger().Warn("config roots reconcile: rebuild watcher failed; keeping current", "err", rerr)
				break
			}
			watcher.Close()
			watcher = nw
			s.setWatchedPaths(len(watcher.watched))
			s.enqueueFullAll("config-reload")
		case req := <-s.pauseCh:
			s.handlePause(req)
		case req := <-s.resumeCh:
			wasPaused := s.getState() == StatePaused
			s.handleResume(req)
			if wasPaused && s.getState() == StateRunning {
				if debounceActive {
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceActive = false
				}
				flushPendingFiles("resume")
			}
		case event := <-watcher.Events:
			// Register newly created directories for watching regardless of
			// pause state: this is the only run path that recursively watches
			// new project/session subdirs, and skipping it while paused leaves
			// those dirs permanently unwatched (a lasting live-latency regression).
			if event.Op&fsnotify.Create != 0 {
				_ = watcher.AddCreatedPath(event.Name)
				s.setWatchedPaths(len(watcher.watched))
			}
			if s.getState() == StatePaused {

				break
			}
			if watcher.ShouldIngest(event) {

				before := len(pendingFiles)
				pendingFiles[event.Name] = struct{}{}
				// Emit one activity event per file per debounce window. A single
				// save often fires several fsnotify Writes (write, chmod, sync);
				// coalescing on first-seen keeps that burst from flooding the
				// firehose. The debounced file ingest below still carries the
				// real session update.
				if len(pendingFiles) > before {
					s.emit(liveEventForActivity(s.providerForPath(event.Name), event.Name))
				}
				s.setPendingFiles(len(pendingFiles))
				if debounceActive {
					if !debounceTimer.Stop() {

						select {
						case <-debounceTimer.C:
						default:
						}
					}
				}
				debounceTimer.Reset(s.cfg.debounce())
				debounceActive = true
			}
		case err := <-watcher.Errors:
			s.cfg.logger().Warn("fsnotify error", "err", err)
		case <-debounceTimer.C:
			// Check pause before draining pendingFiles: if paused when the
			// timer fires, keep the pending list (and let it be re-flushed on
			// resume) instead of capturing-then-dropping it, which would lose
			// these targeted file-ingest jobs until the next full reconcile.
			if s.getState() == StatePaused {
				break
			}
			debounceActive = false
			flushPendingFiles("watch")
		case <-ticker.C:
			// Periodic full reconcile: a safety net for fsnotify events missed or
			// coalesced (the watch case above handles the common path). Runs at
			// DefaultInterval — deliberately infrequent because each pass walks the
			// tree; unchanged files are fingerprint-skipped so an idle pass is cheap.
			if s.getState() == StatePaused {
				break
			}
			s.enqueueFullAll("reconcile")
		}
	}
}

func (s *Service) runIngestWorker(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.ingestCh:
			res := s.execIngestJob(ctx, job)
			if job.reply != nil {
				select {
				case job.reply <- res:
				default:
				}
			}
		}
	}
}

func (s *Service) execIngestJob(ctx context.Context, job ingestJob) TriggerResult {
	res := TriggerResult{Mode: job.mode, Source: job.source, Reason: job.reason, Accepted: true}
	// Bound each job so a hung filesystem call cannot pin the single ingest worker
	// goroutine indefinitely (which would fill ingestCh and make enqueueAuto
	// silently drop every later job while the daemon still looks healthy).
	switch job.mode {
	case "file":
		for _, path := range job.paths {
			if err := s.runFileForAnySource(ctx, path); err != nil {
				res.Error = err.Error()
				break
			}
		}
	case "full":
		jobCtx, cancel := context.WithTimeout(ctx, s.cfg.fullIngestTimeout())
		defer cancel()
		sources := s.cfg.Sources
		if job.source != "" {
			sources = []string{job.source}
		}
		for _, source := range sources {
			if err := s.runFull(jobCtx, source, job.reason); err != nil {
				res.Error = err.Error()
				break
			}
		}
	}
	return res
}

func (s *Service) enqueueAuto(job ingestJob) {
	select {
	case s.ingestCh <- job:
	default:
		s.cfg.logger().Warn("ingest queue full; dropping automatic job",
			"mode", job.mode, "source", job.source, "paths", job.paths)
	}
}

func (s *Service) enqueueFullAll(reason string) {
	for _, source := range s.cfg.Sources {
		s.enqueueAuto(ingestJob{mode: "full", source: source, reason: reason})
	}
}

func (s *Service) providerForPath(path string) string {
	// rootsFor returns the already-resolved, cleaned Snapshot roots, so match
	// against them directly instead of re-running DiscoverRootPaths (and its
	// per-call clean/dedup allocation) on every fsnotify Write.
	for _, source := range s.cfg.Sources {
		for _, root := range s.rootsFor(source) {
			if root != "" && fsx.PathWithin(root, path) {
				return source
			}
		}
	}
	return ""
}

func (s *Service) handlePause(req controlReq) {
	if s.getState() == StateRunning {
		s.setState(StatePaused)
		s.emitDaemonState("paused")
	}
	req.reply <- nil
}

func (s *Service) handleResume(req controlReq) {
	if s.getState() == StatePaused {
		s.setState(StateRunning)
		s.emitDaemonState("running")
		s.enqueueFullAll("resume")
	}
	req.reply <- nil
}

func (s *Service) runFull(ctx context.Context, sourceName, reason string) error {
	if err := s.acquireIngest(ctx); err != nil {
		return err
	}
	defer s.releaseIngest()
	return s.runFullLocked(ctx, sourceName, reason)
}

func (s *Service) runFullLocked(ctx context.Context, sourceName, reason string) error {

	summary, err := ingest.RunFull(ctx, s.store, ingest.Options{
		Source: sourceName,
		Roots:  s.rootsFor(sourceName),
		Policy: s.policy(),
	})
	if err != nil {
		s.cfg.logger().Warn("daemon ingest failed", "reason", reason, "err", err)
		s.incrFullFailure()
		return err
	}
	s.printf("ingested mode=full reason=%s source=%s files=%d turns=%d raw_events=%d\n",
		reason, summary.Source, summary.Files, summary.TurnCount, summary.RawEventCount)
	s.recordFull(reason)
	s.emit(liveEventForFullIngest(summary, reason))
	return nil
}

func (s *Service) runFileForAnySource(ctx context.Context, path string) error {
	if err := s.acquireIngest(ctx); err != nil {
		return err
	}
	defer s.releaseIngest()
	// fileCtx is used only inside this first loop; a single defer is leak-proof
	// against any future early-return (cancel is idempotent).
	fileCtx, cancelFile := context.WithTimeout(ctx, s.cfg.fileIngestTimeout())
	defer cancelFile()
	for _, sourceName := range s.cfg.Sources {
		result, ok, err := ingest.RunFile(fileCtx, ingest.Options{
			Source: sourceName,
			Roots:  s.rootsFor(sourceName),
			Policy: s.policy(),
		}, path)
		if err != nil {
			s.cfg.logger().Warn("daemon file ingest failed", "source", sourceName, "path", path, "err", err)
			s.incrFileFailure()
			return err
		}
		if !ok {
			continue
		}
		if err := s.store.SaveSessionIngest(fileCtx, result.Index, result.RawEventList, result.Fingerprints); err != nil {
			s.cfg.logger().Warn("daemon file save failed", "source", sourceName, "path", path, "err", err)
			s.incrFileFailure()
			return err
		}
		s.printf("ingested mode=file source=%s file=%s turns=%d raw_events=%d\n",
			result.Index.Source, path, result.Index.TurnCount, result.Index.RawEventCount)
		s.recordFile(path)
		s.emit(liveEventForSessionIngest(result, path))
		return nil
	}
	s.incrUnmapped()

	if !s.claimUnmappedFullSlot(time.Now().UTC(), unmappedFullWindow) {
		s.cfg.logger().Debug("skipping unmapped-file full reconcile (rate-limited)", "path", path)
		return nil
	}
	fullCtx, cancelFull := context.WithTimeout(ctx, s.cfg.fullIngestTimeout())
	defer cancelFull()
	var firstErr error
	for _, sourceName := range s.cfg.Sources {

		if err := s.runFullLocked(fullCtx, sourceName, "unmapped-file"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

const unmappedFullWindow = 60 * time.Second

func (s *Service) emit(ev liveevent.Event) {
	if s.cfg.Emitter == nil || s.emitCh == nil {
		return
	}
	select {
	case s.emitCh <- ev:
	default:
		s.cfg.logger().Warn("emit channel full; dropping live event", "type", ev.Type)
	}
}

// emitDaemonState publishes a daemon lifecycle transition through the same
// non-blocking emitCh as ordinary live events. The terminal stopped event uses
// emitDaemonStateBlocking so a full channel cannot silently drop shutdown state.
func (s *Service) emitDaemonState(state string) {
	s.emit(liveEventForDaemonState(state))
}

func (s *Service) emitDaemonStateBlocking(state string) {
	if s.cfg.Emitter == nil || s.emitCh == nil {
		return
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case s.emitCh <- liveEventForDaemonState(state):
	case <-timer.C:
		s.cfg.logger().Warn("emit channel full; dropping daemon state", "state", state)
	}
}

func (s *Service) runEmitFlusher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// Drain any events still buffered in emitCh before returning so
			// watch/reconcile live events produced just before shutdown are not
			// silently lost. Each is emitted with a fresh short-lived context
			// since ctx is already cancelled.
			s.drainEmitCh()
			return
		case ev, ok := <-s.emitCh:
			if !ok {
				return
			}
			if s.cfg.Emitter == nil {
				continue
			}
			if _, err := s.cfg.Emitter.Emit(ctx, ev); err != nil {
				s.cfg.logger().Warn("emit live event failed", "type", ev.Type, "err", err)
			}
		}
	}
}

func (s *Service) drainEmitCh() {
	if s.cfg.Emitter == nil || s.emitCh == nil {
		return
	}
	for {
		select {
		case ev, ok := <-s.emitCh:
			if !ok {
				return
			}
			drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if _, err := s.cfg.Emitter.Emit(drainCtx, ev); err != nil {
				s.cfg.logger().Warn("emit live event failed", "type", ev.Type, "err", err)
			}
			cancel()
		default:
			return
		}
	}
}

func (s *Service) printf(format string, args ...any) {
	if s.cfg.Stdout == nil {
		return
	}
	_, _ = fmt.Fprintf(s.cfg.Stdout, format, args...)
}
