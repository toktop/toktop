package ingest

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
)

type Provider interface {
	Name() string

	// Aliases lists alternate user-facing names that fold to Name() (e.g.
	// claude-code accepts "claude"/"claudecode"). Provider-name knowledge stays
	// on the provider, so NormalizeName needs no hardcoded per-provider branch.
	// Return nil when the canonical name is the only accepted form.
	Aliases() []string

	// WatchSubdir is the subdirectory under each discovery root where this
	// provider's transcripts live (claude-code: "projects", codex: "sessions").
	// The fsnotify watcher and `toktop doctor` watch this subdir, not the bare
	// root; "" means watch the root itself. Keeping it on the provider is what
	// lets a new provider be added without editing the watcher/diagnostics.
	WatchSubdir() string

	// TranscriptExt is the file extension of this provider's transcript files
	// (e.g. ".jsonl"). The fsnotify watcher pre-filters events by it so that
	// non-transcript writes (lock/temp/editor/.DS_Store files) never enter the
	// ingest pipeline. Like WatchSubdir, keeping the format fact on the provider
	// is what lets a provider with a different transcript extension be added
	// without editing the neutral watcher.
	TranscriptExt() string

	// ResolveRoots resolves discovery roots across the precedence chain
	// explicit > env > config-file > default, exposing each root's origin Kind.
	// explicit is caller-supplied override roots (already-resolved roots a
	// caller passes back in — no CLI flag feeds it); file is this provider's
	// config.json roots.
	ResolveRoots(explicit, file []string) []SourceRoot

	Ingest(ctx context.Context, roots []string, policy redact.Policy, known map[string]source.Fingerprint, sink BatchSink) (Summary, error)

	IngestFile(ctx context.Context, roots []string, policy redact.Policy, path string) (Result, bool, error)
}

// HookSentinel marks a hook entry as toktop-installed. It is embedded both as a
// top-level key (claude-code entries) and in the curl command's query string
// (?toktop_observer=...), so install/uninstall/status can recognize toktop's own
// entries regardless of provider entry schema.
const HookSentinel = "__toktop_observer__"

// HookInstaller is the optional hook-install seam: a Provider that also
// implements it can be targeted by `toktop hooks install/uninstall/status`. The
// CLI owns the generic settings-file read/upsert/write flow and the shared curl
// command builder; the provider owns its config-file location, event list, the
// per-event entry schema (matcher + provider-specific fields), and the mapping
// from its own hook event names to live-status values. A provider that does not
// emit hooks simply does not implement this interface.
type HookInstaller interface {
	// HookConfigPath returns the settings file path and a human label
	// ("settings" | "hooks") for the given scope ("user" | "project").
	HookConfigPath(scope string) (path string, label string, err error)
	// HookEvents is the ordered list of hook event names to install.
	HookEvents() []string
	// HookEntry builds the settings entry for one event. command is the
	// already-built, provider-agnostic curl invocation the CLI owns (it carries
	// the HookSentinel in its query string); the provider wraps it in its matcher
	// and schema.
	HookEntry(event, command string) map[string]any
	// HookEventStatus maps a provider-specific hook event name to a trace.Status*
	// value. ok=false means "no provider opinion; use the generic heuristic".
	HookEventStatus(eventName string) (status string, ok bool)
}

// HookInstallNoter is an optional companion to HookInstaller: a provider that
// must tell the user something after a successful install — e.g. a manual trust
// step the provider requires before the hook will actually run — implements it.
// The CLI prints the note verbatim after writing the hook file; an empty string
// prints nothing. Kept separate from HookInstaller so providers with no such
// step (claude-code, whose hooks run as soon as they are written) need not
// implement it.
type HookInstallNoter interface {
	HookInstallNote() string
}

// HookInstallerFor returns the HookInstaller for name when the provider supports
// hooks.
func HookInstallerFor(name string) (HookInstaller, bool) {
	p, ok := registry[name]
	if !ok {
		return nil, false
	}
	hi, ok := p.(HookInstaller)
	return hi, ok
}

// PluginInstaller is the optional realtime-install seam for a provider whose live
// status is delivered by a host PLUGIN rather than config-level shell hooks
// (opencode). It has no per-event settings entries and no embedded curl command —
// the producer is the plugin itself, which POSTs to /v1/hooks:intake — so it
// cannot reuse HookInstaller. The provider owns the plugin asset (endpoint/token
// baked in at install time) and where it is written; a provider that uses shell
// hooks implements HookInstaller instead. Both are accepted by `toktop hooks
// install/uninstall/status`.
type PluginInstaller interface {
	// PluginConfigPath returns the plugin asset file path toktop manages and a
	// human label, for scope ("user" | "project").
	PluginConfigPath(scope string) (path string, label string, err error)
	// InstallPlugin writes the plugin asset, baking in the resolved intake
	// endpoint and (for a TCP endpoint) bearer token. dryRun returns the planned
	// write without touching disk. Returns a human-facing summary.
	InstallPlugin(scope, endpoint, token string, dryRun bool) (summary string, err error)
	// UninstallPlugin removes the plugin asset. Returns a human-facing summary.
	UninstallPlugin(scope string, dryRun bool) (summary string, err error)
	// PluginEventStatus maps a provider realtime event name to a trace.Status*
	// value. ok=false means "no provider opinion; use the generic heuristic".
	PluginEventStatus(eventName string) (status string, ok bool)
}

