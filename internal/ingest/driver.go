package ingest

import (
	"context"
	"fmt"
	"time"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

// Spec parameterizes the generic ingest Driver for one provider. F is the
// provider's session-file type; the driver needs only its absolute transcript
// path and discovery-root path (via PathOf/RootOf), so each provider keeps its
// own richer SessionFile shape.
type Spec[F any] struct {
	Source        string // provider name; also the Index.Source value
	ParserVersion string

	PathOf func(F) string // session file's absolute transcript path
	RootOf func(F) string // session file's discovery-root path

	// Collect reads one session file into a RawSession plus any per-file collect
	// errors (e.g. malformed lines that don't abort the whole file).
	Collect func(ctx context.Context, file F) (source.RawSession, []trace.ParseError, error)
	// Parse turns a RawSession into the neutral trace pieces. It wraps the
	// provider's parser, whose ParseResult is field-identical across providers.
	Parse func(ctx context.Context, raw source.RawSession) (trace.Session, []trace.Turn, []trace.ParseError, error)

	// FingerprintOf returns a file's change-detection fingerprint and whether it
	// is currently present. Nil ⇒ the default StatFingerprint(PathOf(f)) — the
	// file-backed behavior every existing provider relies on. A DB-backed provider
	// (opencode) supplies a seq-based fingerprint instead of a file stat.
	FingerprintOf func(F) (source.Fingerprint, bool)
	// ByteSizeOf returns a file's approximate parse cost in bytes, used only for
	// batch sizing. Nil ⇒ the fingerprint's Size field (correct for file
	// providers, where Size is the byte size). A provider whose fingerprint Size
	// is not a byte count (opencode packs a revision into Token, leaving Size 0)
	// must supply this so batches stay memory-bounded.
	ByteSizeOf func(F) int64
}

// fingerprintOf resolves the spec's fingerprint function, defaulting to a file
// stat so providers that leave FingerprintOf nil keep the exact prior behavior.
func (s Spec[F]) fingerprintOf(f F) (source.Fingerprint, bool) {
	if s.FingerprintOf != nil {
		return s.FingerprintOf(f)
	}
	size, mtimeNS, ino, ok := collector.StatFingerprint(s.PathOf(f))
	return source.Fingerprint{Size: size, MtimeNS: mtimeNS, Ino: ino}, ok
}

// byteSizeOf resolves the spec's batch-sizing weight, defaulting to the
// fingerprint Size (the byte size for file providers).
func (s Spec[F]) byteSizeOf(f F) int64 {
	if s.ByteSizeOf != nil {
		return s.ByteSizeOf(f)
	}
	fp, _ := s.fingerprintOf(f)
	return fp.Size
}

// Driver runs the shared, provider-neutral ingest pipeline (parse → accumulate →
// finalize → price → redact → intern) for a provider described by Spec. It lives
// in package ingest (not collector) because it returns ingest.Result, and the
// collector package must not depend on ingest.
type Driver[F any] struct {
	Spec Spec[F]
}

func NewDriver[F any](spec Spec[F]) Driver[F] { return Driver[F]{Spec: spec} }

// BatchBytesThreshold caps the total bytes of session files parsed per batch
// before the pipeline flushes. One value for every provider's Stream.
const BatchBytesThreshold = 32 << 20

// Stream runs the provider-neutral streaming ingest over an already-discovered
// session list: it emits provider metadata first, then parses changed files in
// size-bounded batches via IngestBatch. Providers supply their own discovery and
// metadata closure; the pipeline shape (parseBatch + StreamSessions) lives here
// once instead of in a byte-identical per-provider Ingest wrapper.
func (d Driver[F]) Stream(ctx context.Context, roots []SourceRoot, policy redact.Policy, sessions []F, known map[string]source.Fingerprint, metadata MetadataFn, sink BatchSink) (Summary, error) {
	parseBatch := func(ctx context.Context, batch []F) (Result, error) {
		return d.IngestBatch(ctx, roots, policy, batch)
	}
	return StreamSessions(ctx, sessions, d.Spec.PathOf, d.Spec.fingerprintOf, d.Spec.byteSizeOf, known, metadata, parseBatch, BatchBytesThreshold, sink)
}

func (d Driver[F]) newIndex(roots []SourceRoot, capHint int) trace.Index {
	return trace.Index{
		GeneratedAt:   time.Now().UTC(),
		Source:        d.Spec.Source,
		ParserVersion: d.Spec.ParserVersion,
		SourceRoots:   RootPaths(roots),
		Sessions:      make([]trace.Session, 0, capHint),
	}
}

// IngestBatch parses a batch of session files concurrently and accumulates them
// into one redacted, interned Result. A single unreadable/unparseable
// file is recorded as a ParseError and skipped (so it cannot abort the batch and
// every alphabetically-later batch); only context cancellation stays fatal.
func (d Driver[F]) IngestBatch(ctx context.Context, roots []SourceRoot, policy redact.Policy, batch []F) (Result, error) {
	index := d.newIndex(roots, len(batch))
	type parseOutput struct {
		raw           source.RawSession
		collect       []trace.ParseError
		session       trace.Session
		turns         []trace.Turn
		perrs         []trace.ParseError
		processedFile string
	}
	parsed, err := collector.SafeMapErr(ctx, batch, func(file *F) (parseOutput, error) {
		raw, collectErrors, err := d.Spec.Collect(ctx, *file)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return parseOutput{}, cerr
			}
			path := d.Spec.PathOf(*file)
			return parseOutput{collect: []trace.ParseError{d.fileSkipError(d.Spec.RootOf(*file), path, err)}, processedFile: path}, nil
		}
		session, turns, perrs, err := d.Spec.Parse(ctx, raw)
		if err != nil {
			collector.ReleaseRawJSON(raw.RawEventList)
			if cerr := ctx.Err(); cerr != nil {
				return parseOutput{}, cerr
			}
			path := d.Spec.PathOf(*file)
			return parseOutput{collect: []trace.ParseError{d.fileSkipError(d.Spec.RootOf(*file), path, err)}, processedFile: path}, nil
		}
		collector.ReleaseRawJSON(raw.RawEventList)
		return parseOutput{raw: raw, collect: collectErrors, session: session, turns: turns, perrs: perrs, processedFile: d.Spec.PathOf(*file)}, nil
	})
	if err != nil {
		return Result{}, err
	}
	var rawList []source.RawEvent
	processed := make([]string, 0, len(batch))
	for _, out := range parsed {
		if out.processedFile != "" {
			processed = append(processed, out.processedFile)
		}
		index.RawEventCount += len(out.raw.RawEventList)
		rawList = append(rawList, out.raw.RawEventList...)
		index.ParseErrorList = append(index.ParseErrorList, out.collect...)
		index.ParseErrorList = append(index.ParseErrorList, out.perrs...)
		if out.session.TurnCount == 0 && len(out.raw.RawEventList) == 0 {
			continue
		}
		index.Sessions = append(index.Sessions, out.session)
		index.Turns = append(index.Turns, out.turns...)
		for _, turn := range out.turns {
			index.Invocations = append(index.Invocations, turn.Invocations...)
			index.TurnComponents = append(index.TurnComponents, turn.Components...)
		}
	}
	collector.FinalizeCounts(&index)
	if err := policy.ApplyToIndex(ctx, &index); err != nil {
		return Result{}, err
	}
	trace.InternIndexStrings(&index)
	return Result{Index: index, RawEventList: rawList, ProcessedFiles: processed}, nil
}

