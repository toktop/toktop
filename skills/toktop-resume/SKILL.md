---
name: toktop-resume
description: Resume an interrupted agent session by loading a toktop handoff package and continuing under its rules — reuses work the prior session already completed (sub-agent analyses, drafted plans, the final answer) instead of redoing it. Invoke ONLY when the user explicitly runs /toktop-resume (optionally with a session id). Do not trigger this automatically.
---

# Resume an interrupted session via toktop handoff

You are the *receiving* agent. Goal: reuse what the prior session already produced —
do not re-plan or redo finished work. Works identically in Claude Code and Codex; it
uses only the `toktop` CLI plus reading files from the handoff package.

## Steps

1. **Refresh the index.** Run `toktop ingest` (idempotent; only re-reads changed
   transcripts) so the target session's latest state is indexed. Skip if a toktop
   daemon is already running.

2. **Identify the session.**
   - If the user gave a session id, use it.
   - Otherwise run `toktop sessions --since 24h` (narrow with
     `--sources claude-code,codex` or `--project <name>`) and show the recent
     sessions. Each row has ID · PROJECT · STARTED · TURNS · SUBAGENTS. Prefer rows
     with `SUBAGENTS > 0` — those ran a workflow / sub-agents and have the most to
     recover. Ask which one to resume, and use the value in the **ID** column.

3. **Build the package.** `toktop handoff create --session <id>` writes to
   `.ai/handoff/toktop/`. Read the printed `status=` line: `interrupted_after_agents_completed`
   or `interrupted_agents_in_flight` mean there is recoverable work; `completed`
   means a final answer already exists. For a large session add
   `--max-output-bytes 4000` to cap inlined results (the raw pointers still reach
   the full bytes).

4. **Load the package, in this order:** `manifest.json` → `receiver-prompt.md` →
   `evidence-index.md` → `agent-results.ndjson`. The hard rules in
   `receiver-prompt.md` are binding.

5. **Continue under those rules.**
   - Reuse every captured sub-agent result in `agent-results.ndjson` (each has a
     `source` pointing at that sub-agent's transcript you can re-read). **Do not redo
     completed work.**
   - For runs with no captured result (incomplete / failed), redo from the run's
     prompt/description or its `source` transcript — never trust a blank or partial
     output.
   - Rely only on `evidence`-tagged facts; trace any claim through
     `raw-pointers.ndjson` before trusting it.
   - First **summarize for the user**: where the work ended · what's reusable ·
     what's left. Then propose the next step and **wait for the user's confirmation
     before modifying any code.**

Invoke explicitly: `/toktop-resume` (optionally a session id). This skill is not
meant to trigger automatically.
