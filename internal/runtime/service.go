package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/store/sqlite"
)

type Service struct {
	cfg   Config
	store *sqlite.Store

	mu   sync.Mutex
	prog progress

	ingestCh chan ingestJob
	pauseCh  chan controlReq
	resumeCh chan controlReq

	ingestSem chan struct{}

	emitCh chan liveevent.Event

	reconcileCh chan struct{}

	// Backpressure counters: cumulative, monotonic, lock-free. emit is called
	// from several goroutines, so a mutex-guarded counter would serialize the
	// hot path — atomics keep it free. Surfaced via Status().Counters.
	ingestAutoDropped atomic.Uint64
	emitDropped       atomic.Uint64
}

const emitBufferSize = 256

const ingestQueueSize = 64

type ingestJob struct {
	mode   string
	source string
	paths  []string
	reason string
	reply  chan TriggerResult
}

type controlReq struct {
	reply chan error
}

func New(store *sqlite.Store, cfg Config) (*Service, error) {
	if store == nil {
		return nil, errors.New("runtime: store is nil")
	}
	if len(cfg.Sources) == 0 {
		return nil, errors.New("runtime: at least one source required")
	}
	return &Service{
		cfg:         cfg,
		store:       store,
		ingestCh:    make(chan ingestJob, ingestQueueSize),
		ingestSem:   make(chan struct{}, 1),
		pauseCh:     make(chan controlReq),
		resumeCh:    make(chan controlReq),
		emitCh:      make(chan liveevent.Event, emitBufferSize),
		reconcileCh: make(chan struct{}, 1),
	}, nil
}

// policy returns the effective redact policy: the live Snapshot when a Provider
// is wired (daemon/serve hot-reload), else the static startup Policy.
func (s *Service) policy() redact.Policy {
	if s.cfg.Provider != nil {
		return s.cfg.Provider.Current().RedactPolicy
	}
	return s.cfg.Policy
}

// rootsFor returns the effective roots for source from the live Snapshot. The
// nil-Provider guard mirrors policy(); New does not require a Provider, so the
// method must not dereference it unconditionally.
func (s *Service) rootsFor(source string) []string {
	if s.cfg.Provider != nil {
		return s.cfg.Provider.Current().Roots[source]
	}
	return nil
}

// rootsBySource builds the per-source roots map used to (re)build the watcher.
func (s *Service) rootsBySource() map[string][]string {
	m := make(map[string][]string, len(s.cfg.Sources))
	for _, src := range s.cfg.Sources {
		m[src] = s.rootsFor(src)
	}
	return m
}

func (s *Service) acquireIngest(ctx context.Context) error {
	select {
	case s.ingestSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseIngest() { <-s.ingestSem }

func (s *Service) Status() Status {
	s.mu.Lock()
	st := s.prog.snapshot(s.cfg)
	s.mu.Unlock()
	st.Counters.IngestAutoDropped = s.ingestAutoDropped.Load()
	st.Counters.EmitDropped = s.emitDropped.Load()
	return st
}

func (s *Service) getState() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prog.state
}

func (s *Service) Pause(ctx context.Context) error {
	if s.getState() == StateStopped {
		return errors.New("runtime: service not running")
	}
	return s.sendControl(ctx, s.pauseCh)
}

func (s *Service) Resume(ctx context.Context) error {
	if s.getState() == StateStopped {
		return errors.New("runtime: service not running")
	}
	return s.sendControl(ctx, s.resumeCh)
}

func (s *Service) sendControl(ctx context.Context, ch chan controlReq) error {
	reply := make(chan error, 1)
	select {
	case ch <- controlReq{reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) TriggerIngest(ctx context.Context, req TriggerRequest) (TriggerResult, error) {
	if req.Reason == "" {
		req.Reason = "manual"
	}

	if req.Mode == "once" {
		err := s.RunOnce(ctx)
		res := TriggerResult{
			Mode:     "once",
			Source:   req.Source,
			Reason:   req.Reason,
			Accepted: true,
		}
		if err != nil {
			res.Error = err.Error()
		}
		return res, err
	}

	if s.getState() == StateStopped {
		return TriggerResult{
			Mode:   req.Mode,
			Reason: req.Reason,
			Error:  "service not running",
		}, errors.New("runtime: service not running")
	}

	switch req.Mode {
	case "full":

	case "file":
		if req.Path == "" {
			return TriggerResult{Mode: req.Mode, Reason: req.Reason, Error: "file mode requires path"},
				errors.New("runtime: file mode requires path")
		}
	default:
		return TriggerResult{Mode: req.Mode, Reason: req.Reason, Error: fmt.Sprintf("unsupported mode %q", req.Mode)},
			fmt.Errorf("runtime: unsupported mode %q", req.Mode)
	}

	var reply chan TriggerResult
	if req.Sync {
		reply = make(chan TriggerResult, 1)
	}

	job := ingestJob{mode: req.Mode, source: req.Source, reason: req.Reason, reply: reply}
	if req.Path != "" {
		job.paths = []string{req.Path}
	}
	select {
	case s.ingestCh <- job:
	case <-ctx.Done():
		return TriggerResult{}, ctx.Err()
	}
	if !req.Sync {
		return TriggerResult{Mode: req.Mode, Reason: req.Reason, Source: req.Source, Accepted: true}, nil
	}
	select {
	case res := <-reply:
		return res, nil
	case <-ctx.Done():
		return TriggerResult{}, ctx.Err()
	}
}

type TriggerRequest struct {
	Mode   string `json:"mode"`
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
	Sync   bool   `json:"sync,omitempty"`
}

type TriggerResult struct {
	Mode     string `json:"mode"`
	Source   string `json:"source,omitempty"`
	Reason   string `json:"reason"`
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}
