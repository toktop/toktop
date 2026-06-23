<p align="center">
  <img src="docs/logo.png" alt="" width="120">
</p>

<h1 align="center">Toktop</h1>

<p align="center">
  Local-first usage and live status for Claude Code, Codex &amp; opencode sessions — skills, MCP servers, and tools.
</p>

`toktop` reads the local session data that **Claude Code**, **Codex**, and **opencode**
record on your machine (JSONL transcripts for the first two, a SQLite store for opencode),
normalizes them into one provider-neutral model, stores them in a local SQLite database with
full-text search, and exposes everything through a CLI and an HTTP API v1 — including a
low-latency **live event stream** over Server-Sent Events.

It answers questions like: *which MCP servers and skills are installed but never actually
used? where did a tool retry-loop burn tokens? how many turns and tokens did a session
take? what is happening in my agent sessions right now?*

**Everything runs locally** — no network calls, no telemetry, transcripts never leave your
machine. Secret redaction is on by default.

```console
$ toktop summary
raw events: 84120
sessions: 1109
turns: 3195
invocations: 38964
tool calls: 26397
input tokens: 14.8M
output tokens: 41.3M
cache read tokens: 3.9B
cache write tokens: 187.7M (5m 120.4M · 1h 67.3M)
parse errors: 0

$ toktop mcps unused
SERVER     CALLS  TOOLS  TURNS  AVAILABILITY  SCOPE    CONFIG_PATH           LAST_USED
shadcn     0      0      0      0             project  ~/acme-web/.mcp.json
node_repl  0      0      0      0             user     ~/.codex/config.toml
```

Human-readable output abbreviates token counts (`14.8M`, `3.9B`); `--format json` and the
HTTP API keep raw integers. Cache-write totals with a long-lived subset also show their
TTL split — `(5m … · 1h …)` — because Anthropic bills the long-lived (1h) cache tier
higher than the 5m default; totals without one (codex has no cache writes; sessions that
only write the default 5m tier) show just the total. The `cache_write_long_tokens` field
carries the 1h subset in JSON.

---

## Install

**macOS / Linux**

```bash
curl -fsSL https://toktop.unceas.dev/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://toktop.unceas.dev/install.ps1 | iex
```

This drops a prebuilt binary in `~/.local/bin` (or `%LOCALAPPDATA%\toktop\bin` on Windows) —
no compiler needed. Override the location with `TOKTOP_INSTALL_DIR`.

**Upgrading is the same command** — the installer always fetches the latest release and
overwrites the binary in place, so re-run it whenever you want the newest version (keep the
same `TOKTOP_INSTALL_DIR` if you set one). There is no `toktop upgrade` subcommand. If a
daemon is running, `toktop daemon stop` afterwards so the next `status`/`stream` picks up the
new binary.