// PluginInstallerFor returns the PluginInstaller for name when the provider
// delivers realtime status via a plugin.
func PluginInstallerFor(name string) (PluginInstaller, bool) {
	p, ok := registry[name]
	if !ok {
		return nil, false
	}
	pi, ok := p.(PluginInstaller)
	return pi, ok
}

// EventStatusFor resolves a realtime event name to a trace.Status* value via
// whichever install seam the provider implements — HookInstaller for shell-hook
// providers, PluginInstaller for plugin providers. It is the single status-mapping
// lookup the live broker calls, so a new realtime seam needs no special-case at
// the use site. ok=false means no provider opinion (use the generic heuristic).
func EventStatusFor(provider, eventName string) (status string, ok bool) {
	if hi, ok := HookInstallerFor(provider); ok {
		if st, mapped := hi.HookEventStatus(eventName); mapped {
			return st, true
		}
	}
	if pi, ok := PluginInstallerFor(provider); ok {
		if st, mapped := pi.PluginEventStatus(eventName); mapped {
			return st, true
		}
	}
	return "", false
}

// LivenessChecker is the optional seam letting a Provider decide whether a
// previously-ingested source_file still exists, used by purgeVanished to drop
// rows for a vanished source. Not implemented ⇒ the default os.Stat(file) check,
// which every file-backed provider relies on. A DB-backed provider whose
// source_file is a SYNTHETIC key (opencode's "opencode://<session-id>", never a
// real path) MUST implement it — else os.Stat always returns ErrNotExist and
// every row is purged on each reconcile. roots are the discovery roots ingest
// used, so the check resolves against the same store(s) (a custom-configured
// opencode root must not be re-resolved to the default).
type LivenessChecker interface {
	SourceFileExists(roots []string, file string) bool
}

// livenessFor returns the LivenessChecker for name when the provider supplies one.
func livenessFor(name string) (LivenessChecker, bool) {
	p, ok := registry[name]
	if !ok {
		return nil, false
	}
	lc, ok := p.(LivenessChecker)
	return lc, ok
}

// AgentToolDeclarer is the optional seam by which a Provider declares the
// built-in tool names that spawn a subagent / multi-agent workflow whose run the
// handoff reconstructs and the workflow_interrupted rule counts (claude-code:
// Task, Agent, Workflow). A single-agent provider (e.g. codex) has none and
// simply does not implement it — keeping the per-provider tool-name knowledge on
// the provider instead of hardcoded in the neutral downstream layers.
type AgentToolDeclarer interface {
	AgentToolNames() []string
}

// IsAgentTool reports whether tool name, for the named provider, spawns a
// subagent whose run downstream (handoff, the workflow_interrupted rule) treats
// as an agent run. False for an unknown provider or one declaring no agent tools.
// Requires the provider to be registered — true on every real CLI/daemon path,
// where cmd/toktop/main.go imports the built-in providers.
func IsAgentTool(provider, name string) bool {
	p, ok := registry[provider]
	if !ok {
		return false
	}
	d, ok := p.(AgentToolDeclarer)
	return ok && slices.Contains(d.AgentToolNames(), name)
}

// AgentRunProjector is the optional seam by which a Provider maps one of its
// agent-spawning tools' input_json into the neutral fields the handoff
// reconstructs: the run's type/subagent label, its description, and its prompt.
// Co-located with AgentToolNames so a provider owns ALL its agent-tool knowledge
// (the tool names AND the JSON shape of their inputs); the neutral handoff layer
// asks the provider rather than hardcoding any one provider's input keys.
type AgentRunProjector interface {
	AgentRunInput(toolName string, inputJSON []byte) (typ, description, prompt string)
}

// AgentRunInput projects an agent-tool call's input_json into neutral handoff
// fields via the named provider's AgentRunProjector. Returns empty strings for an
// unknown provider, one not implementing the seam, or input it cannot map.
func AgentRunInput(provider, toolName string, inputJSON []byte) (typ, description, prompt string) {
	p, ok := registry[provider]
	if !ok {
		return "", "", ""
	}
	pr, ok := p.(AgentRunProjector)
	if !ok {
		return "", "", ""
	}
	return pr.AgentRunInput(toolName, inputJSON)
}

// AgentSpawnResolver is the optional seam by which a Provider extracts, from an
// agent-spawning tool call's OUTPUT, the external id of the subagent it launched —
// when that link lives in the spawn result rather than on the child (codex's
// spawn_agent returns {"agent_id": …}; claude-code's Task instead records the
// launching tool_use on the child's own .meta.json, so it does not implement this).
// It lets the handoff dedup a spawn run against the linked subagent session.
type AgentSpawnResolver interface {
	AgentSpawnChildID(toolName string, outputJSON []byte) string
}

