package codex

import (
	"context"
	"encoding/json"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
)

type provider struct{}

func init() { ingest.Register(provider{}) }

func (provider) Name() string { return "codex" }

func (provider) Aliases() []string { return nil }

// agentToolNames is codex's built-in subagent-spawning tool (multi_agent_v1
// namespace). spawn_agent starts an agent and returns its agent_id; wait_agent /
// close_agent are orchestration ops, not runs, so only the spawner is declared —
// mirroring claude-code's Task/Agent/Workflow. Consumed via ingest.IsAgentTool.
var agentToolNames = []string{"spawn_agent"}

// AgentToolNames satisfies ingest.AgentToolDeclarer.
func (provider) AgentToolNames() []string { return agentToolNames }

// spawnAgentInput is the shape of codex spawn_agent's input_json.
type spawnAgentInput struct {
	AgentType string `json:"agent_type"`
	Message   string `json:"message"`
}

// AgentRunInput satisfies ingest.AgentRunProjector: project a spawn_agent call's
// input into the neutral (type, description, prompt) the handoff reconstructs.
// codex has no separate description; the role is the type and message is the prompt.
func (provider) AgentRunInput(_ string, inputJSON []byte) (typ, description, prompt string) {
	var in spawnAgentInput
	_ = json.Unmarshal(inputJSON, &in)
	return in.AgentType, "", in.Message
}

// AgentSpawnChildID satisfies ingest.AgentSpawnResolver: spawn_agent's output is
// {"agent_id":"<thread id>","nickname":"…"} where agent_id is the spawned agent's
// session/external id — the key that links this spawn call to the subagent session,
// so the handoff folds them into one run instead of double-counting.
func (provider) AgentSpawnChildID(_ string, outputJSON []byte) string {
	var out struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(outputJSON, &out)
	return out.AgentID
}

func (provider) WatchSubdir() string { return "sessions" }

func (provider) TranscriptExt() string { return ".jsonl" }

func (provider) ResolveRoots(explicit, file []string) []ingest.SourceRoot {
	// resolveRoots returns []SourceRoot, an alias of []ingest.SourceRoot (see
	// discover.go), so no conversion is needed.
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