Releases are **signed**: `checksums.txt` is signed keyless with
[cosign](https://docs.sigstore.dev/) using the release workflow's GitHub OIDC identity
(logged in the Rekor transparency log), so a tampered release — not just transit
corruption — is detectable. The install scripts verify it automatically when `cosign` is
installed; otherwise they fall back to the sha256 check. To verify manually:

```bash
cosign verify-blob \
  --bundle checksums.txt.bundle \
  --certificate-identity-regexp '^https://github\.com/toktop/toktop/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

<details>
<summary>Build from source</summary>

**Requirements:** Go 1.26+, CGO (a C compiler), and the `sqlite_fts5` build tag — mandatory:
Toktop probes for FTS5 on startup and refuses to run without it, so every `go` command must
carry `-tags sqlite_fts5`.

```bash
git clone https://github.com/toktop/toktop && cd toktop
CGO_ENABLED=1 go build -tags sqlite_fts5 -o toktop ./cmd/toktop
```

The bare `go build` above stamps no version metadata, so `toktop --version` reports
`dev`. Use `make build` to inject the git-derived version, commit, and build date
(the release workflow does the same from the pushed tag).

</details>

---

## Quick start

```bash
toktop init        # create ~/.toktop/{config,data}
toktop ingest      # import Claude Code / Codex / opencode sessions (idempotent)
toktop summary     # imported counts and token totals
toktop status      # what's happening in your sessions right now
```

`toktop ingest` auto-detects which providers are present on disk. Re-run it any time — it is
idempotent (IDs are content-hashed), so unchanged transcripts are skipped. Run
`toktop doctor` if something looks off (it checks dirs, the DB, sqlite/FTS5, and per-provider
roots/hooks).

## How it works

```
Claude Code / Codex transcripts (JSONL) · opencode session store (SQLite)
        │  collect + parse (per provider)
        ▼
   provider-neutral trace index
   sessions → turns → invocations → tool calls   (+ MCP servers, skills)
        │  store
        ▼
   local SQLite (+ FTS5 full-text index)  ──►  CLI · HTTP API v1 · live SSE stream
```

The agent's own session data is the **source of truth**: Toktop stores normalized rows plus a
pointer back to it (a `(file, offset)` into the JSONL transcript, or the opencode session id)
and re-reads on demand. Deleting a session's source drops its raw bytes; the normalized rows
survive until the next reconcile. Change-detection is per-provider — file stat for the JSONL
providers, opencode's `event_sequence.seq` for its SQLite store.

---

## Analyze your usage

All analytics read the local SQLite DB directly (no daemon required) — so they show the
database **as of the last `ingest` or daemon reconcile**, not the live transcript. To pick up
new transcript activity, re-run `toktop ingest` (idempotent — unchanged files are skipped) or
keep a daemon running so the DB stays current automatically (see
[Live status](#live-status--event-stream)). `search` and the other analytics commands do
**not** auto-start a daemon — only `status` / `stream` do.

| Command | Shows |
| --- | --- |
| `toktop summary` | Trace counts + token totals |
| `toktop sessions` | Sessions, most-recent first (`sessions inspect <id>` for one) |
| `toktop turns` | Turns (`turns inspect\|timeline\|components <id>` for one) |
| `toktop search <query>` | Full-text search over turn text and tool calls (FTS5) |
| `toktop mcps` | MCP server usage rollup (`mcps unused` = declared but never called) |
| `toktop skills` | Skill usage rollup (`skills unused` = installed but never used) |
| `toktop tools` | Tool-call usage (call / turn / failed / rejected counts per tool) |
| `toktop models` | Model invocation usage (call / turn / token counts, incl. cache, per provider+model) |
| `toktop projects` | Per-project session / turn / tool counts |
| `toktop suggestions` | Rule-engine findings (`suggestions recompute` reruns the rules) |
| `toktop sources` | Configured providers and their discovery roots |

**Shared filter flags** (on `summary`, `sessions`, `turns`, `mcps`, `skills`, `tools`,
`models`, `projects`, `status`):

```
--sources claude-code,codex     # provider filter (repeatable / comma-separated)
--project <id>                  # project filter
--session <id>                  # session id or external session id
--status success,failed         # turn/session status filter
--since 24h    --until 7d       # duration (7d, 24h) or an RFC3339 timestamp
--subagents                     # include subagent transcripts (excluded by default)
```

`--subagents` opts a list/stats query (also `search` and `export`) into subagent
transcripts — Claude Code's nested Task/Agent runs and Workflow internal agents, and
Codex's spawned agents (`spawn_agent`). They are ingested and linked to their parent
session, but every list, count, search, and export **excludes them by default** so
the view is your own top-level sessions; pass `--subagents` to fold them in (e.g.
`tools --subagents` to count tool use inside workflows). `status` is top-level only
and has no such flag; `suggestions` rules are always top-level.

**Output formats** — `--format table` (default) `| json | ndjson | csv | markdown | html`
(`summary` and `search` are `table|json`, stdout only — redirect with shell `>`). Every
full-format list command also takes `--output <path>` (default `-` = stdout, else a file)
and `--columns col1,col2,…` to pick and reorder displayed columns by header name
(table/csv/markdown/html only — rejected with `--format json|ndjson`; project JSON fields
with a tool like `jq`).
`sessions`, `turns`, and `status` page with `--limit` / `--offset` (default 20); `sessions`
and `turns` also take `--sort`.

```bash
toktop turns --sources claude-code --since 24h --sort tokens_desc --limit 10
toktop sessions --columns id,title,tokens,started     # show only the columns you want (trims the wide default)
toktop search 'rate limit' kind:turn source:claude-code --limit 20   # kind:/source: tokens are separate args
toktop mcps unused --format json
toktop sessions inspect 7fe8484969b12a21
toktop turns timeline 2dcb402ffc459e93     # per-turn event timeline
```

`suggestions` runs a small rule engine over your history — e.g. an MCP server enabled but
unused for 30 days, a large tool output that later turns keep carrying in context, a turn that
took many invocations to succeed, or a session whose later turns carry far more context than its
earlier ones. Estimated
figures always carry a `confidence`; observed counts are exact.

---

## Handoff

One side hits a quota limit or gets interrupted mid-workflow and you want to
continue in the *other* agent — analyze in Claude Code, execute in Codex, or the
reverse — without re-deriving or re-running everything. `toktop handoff` packages
one session as an **Evidence-based Handoff Package** another agent can pick up. It
is symmetric: a Claude Code session resumes in Codex and vice versa.

```bash
toktop handoff create --session 7fe8484969b12a21
# → ~/.toktop/handoff/<session>/   (--output <dir> to override · --max-output-bytes N to clip)
```

The package leads with a lean **`digest.md`** — headed by the session's title
and workflow status, then the user→assistant narrative with no tool-call bloat —
so the receiver orients cheaply on *any* session. For a multi-agent workflow it also recovers each completed sub-agent's
real result (so they are reused, not re-run), flags any run the user declined — a
denied plan or dismissed prompt — so the receiver skips it rather than re-running,
plus provenance pointers back to the raw transcript; every fact is tagged
`evidence` / `inference` / `unknown`. The
same package is served over HTTP at `GET /v1/sessions/{id}/handoff` (one JSON
body, `digest` inline).

To consume a package and continue the work, use the
[`toktop-resume`](#agent-skills) skill below.

---

## Agent Skills

Beyond the `toktop` binary, the repo ships **Agent Skills** under
[`skills/`](skills/) — tool-neutral skills (Claude Code, Codex, …) that wrap a
`toktop` workflow so the *receiving* agent can drive it with one slash command.
Install any of them with [`npx skills`](https://github.com/vercel-labs/skills).

| Skill | What it does |
| --- | --- |
| [`toktop-resume`](skills/toktop-resume/) | Resume an interrupted session from a handoff package. Install `npx skills add toktop/toktop/skills/toktop-resume`, then run `/toktop-resume [session-id]` in the *receiving* agent: it builds the package and continues from it under the receiver-prompt rules — orienting on the lean `digest.md` and reusing completed sub-agent results rather than redoing them. Explicit-invocation only; with no id it lists recent sessions to pick from. |

---

## Live status & event stream

This is what sets toktop apart from a plain transcript analyzer: it is a **real-time event
broker**. A background daemon watches your transcript roots and (optionally) receives hook
callbacks, fans everything out over SSE, and answers a **current live-status** query — so
dashboards, status bars, and RGB indicators can react to what your agents are doing *right
now*.

**Get the current live status (one-shot snapshot):**

```bash
toktop status                                   # every active session's current state
toktop status --session <id>                    # one session
toktop status --sources claude-code --since 24h
```

`status` returns each session's **current status** (active / awaiting confirmation / success
/ failed), title, turn/tool counts, project, and last activity time. It prefers the running daemon
(which overlays the in-memory broker — the freshest, same view SSE consumers get) and falls
back to reading the local store when no daemon is up.

**Subscribe to the live event stream (SSE):**

```bash
toktop stream                                   # everything, live
toktop stream claude-code:052a6e33-...          # one session by id
toktop stream claude-code:ID --status-only      # status changes only (no firehose)
toktop stream claude-code:ID,codex:OTHER        # several at once
```

`stream` subscribes to the daemon's `GET /v1/stream` SSE endpoint. On reconnect the daemon
replays missed events from its append-only event log, so a consumer never silently loses a
transition.

**Run / control the daemon:**

```bash
toktop daemon serve     # foreground: watch transcripts + serve HTTP/SSE + live broker
toktop daemon run       # foreground: watch transcripts only (no HTTP/SSE live broker)
toktop daemon status    # is it running? what is it watching? is it keeping up (backpressure)?
toktop daemon pause | resume | trigger | stop
```

`status` and `stream` **auto-start a daemon on demand** (config `autostart`, default on) and
it **idle-stops** ~60 s after the last SSE consumer disconnects (config `idle_stop`). Exactly
one daemon owns the live event log per home; live/status/stream commands are daemon *clients*
over the socket. To keep one running indefinitely — e.g. so analytics / `search` always
reflect the latest transcripts — use `toktop daemon run` (watch-only; never idle-stops), or
`toktop config set idle_stop off` before `toktop daemon serve`.

**Push live status from the agents themselves (hooks):** transcript watching alone lags a
little; installing observer hooks makes status near-instant. Each hook POSTs to
`/v1/hooks:intake`.

```bash
toktop hooks install --sources=claude-code             # observe Claude Code (user scope)
toktop hooks install --sources=claude-code,codex       # both at once
toktop hooks install --sources=opencode                # opencode (installs a plugin, see below)
toktop hooks status                                    # what's installed
toktop hooks uninstall --sources=claude-code
```

Hook commands default to the configured daemon address. The default unix socket
needs no token; a TCP hook endpoint references `~/.toktop/config/api-token`
instead of embedding the secret in the agent config.

**opencode has no config-level shell hooks**, so `hooks install --sources=opencode`
writes a small observer **plugin** into opencode's auto-loaded `plugins/` dir
(`~/.config/opencode/plugins/toktop-observer.js`) instead of a `curl` entry; the plugin
POSTs to the same `/v1/hooks:intake` endpoint on each **top-level** session's status
transition — busy/idle (opencode's `session.status`/`session.idle`), errors, and
permission prompts — and drops subagent runs so live status stays top-level only, like
the other providers. It takes effect on opencode's next launch. A TCP endpoint bakes the
bearer token into the plugin file (a secret on disk under the opencode config dir), so
the default unix socket is preferred.

**Claude Code hooks fire immediately; Codex hooks must be trusted first.** Codex treats a
third-party (unmanaged) hook as *untrusted* the first time it sees it and **only runs hooks
you have approved** — so after `hooks install --sources=codex` you must trust the toktop
observer hook in Codex before any callbacks fire (until then Codex falls back to transcript
watching, which lags a little). Codex tracks a `trusted_hash` per hook, so it re-prompts
whenever the hook command changes — re-approve it after a toktop upgrade that rewrites the
hook entry. `toktop hooks status` only reports that the entry is *installed*, not that Codex
has *trusted* it.

**Emit a custom live event** into a running daemon (e.g. from a script):

```bash
toktop emit --type session.active --provider claude-code --session <id> --status active
```

Pipeline at a glance: `hook / emit → POST intake → event log → in-memory bus → SSE consumers`,
and any time you can ask `toktop status` (or `GET /v1/status`) for the current snapshot.

---

## Web UI (opt-in build)

`toktop ui` opens a local web interface backed by the running daemon. It is a **daemon
client** — the same `/v1` HTTP API everything else uses — so it requires the daemon to be up
(auto-started by default, see above).

**The web UI is opt-in at build time.** The standard `make build` / `go build` does not include
it. To build with the embedded SPA:

```bash
make ui                                     # builds the React SPA then compiles it in
# equivalent: cd web && pnpm build, then:
go build -tags sqlite_fts5,ui ./cmd/toktop
```

A binary built without the UI flag prints `this build has no web UI; rebuild with -tags ui`
and exits when `toktop ui` is invoked.

**Start the web UI:**

```bash
toktop ui                   # bind on a random loopback port, open browser automatically
toktop ui --no-browser      # print the URL instead of opening a browser
toktop ui --addr 127.0.0.1:8080   # bind on a fixed port
```

`toktop ui` starts an ephemeral **loopback reverse-proxy** that:

- serves the embedded SPA at `/`
- proxies all `/v1` traffic (including SSE) to the daemon, injecting the daemon bearer token
  so the browser never sees it

The proxy is guarded by an **ephemeral per-launch token**: the URL printed on startup includes
`?t=<nonce>`. Opening that URL sets a `HttpOnly; SameSite=Strict` cookie for the session;
every subsequent same-origin request (including `EventSource` for the live stream) carries
the cookie automatically. Unauthenticated requests get `401`. The nonce is regenerated on
every invocation, so old URLs do not grant access.

**Flags:**

| Flag | Default | Description |
| --- | --- | --- |
| `--no-browser` | off | Print the URL to stdout instead of opening the default browser |
| `--addr` | `127.0.0.1:0` | Loopback address to bind the proxy on (must be `127.0.0.1` or `localhost`; a port of `0` picks a random free port) |

**Security model.** The proxy binds loopback only — no remote access, no inbound connections
from the network. The bearer token the proxy uses to reach the daemon never leaves the local
process. The SPA has no backend of its own; it reads data exclusively through the proxied
`/v1` endpoints. No external services, no telemetry, offline-capable.

---

## HTTP API v1

Start a server two ways:

```bash
toktop serve            # HTTP API only (no transcript watching)
toktop daemon serve     # API + watching + live broker (see above)
```

**Transport & auth.** By default the API binds a **unix socket** at
`~/.toktop/run/toktop.sock` (mode `0600`, same-user only — no port, no token). TCP is opt-in
via config `addr=tcp://host:port`; off loopback it **requires a bearer token** read from
`~/.toktop/config/api-token` (auto-generated). CLI clients send it automatically; pass
`--token` / `--no-auth` to override.

| Method & path | Purpose |
| --- | --- |
| `GET /v1/health` | Liveness |
| `GET /v1/summary` | Counts + token totals |
| `GET /v1/sessions` · `/v1/sessions/{id}` · `/{id}/handoff` | List / one session / Evidence-based Handoff Package (`max_output_bytes`) |
| `GET /v1/turns` · `/{id}` · `/{id}/timeline` · `/{id}/components` | Turns + per-turn detail |
| `GET /v1/projects` · `/v1/tools` · `/v1/models` | Project / tool / model rollups |
| `GET /v1/mcps` · `/v1/mcps/unused` · `/v1/skills` · `/v1/skills/unused` | MCP / skill usage |
| `GET /v1/search` | Full-text search (`q`, `limit`, `kind`, `source`, `subagents`) |
| `GET /v1/suggestions` · `POST /v1/suggestions:recompute` | Rule findings |
| `POST /v1/export` | Full trace index (JSON) |
| **`GET /v1/stream`** | **Live event stream (SSE)** |
| **`GET /v1/status`** | **Current live session-state snapshot** |
| `POST /v1/events` · `POST /v1/hooks:intake` | Ingest live events / hook callbacks |
| `GET /v1/daemon` · `:trigger` · `:pause` · `:resume` | Daemon state / control |
| `GET /v1/sources` · `GET /v1/config` · `POST /v1/config:reload` | Sources / config |
| `POST /v1/data:prune` · `GET /v1/data/retention` · `/profiles` · `:prune` | Data lifecycle |

The list/stats/search routes accept the same filters as the CLI as query params
(`source`, `project`, `session`, `status`, `since`, `until`, `sort`, `limit`,
`offset`), including **`subagents=1`** to fold in subagent transcripts (excluded by
default, mirroring the CLI `--subagents`).

---

## Configuration

`~/.toktop/config/config.json` is the **single source of truth** for every persistent
setting. Precedence is just *built-in default < config.json* — there is no env/flag override
layer. Edits hot-reload (a bad edit is logged and the previous config is kept).

```bash
toktop config get             # all effective values + their source (default / file)
toktop config get autostart   # one key
toktop config set addr tcp://127.0.0.1:8787
toktop config unset addr
toktop config path            # where the file lives
```

| Key | Default | Meaning |
| --- | --- | --- |
| `redact` | `on` | Secret redaction on projected/indexed fields |
| `autostart` | `on` | `status`/`stream` may auto-start a daemon |
| `idle_stop` | `on` | Daemon idle-stops ~60 s after the last SSE consumer (`off` = keep running) |
| `addr` | _(unix socket)_ | Server bind: empty = `~/.toktop/run/toktop.sock`, or `tcp://host:port` |
| `interval` | _(built-in)_ | Daemon full-reconcile interval (e.g. `5m`) |
| `timezone` | `UTC` | Display timezone: `utc`, `local`, or an IANA name |
| `roots.<provider>` | _(auto-discovered)_ | Override a provider's transcript roots |

**Environment.** Only `TOKTOP_HOME` is toktop's own (it locates `~/.toktop`, so it can't
live inside the config file). `CLAUDE_CONFIG_DIR` / `CODEX_HOME` are upstream conventions
honored during root discovery.