// AgentSpawnChildID returns the external id of the subagent a spawn tool call
// launched, via the named provider's AgentSpawnResolver. Empty for an unknown
// provider, one not implementing the seam, or output it cannot map.
func AgentSpawnChildID(provider, toolName string, outputJSON []byte) string {
	p, ok := registry[provider]
	if !ok {
		return ""
	}
	r, ok := p.(AgentSpawnResolver)
	if !ok {
		return ""
	}
	return r.AgentSpawnChildID(toolName, outputJSON)
}

// SourceRoot is a resolved discovery root and where it came from.
type SourceRoot struct {
	Path string
	Kind string // "manual" | "env" | "file" | "default"
}

type BatchSink func(ctx context.Context, batch Result) error

type Summary struct {
	Source          string
	Files           int
	SessionCount    int
	TurnCount       int
	InvocationCount int
	ToolCallCount   int
	RawEventCount   int
	ParseErrorCount int

	Fingerprints map[string]source.Fingerprint
}

var registry = map[string]Provider{}

func Register(p Provider) {
	registry[p.Name()] = p
}

func ProviderFor(name string) (Provider, bool) {
	p, ok := registry[name]
	return p, ok
}

// TranscriptExt returns the transcript file extension for name, or "" when the
// provider is unknown. The fsnotify watcher uses it to pre-filter events without
// hardcoding any provider's transcript format.
func TranscriptExt(name string) string {
	if p, ok := registry[name]; ok {
		return p.TranscriptExt()
	}
	return ""
}

// SortedProviders returns every registered provider name, sorted.
func SortedProviders() []string {
	names := slices.Collect(maps.Keys(registry))
	slices.Sort(names)
	return names
}

// HasProvider reports whether name is a registered provider.
func HasProvider(name string) bool {
	_, ok := registry[name]
	return ok
}

// NormalizeName folds a user-supplied provider token to its canonical
// registered name via each provider's declared aliases (case/space-insensitive).
// Unknown input is returned trimmed so callers can validate and report it
// verbatim. This is the single alias-folding implementation; do not re-add a
// per-call switch over "claude"/"codex" literals anywhere else.
func NormalizeName(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	// Iterate in sorted order, not map order, so that if two providers ever
	// declare the same alias the fold is deterministic instead of random per run.
	for _, canon := range SortedProviders() {
		if key == strings.ToLower(canon) {
			return canon
		}
		for _, a := range registry[canon].Aliases() {
			if key == strings.ToLower(strings.TrimSpace(a)) {
				return canon
			}
		}
	}
	return strings.TrimSpace(name)
}

// PresentProviders returns the registered providers whose discovery roots exist
// on disk (sorted) — the "auto-detect" set used by ingest/daemon/doctor when no
// explicit --sources is given. rootsByProvider supplies already-resolved roots
// (e.g. config.Snapshot.Roots); a provider missing from it falls back to
// flag-less discovery. This is the only implementation of source auto-detect.
func PresentProviders(rootsByProvider map[string][]string) []string {
	var present []string
	for _, name := range SortedProviders() {
		roots := rootsByProvider[name]
		if len(roots) == 0 {
			roots = DiscoverRootPaths(name, nil)
		}
		if slices.ContainsFunc(roots, fsx.DirExists) {
			present = append(present, name)
		}
	}
	return present
}

// ResolveRoots resolves a named provider's roots; returns nil for unknown
// providers.
func ResolveRoots(name string, explicit, file []string) []SourceRoot {
	if p, ok := registry[name]; ok {
		return p.ResolveRoots(explicit, file)
	}
	return nil
}

// UniqueSourceRoots cleans and dedups discovery root paths by cleaned path,
// labeling each with kind ("manual" | "env" | "file" | "default"). Shared by
// every provider's ResolveRoots so the clean/dedup rule has one definition
// instead of byte-identical per-provider copies.
func UniqueSourceRoots(values []string, kind string) []SourceRoot {
	seen := make(map[string]struct{}, len(values))
	roots := make([]SourceRoot, 0, len(values))
	for _, value := range values {
		path := filepath.Clean(strings.TrimSpace(value))
		if path == "." || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		roots = append(roots, SourceRoot{Path: path, Kind: kind})
	}
	return roots
}

// RootPaths extracts the paths from resolved roots.
func RootPaths(roots []SourceRoot) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.Path
	}
	return out
}

// DiscoverRootPaths returns resolved root paths for name given only
// caller-supplied explicit roots, no config-file layer. Kept for callers
// without config.
func DiscoverRootPaths(name string, roots []string) []string {
	if !HasProvider(name) {
		return roots
	}
	return RootPaths(ResolveRoots(name, roots, nil))
}
