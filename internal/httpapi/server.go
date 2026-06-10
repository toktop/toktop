package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/httpapi/internal/eventlog"
	"toktop.unceas.dev/internal/query"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/runtime"
	"toktop.unceas.dev/internal/store/sqlite"
)

const maxHookIntakeBytes int64 = 16 << 20
const maxLiveEventBytes int64 = 1 << 20

type Options struct {
	DataDir string
	Token   string
	Logger  *slog.Logger

	// WipeGuard gates the store's destructive schema-epoch rebuild; see
	// sqlite.WipeGuard. nil leaves the wipe unguarded.
	WipeGuard sqlite.WipeGuard

	ConfigLoader *config.Loader

	// IdleShutdownAfter, when > 0, makes the server self-stop (via OnIdle) after
	// this long with zero SSE subscribers. 0 disables (resident).
	IdleShutdownAfter time.Duration
	// OnIdle is called once when the idle monitor decides to stop. Wire it to the
	// daemon/serve loop's cancel so the whole process shuts down gracefully.
	OnIdle func()
}

const (
	DefaultEventLogMaxAge     = 7 * 24 * time.Hour
	DefaultEventLogMaxEvents  = 100_000
	DefaultEventLogGCInterval = 6 * time.Hour
)

type Server struct {
	store      *sqlite.Store
	eventStore eventlog.Store
	service    *query.Service
	token      string
	mux        *http.ServeMux
	logger     *slog.Logger

	// emitMu serializes the Append+publish pair so monotonic event IDs are
	// assigned and fanned out in the same order; without it concurrent emitters
	// can publish out of ID order and an event can be lost on reconnect.
	// Lock order, whenever more than one is held: emitMu → liveMu → eventsMu
	// (Emit is the only multi-lock path). Never invert.
	emitMu sync.Mutex

	eventsMu sync.Mutex
	events   map[*sseSubscriber]struct{}
	// lastPublishedID is the highest event ID handed to publishEvent — the
	// reconnect watermark. Protected by eventsMu: only ever read/written under
	// it (subscribeEvents reads, publishEvent advances). Do not add a lock-free
	// reader.
	lastPublishedID uint64

	// replayMu lets a reconnect replay (and startup loadLiveState) run to
	// completion without Prune deleting events out from under it, which would
	// otherwise silently skip a pruned range with no resync. Replay holds RLock
	// for the whole walk; PruneEventLog holds Lock. It is a plain Go mutex, not a
	// bbolt txn, so holding it across socket writes is fine — it only makes the
	// periodic GC wait for an in-flight replay, never the emit hot path.
	replayMu sync.RWMutex

	liveMu       sync.Mutex
	liveSessions map[string]LiveEvent

	runtime atomic.Pointer[runtime.Service]

	cfgLoader *config.Loader

	// Hook spool writes are deferred to a single background goroutine
	// (runSpoolWriter) off the request path, so handleHooksIntake never blocks on
	// the full-body redact scan + file I/O. spoolCh carries raw bodies; spoolFile
	// and spoolDate are owned solely by runSpoolWriter (and, after the channel has
	// drained on shutdown via stopSpooler, by Close). No mutex is needed: the
	// channel close is the happens-before edge for the post-drain spoolFile.Close.
	spoolCh   chan []byte
	spoolDone chan struct{}
	spoolOnce sync.Once
	spoolFile *os.File
	spoolDate string

	eventLogMaxAge     time.Duration
	eventLogMaxEvents  int
	eventLogGCInterval time.Duration

	idleShutdownAfter time.Duration
	onIdle            func()

	// eventSeq assigns monotonic live-event ids from memory (seeded from the
	// event log's LastID at startup) so Emit can fan out to SSE before the
	// durable bbolt write. persistCh hands that durable write to a single
	// background goroutine off the emit critical path; persistDone closes once it
	// has drained on shutdown, and persistOnce makes the drain idempotent.
	eventSeq atomic.Uint64
	// durableID is the highest event id the persister has actually written to the
	// event log. Reconnect replay reads from that log, so it waits for durableID
	// to reach the requested watermark before paging (see writeReplayEvents) —
	// this keeps the async write off the emit hot path WITHOUT letting a
	// reconnecting client's replay range outrun durable storage, which would
	// silently drop the newest events. Advanced only by runEventPersister (single
	// writer, FIFO), seeded to LastID at startup since the prior log is durable.
	durableID   atomic.Uint64
	persistCh   chan persistJob
	persistDone chan struct{}
	persistOnce sync.Once
}