---

## Data lifecycle & privacy

```bash
toktop export                        # full trace index as JSON (--since 24h, --format ndjson, --output file, --max-output-bytes N)
toktop data prune --help             # age out old raw events and redact normalized rows
toktop data retention status         # effective retention windows for one profile
toktop data retention profiles       # list the retention profiles
toktop db stats                      # database size / WAL / FTS / row counts
toktop db checkpoint                 # run a SQLite WAL checkpoint
toktop db optimize                   # checkpoint WAL and run SQLite/FTS optimize
toktop db reindex                    # rebuild the FTS search index
toktop db path                       # path to the SQLite file
```

- **Local-first.** No network calls, no telemetry. Transcripts never leave your machine.
- **Redaction** is on by default and runs on persisted/indexed text (turn text, tool
  input/output) — never on the raw transcript bytes.
- **Same-user only.** The default unix socket is `0600`; TCP off loopback requires a token.

---

## Uninstall

`toktop uninstall` reverses an install — it stops the daemon, removes the observer hooks it
injected into Claude Code / Codex, and deletes the home directory, then prints the one command
to remove the binary itself:

```bash
toktop uninstall              # prompts before deleting ~/.toktop
toktop uninstall --keep-data  # stop daemon + remove hooks, but keep config / data / DB
toktop uninstall --yes        # skip the confirmation prompt (for scripts)
```

