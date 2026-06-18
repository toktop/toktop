package ingest

import (
	"context"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/source"
)

type BatchParser[F any] func(ctx context.Context, batch []F) (Result, error)

type MetadataFn func(fingerprints map[string]source.Fingerprint) (Result, bool, error)

func StreamSessions[F any](
	ctx context.Context,
	sessions []F,
	pathOf func(F) string,
	fingerprintOf func(F) (source.Fingerprint, bool),
	byteSizeOf func(F) int64,
	known map[string]source.Fingerprint,
	metadata MetadataFn,
	parseBatch BatchParser[F],
	maxBatchBytes int64,
	sink BatchSink,
) (Summary, error) {
	changed, _, fingerprints := collector.PartitionByFingerprint(sessions, pathOf, fingerprintOf, known)
	summary := Summary{Files: len(sessions), Fingerprints: fingerprints}

	if metadata != nil {
		meta, ok, err := metadata(fingerprints)
		if err != nil {
			return summary, err
		}
		if ok {
			if err := sink(ctx, meta); err != nil {
				return summary, err
			}
		}
	}

	// Default batch-sizing reads the byte size straight from the fingerprints map
	// PartitionByFingerprint already populated (zero extra syscalls) — the prior
	// behavior file providers rely on. A provider whose fingerprint Size is not a
	// byte count (opencode packs a seq into Token, leaving Size 0) supplies
	// Spec.ByteSizeOf to weight batches itself.
	sizeOf := byteSizeOf
	if sizeOf == nil {
		sizeOf = func(f F) int64 { return fingerprints[pathOf(f)].Size }
	}
	batches := collector.ChunkBySize(changed, sizeOf, maxBatchBytes)
	if len(batches) == 0 {
		return summary, nil
	}

	// Pipeline the serial sqlite write behind parse+redact: a dedicated writer
	// goroutine drains redacted batches off an unbuffered channel while the
	// producer parses the next batch. The unbuffered channel keeps the pipeline one
	// batch deep (bounded memory, same backpressure as the old synchronous loop).
	//
	// Error semantics match the previous synchronous code: a sink error is returned
	// and aborts the run. The writer signals exactly once on errCh — its error, or
	// nil at clean completion — and the producer always reads it before returning,
	// so the writer (and its in-flight Commit) is fully joined before StreamSessions
	// returns. That join matters because RunFull is re-entered by the daemon
	// reconcile; no writer goroutine may outlive a call.
	writeCh := make(chan Result)
	errCh := make(chan error, 1)
	go func() {
		for r := range writeCh {
			if err := sink(ctx, r); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	var loopErr error
	for _, batch := range batches {
		result, err := parseBatch(ctx, batch)
		if err != nil {
			loopErr = err
			break
		}
		result.Fingerprints = fingerprints
		summary.SessionCount += result.Index.SessionCount
		summary.TurnCount += result.Index.TurnCount
		summary.InvocationCount += result.Index.InvocationCount
		summary.ToolCallCount += result.Index.ToolCallCount
		summary.RawEventCount += result.Index.RawEventCount
		summary.ParseErrorCount += len(result.Index.ParseErrorList)
		select {
		case writeCh <- result:
		case werr := <-errCh:
			// The writer already failed on an earlier batch and returned; surface
			// that error and stop parsing further batches.
			return summary, werr
		}
	}
	close(writeCh)
	werr := <-errCh
	if loopErr != nil {
		return summary, loopErr
	}
	return summary, werr
}
