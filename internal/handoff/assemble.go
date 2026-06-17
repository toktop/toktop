package handoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Write materializes the handoff package into dir, creating it if needed. The
// layout is the stable toktop.handoff.v1 contract: a manifest entry point, the
// machine-readable agent results / turns / raw pointers, and the human/agent
// readable index + receiving-agent prompt.
func (p Package) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create handoff dir: %w", err)
	}
	writers := []struct {
		name  string
		write func(string) error
	}{
		{"manifest.json", func(path string) error { return writeJSONFile(path, p.Manifest) }},
		{"turns.json", func(path string) error { return writeJSONFile(path, p.Turns) }},
		{"agent-results.ndjson", func(path string) error { return writeNDJSON(path, p.AgentRuns) }},
		{"raw-pointers.ndjson", func(path string) error { return writeNDJSON(path, p.rawPointers()) }},
		{"digest.md", func(path string) error { return os.WriteFile(path, []byte(p.Digest), 0o644) }},
		{"evidence-index.md", func(path string) error { return os.WriteFile(path, []byte(p.evidenceIndexMD()), 0o644) }},
		{"receiver-prompt.md", func(path string) error { return os.WriteFile(path, []byte(p.receiverPromptMD()), 0o644) }},
		{"README.md", func(path string) error { return os.WriteFile(path, []byte(p.readmeMD()), 0o644) }},
	}
	for _, w := range writers {
		if err := w.write(filepath.Join(dir, w.name)); err != nil {
			return fmt.Errorf("write %s: %w", w.name, err)
		}
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func writeNDJSON[T any](path string, items []T) error {
	var b strings.Builder
	for _, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func (p Package) rawPointers() []SourcePointer {
	out := make([]SourcePointer, 0, len(p.Evidence))
	for _, e := range p.Evidence {
		out = append(out, e.Source)
	}
	return out
}

// digestPerMsgCap bounds each user/assistant message inlined into the digest, so a
// single pathological message cannot bloat the lean read path. Independent of
// maxOutputBytes (which only clips tool-call bodies in turns.json).
const digestPerMsgCap = 2000

// digestMD renders the lean narrative — user→assistant per turn, with NO tool-call /
// invocation / component bodies. This is the cheap default read path: enough to
// orient without ingesting the fat turns.json, which stays the deep-dive artifact.
// Each message is flattened to one line (via oneLine) so an embedded newline or a
// stray `### ` line in the message body cannot break the per-turn `### Turn N`
// structure or split the bold label.
func (p Package) digestMD() string {
	var b strings.Builder
	b.WriteString("# Session digest\n\n")
	fmt.Fprintf(&b, "Session `%s` (%s)", p.Session.ID, p.Manifest.Provider)
	if p.Manifest.Title != "" {
		fmt.Fprintf(&b, " · %s", oneLine(p.Manifest.Title, 120))
	}
	if p.Manifest.Project != "" {
		fmt.Fprintf(&b, " · project `%s`", p.Manifest.Project)
	}
	fmt.Fprintf(&b, "\n\nWorkflow status: **%s** · %d turns · %d agent runs\n\n",
		p.Manifest.WorkflowStatus, p.Manifest.Turns, p.Manifest.AgentRuns)
	b.WriteString("Lean narrative (user → assistant per turn, no tool calls). For a specific tool call's bytes, see `turns.json`.\n")
	if len(p.Turns) == 0 {
		b.WriteString("\n_No turns in this session._\n")
		return b.String()
	}
	for i := range p.Turns {
		t := &p.Turns[i]
		fmt.Fprintf(&b, "\n### Turn %d (%s)\n", i+1, t.Status)
		user := strings.TrimSpace(t.UserMessage)
		asst := strings.TrimSpace(t.AssistantFinal)
		if user != "" {
			fmt.Fprintf(&b, "**User:** %s\n", oneLine(user, digestPerMsgCap))
		}
		if asst != "" {
			fmt.Fprintf(&b, "**Assistant:** %s\n", oneLine(asst, digestPerMsgCap))
		}
		if user == "" && asst == "" {
			if t.ToolCallCount > 0 {
				fmt.Fprintf(&b, "_(no user/assistant text; %d tool call(s) — see turns.json)_\n", t.ToolCallCount)
			} else {
				b.WriteString("_(empty turn — no user/assistant text or tool calls)_\n")
			}
		}
	}
	return b.String()
}

func (p Package) evidenceIndexMD() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence index\n\nSession `%s` (%s)", p.Session.ID, p.Manifest.Provider)
	if p.Manifest.Project != "" {
		fmt.Fprintf(&b, " · project `%s`", p.Manifest.Project)
	}
	fmt.Fprintf(&b, "\n\nWorkflow status: **%s** · %d turns · %d agent runs (%d ok, %d failed, %d stopped, %d in-flight)\n\n",
		p.Manifest.WorkflowStatus, p.Manifest.Turns, p.Manifest.AgentRuns,
		p.Manifest.CompletedAgentRuns, p.Manifest.FailedAgentRuns, p.Manifest.InterruptedAgentRuns, p.Manifest.IncompleteAgentRuns)
	fmt.Fprintf(&b, "Each item is tagged `evidence` (proven by the transcript), `inference` (derived), or `unknown`.\nProvenance points to the original transcript so you can re-read raw bytes; do not trust a claim you cannot trace.\n\n")
	if len(p.Evidence) == 0 {
		b.WriteString("_No evidence items extracted._\n")
		return b.String()
	}
	for _, e := range p.Evidence {
		fmt.Fprintf(&b, "- **[%s]** (`%s`) %s\n", e.Confidence, e.Type, e.Claim)
		fmt.Fprintf(&b, "  - source: turn `%s`", e.Source.TurnID)
		if e.Source.ToolCallID != "" {
			fmt.Fprintf(&b, " · tool_call `%s`", e.Source.ToolCallID)
		}
		if e.Source.File != "" {
			fmt.Fprintf(&b, " · file `%s`", e.Source.File)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// receiverPromptMD is the provider-neutral instructions for whatever agent picks
// up this package (codex, claude-code, or other). It names no provider-specific
// tool (no Workflow/TaskStop/resume-recipe): cross-agent recovery reuses the
// captured sub-agent RESULTS, not a literal resume. Agent-centric guidance is
// gated on there being agent runs, so a single-agent source emits no inert rules.
func (p Package) receiverPromptMD() string {
	var b strings.Builder
	b.WriteString("# Handoff prompt for the receiving agent\n\n")
	intro := "recorded for reference"
	if p.Manifest.WorkflowStatus != "completed" {
		intro = "recorded but not finished"
	}
	fmt.Fprintf(&b, "You are picking up a %s session %s. Status: **%s**.\n\n",
		p.Manifest.Provider, intro, p.Manifest.WorkflowStatus)
	hasAgents := p.Manifest.AgentRuns > 0
	if hasAgents {
		fmt.Fprintf(&b, "This is a multi-agent recovery: %d run(s) — %d completed, %d with no captured result, %d stopped, %d failed. Reuse the captured results; do not redo that work.\n\n",
			p.Manifest.AgentRuns, p.Manifest.CompletedAgentRuns, p.Manifest.IncompleteAgentRuns, p.Manifest.InterruptedAgentRuns, p.Manifest.FailedAgentRuns)
	} else {
		b.WriteString("This is a plain session digest — no sub-agents ran, so there is no parallel work to recover.\n\n")
	}

	b.WriteString("## Hard rules\n")
	rule := 0
	add := func(s string) { rule++; fmt.Fprintf(&b, "%d. %s\n", rule, s) }
	if hasAgents {
		add("Do NOT redo a sub-agent whose result is captured (in `agent-results.ndjson`, each with a `source` pointing at that sub-agent's own transcript you can re-read). For a run with no captured result, redo the work from its prompt/description or its captured transcript (the run's `source`) — do not trust a blank or partial output.")
	} else {
		add("This session ran no sub-agents — there is no parallel work to reuse. Read `digest.md` to see where it ended, then continue.")
	}
	add("Do NOT re-plan from scratch or guess from the current git diff.")
	add("Work only from `evidence`-tagged facts. Treat `inference` as a hint and `unknown` as not established.")
	add("Every claim you rely on must trace to a source pointer (`raw-pointers.ndjson`); re-read the transcript if unsure.")
	add("Do not modify code until the human confirms.")

	b.WriteString("\n## Start here\n")
	for i, e := range p.Manifest.RecommendedEntrypoints {
		fmt.Fprintf(&b, "%d. `%s`\n", i+1, e)
	}
	b.WriteString("Read `digest.md` for the narrative; open `turns.json` only for a specific tool call's bytes.\n")

	b.WriteString("\n## What is left\n")
	if slices.ContainsFunc(p.Evidence, func(e EvidenceItem) bool { return e.Type == typeLastAssistantMessage }) {
		b.WriteString("- The session's last assistant message is captured (`last_assistant_message` in the evidence index). Judge it against `digest.md` IN CONTEXT — it may be the conclusion or where the session was cut off; do NOT anchor on it as the answer.\n")
	} else {
		b.WriteString("- The session produced no assistant message (interrupted before any wrap-up). Re-derive what's needed from `digest.md` and the captured results.\n")
	}
	if hasAgents {
		if n := p.Manifest.CompletedAgentRuns; n > 0 {
			fmt.Fprintf(&b, "- %d sub-agent run(s) completed; their results are captured in `agent-results.ndjson` (each `source` points at that sub-agent's transcript). **Reuse them — do not redo this work.**\n", n)
		}
		if n := p.Manifest.IncompleteAgentRuns; n > 0 {
			fmt.Fprintf(&b, "- %d run(s) have no captured result (launched, never finished). Redo that work from the run's prompt/description or its captured transcript (`source`), or reconcile against `digest.md`.\n", n)
		}
		if n := p.Manifest.InterruptedAgentRuns; n > 0 {
			fmt.Fprintf(&b, "- %d run(s) were deliberately stopped; any output is partial and they were ended on purpose — reconcile rather than blindly redo.\n", n)
		}
		if n := p.Manifest.FailedAgentRuns; n > 0 {
			fmt.Fprintf(&b, "- %d run(s) failed; treat their output as unreliable and redo if needed.\n", n)
		}
	}
	return b.String()
}

func (p Package) readmeMD() string {
	var b strings.Builder
	b.WriteString("# toktop handoff package\n\n")
	fmt.Fprintf(&b, "Generated `%s` · schema `%s`.\n\n", p.Manifest.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"), p.Manifest.Schema)
	b.WriteString("Read-only, auditable snapshot of one session's workflow for cross-agent recovery.\n\n")
	b.WriteString("| file | contents |\n|---|---|\n")
	b.WriteString("| `manifest.json` | entry point: status, counts, recommended reading order |\n")
	b.WriteString("| `digest.md` | lean narrative (user→assistant per turn, no tool calls) — read this to orient |\n")
	b.WriteString("| `receiver-prompt.md` | constraints for the agent picking up the work |\n")
	b.WriteString("| `evidence-index.md` | human-readable facts with provenance + confidence |\n")
	b.WriteString("| `agent-results.ndjson` | one reconstructed agent run per line |\n")
	b.WriteString("| `turns.json` | deep-dive / provenance: full turns WITH tool calls — not needed to orient |\n")
	b.WriteString("| `raw-pointers.ndjson` | transcript file + raw-event pointers for re-reading |\n")
	return b.String()
}