It deletes the binary last and leaves that single step to you — a running executable can't
remove itself on every platform — so it ends by printing `rm <path>`. `TOKTOP_INSTALL_DIR` /
`TOKTOP_HOME` set at install time are honored. Toktop only ever *reads* your transcripts (the
hooks are its only writes), so removing it leaves Claude Code, Codex, and their history
untouched.

<details>
<summary>Remove it by hand instead</summary>

```bash
toktop daemon stop                                   # stop the background daemon
toktop hooks uninstall --sources=claude-code,codex   # remove injected hooks
rm ~/.local/bin/toktop                               # the binary (%LOCALAPPDATA%\toktop\bin on Windows)
rm -rf ~/.toktop                                      # all config, data, DB, and the socket
```

Order matters: uninstall the hooks **before** removing the binary — they live in
Claude Code `settings.json` (for example `~/.claude/settings.json`, or `CLAUDE_CONFIG_DIR`),
Codex `~/.codex/hooks.json`, and (for opencode) the observer plugin at
`~/.config/opencode/plugins/toktop-observer.js`. The installed Claude Code / Codex hook is a
`curl` POST to the toktop daemon intake endpoint, and the opencode plugin POSTs to the same
endpoint, so stale entries keep trying to reach a daemon you removed.

</details>

---

## License

Licensed under the [Apache License 2.0](LICENSE).