// IngestSessionFile parses one session file into a redacted, interned
// Result. Unlike IngestBatch, a collect/parse error is returned (a single-file
// ingest has no sibling batch to protect).
func (d Driver[F]) IngestSessionFile(ctx context.Context, roots []SourceRoot, file F, policy redact.Policy) (Result, error) {
	index := d.newIndex(roots, 1)
	path := d.Spec.PathOf(file)
	fingerprints := make(map[string]source.Fingerprint, 1)
	if fp, ok := d.Spec.fingerprintOf(file); ok {
		fingerprints[path] = fp
	}
	raw, collectErrors, err := d.Spec.Collect(ctx, file)
	if err != nil {
		return Result{}, fmt.Errorf("collect %s: %w", path, err)
	}
	session, turns, perrs, err := d.Spec.Parse(ctx, raw)
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", path, err)
	}
	collector.ReleaseRawJSON(raw.RawEventList)
	index.RawEventCount = len(raw.RawEventList)
	index.ParseErrorList = append(index.ParseErrorList, collectErrors...)
	index.ParseErrorList = append(index.ParseErrorList, perrs...)
	if session.TurnCount > 0 || len(raw.RawEventList) > 0 {
		index.Sessions = append(index.Sessions, session)
		index.Turns = append(index.Turns, turns...)
		for _, turn := range turns {
			index.Invocations = append(index.Invocations, turn.Invocations...)
			index.TurnComponents = append(index.TurnComponents, turn.Components...)
		}
	}
	collector.FinalizeCounts(&index)
	if err := policy.ApplyToIndex(ctx, &index); err != nil {
		return Result{}, err
	}
	trace.InternIndexStrings(&index)
	return Result{Index: index, RawEventList: raw.RawEventList, ProcessedFiles: []string{path}, Fingerprints: fingerprints}, nil
}

func (d Driver[F]) fileSkipError(rootPath, filePath string, err error) trace.ParseError {
	sourceID := trace.SourceID(d.Spec.Source)
	return trace.ParseError{
		SourceID:     sourceID,
		SourceRootID: trace.SourceRootID(sourceID, rootPath),
		SourceFile:   filePath,
		Message:      err.Error(),
	}
}
