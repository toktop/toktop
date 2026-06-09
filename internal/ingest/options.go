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
}
