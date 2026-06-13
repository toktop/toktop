package ingest

import (
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

type Options struct {
	Source string

	Roots []string

	Policy redact.Policy
}

type Result struct {
	Index          trace.Index
	RawEventList   []source.RawEvent
	ProcessedFiles []string
	Fingerprints   map[string]source.Fingerprint

	// AuthoritativeSkills / AuthoritativeMCPServers report whether THIS metadata
	// round completely (authoritatively) scanned that kind. The store reconciles
	// (delete-stale) a kind only when its flag is set, so a partial/failed scan of
	// one kind never deletes rows and one kind's scan failure does not suppress the
	// other kind's reconcile. Session/file batches leave both false (they carry no
	// metadata).
	AuthoritativeSkills     bool
	AuthoritativeMCPServers bool
}
