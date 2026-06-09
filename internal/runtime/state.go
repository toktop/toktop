package runtime

import (
	"slices"
	"time"
)

type State int

const (
	StateStopped State = iota
	StateRunning
	StatePaused
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateRunning:
		return "running"
	case StatePaused:
		return "paused"
	default:
		return "unknown"
	}
}

type Counters struct {
	FullRuns      int64 `json:"full_runs"`
	FullFailures  int64 `json:"full_failures"`
	FileRuns      int64 `json:"file_runs"`
	FileFailures  int64 `json:"file_failures"`
	UnmappedFiles int64 `json:"unmapped_files"`
}

type Status struct {
	State          string     `json:"state"`
	Sources        []string   `json:"sources"`
	Interval       string     `json:"interval"`
	Debounce       string     `json:"debounce"`
	StartedAt      time.Time  `json:"started_at,omitzero"`
	LastFullAt     *time.Time `json:"last_full_at,omitempty"`
	LastFullReason string     `json:"last_full_reason,omitempty"`
	LastFileAt     *time.Time `json:"last_file_at,omitempty"`
	LastFilePath   string     `json:"last_file_path,omitempty"`
	PendingFiles   int        `json:"pending_files"`
	WatchedPaths   int        `json:"watched_paths"`
	Counters       Counters   `json:"counters"`
}

type progress struct {
	state              State
	startedAt          time.Time
	lastFullAt         time.Time
	lastFullReason     string
	lastFileAt         time.Time
	lastFilePath       string
	lastUnmappedFullAt time.Time
	pendingFiles       int
	watchedPaths       int
	counters           Counters
}

func clonePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return new(t)
}

func (p progress) snapshot(cfg Config) Status {
	return Status{
		State:          p.state.String(),
		Sources:        slices.Clone(cfg.Sources),
		Interval:       cfg.interval().String(),
		Debounce:       cfg.debounce().String(),
		StartedAt:      p.startedAt,
		LastFullAt:     clonePtr(p.lastFullAt),
		LastFullReason: p.lastFullReason,
		LastFileAt:     clonePtr(p.lastFileAt),
		LastFilePath:   p.lastFilePath,
		PendingFiles:   p.pendingFiles,
		WatchedPaths:   p.watchedPaths,
		Counters:       p.counters,
	}
}
