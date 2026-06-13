package handoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		{"evidence-index.md", func(path string) error { return os.WriteFile(path, []byte(p.evidenceIndexMD()), 0o644) }},
		{"codex-prompt.md", func(path string) error { return os.WriteFile(path, []byte(p.codexPromptMD()), 0o644) }},
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

func (p Package) evidenceIndexMD() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence index\n\nSession `%s` (%s)", p.Session.ID, p.Manifest.Provider)
	if p.Manifest.Project != "" {
		fmt.Fprintf(&b, " · project `%s`", p.Manifest.Project)
	}
	fmt.Fprintf(&b, "\n\nWorkflow status: **%s** · %d turns · %d agent runs (%d ok, %d failed) · final synthesis: %v\n\n",
		p.Manifest.WorkflowStatus, p.Manifest.Turns, p.Manifest.AgentRuns,
		p.Manifest.CompletedAgentRuns, p.Manifest.FailedAgentRuns, p.Manifest.FinalSynthesisPresent)
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

func (p Package) codexPromptMD() string {
	var b strings.Builder
	b.WriteString("# Handoff prompt for the receiving agent\n\n")
	intro := "recorded for reference"
	if p.Manifest.WorkflowStatus != "completed" {
		intro = "recorded but not finished"
	}
	fmt.Fprintf(&b, "You are picking up a %s workflow %s. Workflow status: **%s**.\n\n",
		p.Manifest.Provider, intro, p.Manifest.WorkflowStatus)
	b.WriteString("## Hard rules\n")
	b.WriteString("1. Do NOT re-run the agents below — their results are already captured in `agent-results.ndjson`.\n")
	b.WriteString("2. Do NOT re-plan from scratch or guess from the current git diff.\n")
	b.WriteString("3. Work only from `evidence`-tagged facts. Treat `inference` as a hint and `unknown` as not established.\n")
	b.WriteString("4. Every claim you rely on must trace to a source pointer (`raw-pointers.ndjson`); re-read the transcript if unsure.\n")
	b.WriteString("5. Do not modify code until the human confirms.\n\n")
	b.WriteString("## Start here\n")
	for i, e := range p.Manifest.RecommendedEntrypoints {
		fmt.Fprintf(&b, "%d. `%s`\n", i+1, e)
	}
	b.WriteString("\n## What is left\n")
	if !p.Manifest.FinalSynthesisPresent {
		b.WriteString("- The final synthesis/answer is missing. The agent runs completed; your job is to collect their results and produce the wrap-up the original session never emitted.\n")
	} else {
		b.WriteString("- A final assistant message exists (see `final_answer` in the evidence index). Verify it against the agent results before continuing.\n")
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
	b.WriteString("| `codex-prompt.md` | constraints for the agent picking up the work |\n")
	b.WriteString("| `evidence-index.md` | human-readable facts with provenance + confidence |\n")
	b.WriteString("| `agent-results.ndjson` | one reconstructed agent run per line |\n")
	b.WriteString("| `turns.json` | the session's turns (with tool calls) |\n")
	b.WriteString("| `raw-pointers.ndjson` | transcript file + raw-event pointers for re-reading |\n")
	return b.String()
}
