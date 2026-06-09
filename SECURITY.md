# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately via GitHub's **Report a vulnerability** button under the
repository's **Security** tab (Security → Advisories → *Report a vulnerability*).
This opens a private advisory visible only to you and the maintainers.

Please include enough to reproduce: affected version/commit, configuration
(`toktop config get` is helpful), and the steps or input that trigger the issue.
We aim to acknowledge within a few days and will coordinate a fix and disclosure
timeline with you.

## Supported versions

Toktop is **pre-1.0** with no stability guarantees yet. Security fixes land on
`main`; please reproduce against the latest `main` (or the latest release) before
reporting.

## Security model

Toktop reads the local JSONL transcripts that Claude Code and Codex write, so it
handles potentially sensitive content (prompts, tool I/O, file contents). The
design keeps that data local and minimizes exposure:

- **Local-first.** No network calls, no telemetry — transcripts never leave your
  machine.
- **Transcripts stay the source of truth.** Raw transcript JSON is never copied
  into the database; toktop stores normalized rows plus a `(file, offset)` pointer
  and re-reads the original line on demand. Toktop never modifies your transcripts.
- **Redaction on by default.** Secrets (env vars, tokens, database URLs, cookies,
  plus the [gitleaks](https://github.com/gitleaks/gitleaks) ruleset) are stripped
  from the **projected** text that is persisted-as-served or full-text indexed.
- **No open ports by default.** The HTTP API listens on a `0600` Unix socket —
  same-user-only. TCP is opt-in and requires a bearer token when bound off
  loopback.

### In scope

Vulnerabilities **in toktop itself**, for example:

- A secret slipping past redaction into the SQLite database or the FTS index
  (a redaction bypass on a projected/indexed field).
- Authentication/authorization bypass on the HTTP API (Unix-socket permissions,
  bearer-token check, loopback enforcement).
- Path traversal or unintended file reads during transcript discovery/ingest.
- A crafted transcript causing memory-unsafe behavior, resource exhaustion, or
  arbitrary file write/read.

### Out of scope

- **Secrets already present in your own transcripts.** Redaction only protects
  toktop's *projected* fields (stored/indexed text); it does not — and cannot —
  rewrite the original transcript files on disk, which remain the source of truth
  and are re-read on demand. Treat the transcript directories themselves
  (`~/.claude`, `~/.codex`) and `~/.toktop` as sensitive.
- Issues requiring an already-compromised local account (the Unix socket is
  same-user-only by design).
- Vulnerabilities in third-party dependencies that have no impact on toktop — but
  please still report anything that *does* affect it (we run `govulncheck` in CI).
