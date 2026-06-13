package sqlite

import (
	"context"
	"fmt"
	"strings"
)

type CheckpointResult struct {
	// Busy is SQLite's wal_checkpoint busy flag: true means a RESTART/FULL/TRUNCATE
	// checkpoint was blocked by a concurrent reader/writer and did nothing (the WAL
	// was not reclaimed). It is the first column of PRAGMA wal_checkpoint — a 0/1
	// flag, NOT a frame count.
	Busy               bool `json:"busy"`
	LogFrames          int  `json:"log_frames"`
	CheckpointedFrames int  `json:"checkpointed_frames"`
}

type OptimizeResult struct {
	Checkpoint   CheckpointResult `json:"checkpoint"`
	FTSOptimized bool             `json:"fts_optimized"`
}

// NormalizeCheckpointMode upper-cases and validates a WAL checkpoint mode,
// defaulting an empty value to PASSIVE. It is the single source of truth for the
// allowed modes, shared by Checkpoint and the CLI so the two cannot drift.
func NormalizeCheckpointMode(mode string) (string, error) {
	switch normalized := strings.ToUpper(strings.TrimSpace(mode)); normalized {
	case "":
		return "PASSIVE", nil
	case "PASSIVE", "FULL", "RESTART", "TRUNCATE":
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid checkpoint mode %q (want passive, full, restart, or truncate)", mode)
	}
}

// Checkpoint runs SQLite WAL checkpoint maintenance on the writer connection.
func (s *Store) Checkpoint(ctx context.Context, mode string) (CheckpointResult, error) {
	mode, err := NormalizeCheckpointMode(mode)
	if err != nil {
		return CheckpointResult{}, err
	}
	var result CheckpointResult
	var busy int
	stmt := "PRAGMA wal_checkpoint(" + mode + ")"
	if err := s.writer().QueryRowContext(ctx, stmt).Scan(&busy, &result.LogFrames, &result.CheckpointedFrames); err != nil {
		return CheckpointResult{}, fmt.Errorf("run %s: %w", stmt, err)
	}
	result.Busy = busy != 0
	return result, nil
}

// Optimize runs lightweight SQLite planner and FTS5 maintenance without
// rebuilding the projection.
func (s *Store) Optimize(ctx context.Context) (OptimizeResult, error) {
	checkpoint, err := s.Checkpoint(ctx, "PASSIVE")
	if err != nil {
		return OptimizeResult{}, err
	}
	if _, err := s.writer().ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return OptimizeResult{}, fmt.Errorf("run PRAGMA optimize: %w", err)
	}
	if _, err := s.writer().ExecContext(ctx, `INSERT INTO search_fts(search_fts) VALUES('optimize')`); err != nil {
		return OptimizeResult{}, fmt.Errorf("optimize search_fts: %w", err)
	}
	return OptimizeResult{Checkpoint: checkpoint, FTSOptimized: true}, nil
}

// RebuildSearchIndex rebuilds the external-content FTS5 index from
// search_documents.
func (s *Store) RebuildSearchIndex(ctx context.Context) error {
	if _, err := s.writer().ExecContext(ctx, `INSERT INTO search_fts(search_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild search_fts: %w", err)
	}
	return nil
}
