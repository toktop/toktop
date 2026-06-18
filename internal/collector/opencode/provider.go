package opencode

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
)

type provider struct{}

func init() { ingest.Register(provider{}) }

func (provider) Name() string { return "opencode" }

func (provider) Aliases() []string { return []string{"oc"} }

func (provider) WatchSubdir() string { return "" } // single DB lives at the data-dir root

// TranscriptExt is opencode's WAL sidecar. opencode commits land in
// opencode.db-wal (WAL mode), so a Write there is the cheapest low-latency signal
// that something changed; it is only a coarse hint that triggers a full reconcile
// (which seq-skips unchanged sessions). The authoritative refresh is the periodic
// reconcile, and live STATUS comes from the opencode plugin, not this file watch.
func (provider) TranscriptExt() string { return ".db-wal" }

func (provider) ResolveRoots(explicit, file []string) []ingest.SourceRoot {
	return resolveRoots(explicit, file)
}

func (provider) Ingest(ctx context.Context, roots []string, policy redact.Policy, known map[string]source.Fingerprint, sink ingest.BatchSink) (ingest.Summary, error) {
	return Ingest(ctx, DiscoverRoots(roots), policy, known, sink)
}

func (provider) IngestFile(ctx context.Context, roots []string, policy redact.Policy, path string) (ingest.Result, bool, error) {
	sourceRoots := DiscoverRoots(roots)
	file, ok := SessionFileFromPath(path, sourceRoots)
	if !ok {
		return ingest.Result{}, false, nil
	}
	res, err := driver.IngestSessionFile(ctx, sourceRoots, file, policy)
	if err != nil {
		return ingest.Result{}, true, err
	}
	return res, true, nil
}

// SourceFileExists satisfies ingest.LivenessChecker: opencode's source_file is the
// synthetic "opencode://<id>" key, which os.Stat cannot validate. A session is
// gone iff its id no longer resolves in any opencode.db under the roots ingest
// used (resolved the same way, so a custom-configured root isn't lost). An
// unrecognized key shape is treated as still-present (conservative: never purge a
// row we cannot interpret).
func (provider) SourceFileExists(roots []string, file string) bool {
	id, ok := strings.CutPrefix(file, sourceFileScheme)
	if !ok || id == "" {
		return true
	}
	for _, root := range DiscoverRoots(roots) {
		dbPath := filepath.Join(root.Path, dbFileName)
		if !fsx.FileExists(dbPath) {
			continue
		}
		db, err := openReadOnly(dbPath)
		if err != nil {
			// Can't open the DB to confirm: assume present rather than purge on a
			// transient open error.
			return true
		}
		exists, err := sessionExists(context.Background(), db, id)
		_ = db.Close()
		if err != nil {
			return true
		}
		if exists {
			return true
		}
	}
	return false
}

// agentToolNames is opencode's built-in subagent-spawning tool. `task` starts a
// subagent in its own child session; consumed via ingest.IsAgentTool for handoff
// and the workflow_interrupted rule.
var agentToolNames = []string{"task"}

func (provider) AgentToolNames() []string { return agentToolNames }

// taskInput is the shape of opencode `task` tool input (ToolCall.Input).
type taskInput struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
}

// AgentRunInput satisfies ingest.AgentRunProjector: project a task call's input
// into the neutral (type, description, prompt) the handoff reconstructs.
func (provider) AgentRunInput(_ string, inputJSON []byte) (typ, description, prompt string) {
	var in taskInput
	_ = json.Unmarshal(inputJSON, &in)
	return in.SubagentType, in.Description, in.Prompt
}

// taskOutputID matches the spawned child session id in a task tool's output,
// which opencode emits as `<task id="ses_..." state="...">…`.
var taskOutputID = regexp.MustCompile(`<task\s+id="([^"]+)"`)

// AgentSpawnChildID satisfies ingest.AgentSpawnResolver: the spawned subagent's
// session id is in the task output's <task id="…"> wrapper, so the handoff folds
// the spawn call and the linked subagent session into one run.
func (provider) AgentSpawnChildID(_ string, outputJSON []byte) string {
	m := taskOutputID.FindSubmatch(outputJSON)
	if m == nil {
		return ""
	}
	return string(m[1])
}