// persistJob is one durable event-log write deferred off the emit hot path.
type persistJob struct {
	id        uint64
	eventType string
	at        time.Time
	payload   []byte
}

const eventPersistBuffer = 4096

// spoolBuffer is intentionally far smaller than eventPersistBuffer: each queued
// item is a full hook body (up to maxHookIntakeBytes = 16 MiB), not a small
// persistJob, so a deep backlog would pin a lot of memory. The writer drains
// fast and depth stays ~0; on overflow the spool line is dropped (warned),
// never the live event.
const spoolBuffer = 256

// replayGuardedStore wraps the event log so its Prune blocks while a reconnect
// replay is in flight (Server.replayMu), matching PruneEventLog's own guard.
// retention.Apply prunes the event log directly via the eventlog.Store it is
// handed, so handing it this wrapper is what keeps a retention prune from
// deleting a range out from under an active replay. Every other method delegates
// unchanged via the embedded interface.
type replayGuardedStore struct {
	eventlog.Store
	mu *sync.RWMutex
}

func (g replayGuardedStore) Prune(ctx context.Context, before time.Time, keepN int) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Store.Prune(ctx, before, keepN)
}

func NewServer(ctx context.Context, opts Options) (*Server, error) {
	store, err := sqlite.Open(ctx, opts.DataDir, opts.WipeGuard)
	if err != nil {
		return nil, err
	}
	hotStore, err := eventlog.Open(ctx, opts.DataDir)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	lastID, err := hotStore.LastID(ctx)
	if err != nil {
		_ = hotStore.Close()
		_ = store.Close()
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{
		store:              store,
		eventStore:         hotStore,
		service:            query.New(store),
		token:              opts.Token,
		cfgLoader:          opts.ConfigLoader,
		mux:                http.NewServeMux(),
		logger:             logger,
		events:             make(map[*sseSubscriber]struct{}),
		lastPublishedID:    lastID,
		liveSessions:       make(map[string]LiveEvent),
		eventLogMaxAge:     DefaultEventLogMaxAge,
		eventLogMaxEvents:  DefaultEventLogMaxEvents,
		eventLogGCInterval: DefaultEventLogGCInterval,
		idleShutdownAfter:  opts.IdleShutdownAfter,
		onIdle:             opts.OnIdle,
	}
	// Seed the in-memory id sequence from the durable floor and start the async
	// persister before any Emit can run. loadLiveState below replays from bbolt
	// directly (it does not Emit), so the sequence stays at lastID until the
	// first live event.
	server.eventSeq.Store(lastID)
	server.durableID.Store(lastID) // the pre-restart log up to lastID is already durable
	server.persistCh = make(chan persistJob, eventPersistBuffer)
	server.persistDone = make(chan struct{})
	go server.runEventPersister()
	server.spoolCh = make(chan []byte, spoolBuffer)
	server.spoolDone = make(chan struct{})
	go server.runSpoolWriter()
	if err := server.loadLiveState(ctx); err != nil {
		_ = server.Close()
		return nil, err
	}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.middleware(s.mux)
}

func (s *Server) Close() error {
	// Drain the async event persister before closing the event store it writes
	// to, so live events published just before shutdown still reach the replay
	// log. Idempotent: Close runs on both the NewServer error path and the two
	// ListenAndServe exit paths.
	s.stopPersister()
	s.stopSpooler()
	s.eventsMu.Lock()
	for sub := range s.events {
		close(sub.ch)
		delete(s.events, sub)
	}
	s.eventsMu.Unlock()
	var err error
	if s.eventStore != nil {
		err = s.eventStore.Close()
	}
	if storeErr := s.store.Close(); err == nil {
		err = storeErr
	}
	// stopSpooler above has drained spoolCh and runSpoolWriter has returned, so
	// spoolFile is no longer touched concurrently — close it without a lock.
	if s.spoolFile != nil {
		if closeErr := s.spoolFile.Close(); err == nil {
			err = closeErr
		}
		s.spoolFile = nil
	}
	return err
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	// Build the gitleaks detector off the hot path so the first live-event
	// redaction (Emit on a hook with a Reason) doesn't pay the one-time
	// ruleset parse. Runs in the background so it never delays binding.
	go redact.Warm()

	listener, err := s.listen(addr)
	if err != nil {
		_ = s.Close()
		return err
	}
	// A unix socket file is not removed by the OS when the listener closes or the
	// process exits — it persists on disk. Remove it on graceful shutdown so
	// `toktop daemon stop` can poll for its disappearance to confirm the daemon
	// has stopped.
	var socketPath string
	if network, address := SplitListenAddr(addr); network == "unix" {
		socketPath = address
	}
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	gcCtx, cancelGC := context.WithCancel(ctx)
	defer cancelGC()
	go s.runEventLogGC(gcCtx)

	if s.idleShutdownAfter > 0 && s.onIdle != nil {
		go s.runIdleMonitor(gcCtx)
	}

	errs := make(chan error, 1)
	go func() {
		errs <- httpServer.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = s.Close()
		if socketPath != "" {
			_ = os.Remove(socketPath)
		}
		return nil
	case err := <-errs:
		_ = s.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// listen builds the net.Listener for addr. A unix socket (the default) is bound
// with its parent dir at 0700 and the socket at 0600 — OS file permissions ARE
// the access control, so no bearer token is needed. A TCP address is an explicit
// opt-in downgrade: off loopback it still requires a token, with a stderr warning.
func (s *Server) listen(addr string) (net.Listener, error) {
	network, address := SplitListenAddr(addr)
	if network == "unix" {
		if err := os.MkdirAll(filepath.Dir(address), 0o700); err != nil {
			return nil, fmt.Errorf("create socket dir: %w", err)
		}
		// Remove a stale socket left by a crash/previous run, else bind fails with
		// "address already in use".
		if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove stale socket %s: %w", address, err)
		}
		l, err := net.Listen("unix", address)
		if err != nil {
			return nil, fmt.Errorf("listen unix %s: %w", address, err)
		}
		if err := os.Chmod(address, 0o600); err != nil {
			_ = l.Close()
			return nil, fmt.Errorf("chmod socket %s: %w", address, err)
		}
		return l, nil
	}
	if !isLoopbackAddr(address) {
		if s.token == "" {
			return nil, fmt.Errorf("refusing unauthenticated non-loopback API address %s; use a unix socket, 127.0.0.1, or configure a bearer token", address)
		}
		fmt.Fprintf(os.Stderr, "warning: toktop is listening on %s (not localhost); the API now accepts connections from outside this machine.\n", address)
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen tcp %s: %w", address, err)
	}
	return l, nil
}

// runEventPersister drains persistCh and writes each event to the durable replay
// log, off the Emit hot path. It uses a fresh background context per write so a
// cancelled HTTP request (the original Emit caller) never aborts persistence.
func (s *Server) runEventPersister() {
	defer close(s.persistDone)
	for job := range s.persistCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.eventStore.AppendWithID(ctx, job.id, job.eventType, job.at, job.payload); err != nil {
			s.logger.Warn("persist live event to replay log failed",
				"event_id", job.id, "type", job.eventType, "err", err)
		} else {
			// Jobs are drained FIFO in id order, so this only advances. Reconnect
			// replay waits on it to keep the watermark backed by durable storage.
			s.durableID.Store(job.id)
		}
		cancel()
	}
}

// runIdleMonitor stops the daemon (via onIdle) once it has had zero SSE
// subscribers for idleShutdownAfter. lastActive is seeded to startup, so a
// daemon that never gets a consumer (e.g. auto-started just for a one-shot
// `status`) self-stops after the grace. Any observed subscriber resets the timer.
func (s *Server) runIdleMonitor(ctx context.Context) {
	lastActive := time.Now()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.subscriberCount() > 0 {
				lastActive = time.Now()
				continue
			}
			if time.Since(lastActive) >= s.idleShutdownAfter {
				s.logger.Info("no live consumers; stopping idle daemon",
					"idle_for", time.Since(lastActive).Round(time.Second).String())
				s.onIdle()
				return
			}
		}
	}
}

// stopPersister closes persistCh and waits for the persister to drain. Safe to
// call multiple times (Close runs on several exit paths).
func (s *Server) stopPersister() {
	s.persistOnce.Do(func() {
		if s.persistCh != nil {
			close(s.persistCh)
			<-s.persistDone
		}
	})
}

// enqueueEventPersist hands a durable write to the background persister without
// blocking the emit critical section. On overflow the event is still published
// live over SSE; it just won't be in the replay log, so a reconnecting client
// may need a /v1/status resync for it — consistent with the broker's
// lossy-by-design delivery (see the emitCh drop path).
func (s *Server) enqueueEventPersist(id uint64, eventType string, at time.Time, payload []byte) {
	select {
	case s.persistCh <- persistJob{id: id, eventType: eventType, at: at, payload: payload}:
	default:
		s.logger.Warn("event persist queue full; live event not written to replay log",
			"event_id", id, "type", eventType)
	}
}
