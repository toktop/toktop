package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/query"
	"toktop.unceas.dev/internal/retention"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	limit := defaultPageLimit
	offset := 0
	since := ""
	until := ""
	token := ""
	noAuth := false
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	addLimitOffsetFlags(fs, &limit, &offset)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	fs.StringVar(&token, "token", token, "bearer token (default: read api-token file)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "do not send a bearer token")
	setFlagUsage(fs,
		"usage: toktop status [flags]",
		"",
		"One-shot snapshot of current live session state (session status, not",
		"daemon health). Reads the running daemon (which overlays the live broker",
		"state); falls back to the local store when no server is up.",
		"Narrow it with the shared --sources/--project/--session/--status/--since.",
		"",
		"examples:",
		"  toktop status                        # all recent sessions",
		"  toktop status --session <id>         # one session by id or external id",
		"  toktop status --sources claude-code --since 24h",
	)
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if code := checkPaging(limit, offset, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	srcTokens, serr := resolveFilterTokens(sources)
	if serr != nil {
		cliErr(stderr, serr)
		return 2
	}
	if err := validateStatuses(statuses); err != nil {
		cliErr(stderr, err)
		return 2
	}
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	snap := loader.Current()
	addr := clientAddr(snap)

	cols := []string{"source", "session", "external", "title", "status", "turns", "tools", "project", "last_activity"}
	row := func(item sqlite.LiveSessionItem) []string {
		return []string{item.Provider, item.SessionID, emptyDash(item.ExternalSessionID), emptyDash(oneLine(item.Title, 40)), item.CurrentStatus,
			strconv.Itoa(item.TurnCount), strconv.Itoa(item.ToolCallCount), item.ProjectName, formatTime(item.LastActivityAt)}
	}

	// Resolve --columns here, before ensureDaemon (below) can autostart a daemon:
	// a usage error (unknown column, or --columns with json/ndjson) must not spawn
	// one. Project once and hand the result to whichever emitList path runs.
	cols, row, code := applyColumns(*columns, format, cols, row, stderr)
	if code >= 0 {
		return code
	}

	// Prefer the daemon's /v1/status: it overlays the in-memory broker state, so
	// it is the same fresh, consistent snapshot /v1/stream and downstream SSE
	// consumers see. The direct store read below omits that overlay (it can lag
	// the broker), so it is used only when no daemon is reachable.
	if err := ensureDaemon(ctx, home, addr, snap.Autostart, stderr); err != nil {
		cliErr(stderr, err)
	}
	q := url.Values{}
	for _, v := range srcTokens {
		q.Add("source", v)
	}
	for _, v := range projects {
		q.Add("project", v)
	}
	for _, v := range sessions {
		q.Add("session", v)
	}
	for _, v := range statuses {
		q.Add("status", v)
	}
	if since != "" {
		q.Set("since", since)
	}
	if until != "" {
		q.Set("until", until)
	}
	// limit is >= 1 (checkPaging); offset 0 is the server default, so only send it
	// when non-zero.
	q.Set("limit", strconv.Itoa(limit))
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	q.Set("sort", "started_desc")
	switch items, err := liveStatusFromServer(ctx, addr, clientToken(token, noAuth), q); {
	case err == nil:
		return emitList(*output, stdout, stderr, format, "", items, cols, row)
	case !errors.Is(err, errStreamServerUnreachable):
		cliErr(stderr, err)
		return 1
	default:
		fmt.Fprintf(stderr, "toktop: no live server at %s; reading session state from the local store (may lag the live broker — run `toktop daemon serve` for the freshest state)\n", addr)
	}

	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	filter, err := parseFilterFlags(since, until, "started_desc")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter.Limit = limit
	filter.Offset = offset
	// Live status is a top-level-session view; subagents never carry live hook
	// status, so it has no --subagents toggle and always excludes them.
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, false); err != nil {
		cliErr(stderr, err)
		return 2
	}
	page, err := svc.ListLiveSessions(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, "", page.Items, cols, row)
}

func runStream(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	statusOnly := false
	token := ""
	noAuth := false
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "table|ndjson|csv")
	fs.BoolVar(&statusOnly, "status-only", statusOnly, "only emit session status changes (drop the raw event firehose)")
	fs.StringVar(&token, "token", token, "bearer token (default: read api-token file)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "do not send a bearer token")
	setFlagUsage(fs,
		"usage: toktop stream [app:session ...] [flags]",
		"",
		"Tail the live event stream in real time. It subscribes to the running",
		"daemon over SSE (auto-starting one when configured); the daemon is the sole",
		"owner of the live event log, so there is no local-log fallback. With no",
		"targets it streams every session; pass one or more app:session targets to",
		"follow specific ones.",
		"",
		"  app      provider: claude-code (alias claude) or codex",
		"  session  matches a session id, external session id, transcript path, or file",
		"",
		"examples:",
		"  toktop stream                                # everything, live",
		"  toktop stream claude-code:052a6e33-3d3c-...  # one Claude Code session by id",
		"  toktop stream claude-code:ID --status-only   # status changes only, no firehose",
		"  toktop stream claude-code:ID,codex:OTHER     # several sessions at once",
	)

	flagArgs, positional, _ := partitionArgs(args, valueFlagSet(fs))
	if code := parseFlags(fs, flagArgs, stdout); code >= 0 {
		return code
	}
	if err := validateFormat(format, "table", "ndjson", "csv"); err != nil {
		cliErr(stderr, err)
		return 2
	}
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	snap := loader.Current()
	addr := clientAddr(snap)
	targets, err := liveevent.ParseWatchTargets(positional)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}

	emit, closeEmit, err := liveWatchEmitter(format, stdout)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	defer closeEmit()

	// stream subscribes to the daemon's /v1/stream SSE endpoint and only formats
	// output. The daemon is the single acquirer of the event log; the CLI never
	// opens that bbolt file — a second opener just times out on the exclusive
	// lock the daemon holds, which was the original "stream sees nothing" bug.
	// With no daemon there is no producer and nothing to tail, so surface that
	// plainly instead of silently reading a frozen on-disk log.
	if err := ensureDaemon(ctx, home, addr, snap.Autostart, stderr); err != nil {
		cliErr(stderr, err)
	}
	err = streamFromServer(ctx, addr, clientToken(token, noAuth), targets, statusOnly, emit)
	switch {
	case err == nil || ctx.Err() != nil:
		return 0
	case errors.Is(err, errStreamServerUnreachable):
		cliErrf(stderr, "no live server at %s: %v\nstart one with `toktop daemon serve` — the daemon owns the live event stream and `stream` subscribes to it", addr, err)
		return 1
	default:
		cliErr(stderr, err)
		return 1
	}
}

func liveWatchEmitter(format string, stdout io.Writer) (func(liveevent.Event) error, func(), error) {
	switch format {
	case "table", "":
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "event_id\ttype\tsource\tsession\texternal\tstatus\tproject\tat")
		_ = w.Flush()
		return func(ev liveevent.Event) error {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				ev.EventID, ev.Type, emptyDash(textutil.FirstNonBlank(ev.Provider, ev.SourceID)),
				emptyDash(ev.SessionID), emptyDash(ev.ExternalSessionID),
				emptyDash(ev.Status), emptyDash(textutil.FirstNonBlank(ev.ProjectName, ev.ProjectPath)),
				formatTime(ev.At))
			return w.Flush()
		}, func() { _ = w.Flush() }, nil
	case "ndjson":
		encoder := json.NewEncoder(stdout)
		return func(ev liveevent.Event) error {
			return encoder.Encode(ev)
		}, func() {}, nil
	case "csv":
		w := csv.NewWriter(stdout)
		_ = w.Write([]string{"event_id", "type", "source", "session", "external", "status", "project", "at"})
		w.Flush()
		return func(ev liveevent.Event) error {
			_ = w.Write([]string{
				ev.EventID, ev.Type, textutil.FirstNonBlank(ev.Provider, ev.SourceID),
				ev.SessionID, ev.ExternalSessionID, ev.Status,
				textutil.FirstNonBlank(ev.ProjectName, ev.ProjectPath), formatTime(ev.At),
			})
			w.Flush()
			return w.Error()
		}, func() { w.Flush() }, nil
	default:
		return nil, nil, errors.New("stream --format supports table, ndjson, or csv output")
	}
}

func runProjects(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	subagents := addSubagentsFlag(fs)
	setFlagUsage(fs, "usage: toktop projects [flags]", "List projects with session / turn / tool-call counts.")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	rows, err := svc.ListProjects(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, rows, []string{"id", "source_id", "name", "path", "sessions", "turns", "tool_calls", "last_activity"}, func(item sqlite.ProjectListItem) []string {
		return []string{item.ID, item.SourceID, item.Name, item.Path,
			strconv.Itoa(item.SessionCount), strconv.Itoa(item.TurnCount),
			strconv.Itoa(item.ToolCallCount), formatTime(item.LastActivity)}
	})
}

func runActivity(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	bucket := "1h"
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("activity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&bucket, "bucket", bucket, "bucket width: duration like 5m, 1h, 6h, 1d")
	subagents := addSubagentsFlag(fs)
	setFlagUsage(fs, "usage: toktop activity [flags]", "Roll turns into fixed-width time buckets (turns / tools / tokens per bucket).")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	width, err := query.ParseBucketWidth(bucket, time.Now().UTC())
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	rows, err := svc.ActivitySeries(ctx, filter, int(width.Seconds()))
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, rows, []string{"bucket", "sessions", "turns", "tools", "input_tokens", "output_tokens"}, func(b sqlite.ActivityBucket) []string {
		return []string{formatTime(b.Bucket),
			strconv.Itoa(b.Sessions), strconv.Itoa(b.Turns), strconv.Itoa(b.ToolCalls),
			strconv.Itoa(b.InputTokens), strconv.Itoa(b.OutputTokens)}
	})
}

func runTools(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	subagents := addSubagentsFlag(fs)
	setFlagUsageSub(fs, "usage: toktop tools [flags]",
		[]subcmdDoc{{"calls", "drill into one tool's individual calls (e.g. its failed/rejected instances)"}},
		"Roll up tool-call usage (call / turn / failed / rejected counts per tool).")

	// Dispatch the `calls` drill-down with the union of this command's and the leaf's
	// value flags (toolsDispatchValueFlags), so a value flag whose argument is "calls"
	// — including a `calls`-only flag like `--name calls` — is not mistaken for the subcommand.
	_, rest, firstPos, isCalls := firstLeafSubcommand(args, toolsDispatchValueFlags(), "calls")
	if !isCalls && firstPos != "" {
		cliErrf(stderr, "unknown tools subcommand %q (want calls, or flags to list)", firstPos)
		return 2
	}
	if isCalls {
		return runToolCalls(ctx, rest, home, stdout, stderr)
	}
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	tools, err := svc.ListTools(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, tools, []string{"kind", "name", "mcp_server", "calls", "turns", "failed", "rejected", "last_used"}, func(item sqlite.ToolListItem) []string {
		return []string{item.Kind, item.Name, item.MCPServer,
			strconv.Itoa(item.CallCount), strconv.Itoa(item.TurnCount),
			strconv.Itoa(item.FailedCount), strconv.Itoa(item.RejectedCount), formatTime(item.LastUsedAt)}
	})
}

// toolCallsFlags holds the `tools calls` flag bindings. toolCallsFlagSet registers
// them; runToolCalls reads them, and toolsDispatchValueFlags derives the dispatch
// value-flag vocabulary from the same set so a leaf flag value can't be mistaken
// for the `calls` keyword — never hand-write that set (CLI conventions).
type toolCallsFlags struct {
	format    string
	output    *string
	columns   *string
	name      string
	kind      string
	mcpServer string
	since     string
	until     string
	limit     int
	offset    int
	sources   rootList
	projects  rootList
	sessions  rootList
	statuses  rootList
	subagents *bool
}

func toolCallsFlagSet(f *toolCallsFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("tools calls", flag.ContinueOnError)
	f.format = "table"
	fs.StringVar(&f.format, "format", f.format, formatFlagUsage)
	f.output = addOutputFlag(fs)
	f.columns = addColumnsFlag(fs)
	fs.StringVar(&f.name, "name", "", "tool name to drill into (required)")
	fs.StringVar(&f.kind, "kind", "", "tool kind (builtin or mcp) to disambiguate a name shared across kinds")
	fs.StringVar(&f.mcpServer, "mcp-server", "", "MCP server name, for an mcp-kind tool")
	fs.StringVar(&f.since, "since", "", "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&f.until, "until", "", "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	f.limit = sqlite.DefaultToolCallLimit
	addLimitOffsetFlags(fs, &f.limit, &f.offset)
	addFilterFlags(fs, &f.sources, &f.projects, &f.sessions, &f.statuses)
	f.subagents = addSubagentsFlag(fs)
	return fs
}

// toolsDispatchValueFlags derives runTools' dispatch value-flag set from the real
// `tools calls` leaf flag set (a superset of the bare `tools` flags), so a value
// flag whose argument is "calls" is not mistaken for the subcommand.
func toolsDispatchValueFlags() map[string]bool {
	return valueFlagSet(toolCallsFlagSet(&toolCallsFlags{}))
}

// validateToolCallStatuses rejects any --status token outside the tool-call grain:
// `tools calls --status` filters the call's own status (success/failed/rejected),
// not turn status, so a turn-only status like `interrupted` must error, not match
// zero rows silently.
func validateToolCallStatuses(statuses rootList) error {
	return query.ValidateToolCallStatuses(splitFlagValues(statuses))
}

// runToolCalls is `toktop tools calls` — the drill-down listing the individual
// tool-call instances behind a tool's aggregate counts. --status filters the tool
// call's OWN status (failed/rejected/success), a different grain from turn status,
// so it is routed to CallStatuses rather than the shared turn-status filter.
func runToolCalls(ctx context.Context, args []string, home string, stdout, stderr io.Writer) int {
	var f toolCallsFlags
	fs := toolCallsFlagSet(&f)
	fs.SetOutput(stderr)
	setFlagUsage(fs, "usage: toktop tools calls --name <tool> [--status failed|rejected] [flags]",
		"List the individual tool-call instances for one tool — the drill-down behind its failed/rejected counts. --status filters the tool call's own status (failed/rejected/success), not turn status.")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(f.format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	if f.name == "" {
		cliErrf(stderr, "tools calls requires --name")
		return 2
	}
	if code := checkPaging(f.limit, f.offset, stderr); code >= 0 {
		return code
	}
	if err := validateToolCallStatuses(f.statuses); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(f.since, f.until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	// Pass nil turn-statuses: --status is the tool-call grain, applied via CallStatuses.
	if err := applyMultiFilter(&filter, f.sources, f.projects, f.sessions, nil, *f.subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter.Limit = f.limit
	filter.Offset = f.offset
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	calls, err := svc.ListToolCalls(ctx, sqlite.ToolCallFilter{
		Scope:        filter,
		Kind:         f.kind,
		Name:         f.name,
		MCPServer:    f.mcpServer,
		CallStatuses: splitFlagValues(f.statuses),
	})
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*f.output, stdout, stderr, f.format, *f.columns, calls,
		[]string{"session", "turn", "project", "source", "status", "duration_ms", "started", "detail"},
		func(item sqlite.ToolCallListItem) []string {
			return []string{
				emptyDash(textutil.FirstNonBlank(item.SessionTitle, item.SessionID)),
				strconv.Itoa(item.TurnIndex + 1),
				emptyDash(item.ProjectName),
				item.SourceID,
				item.Status,
				strconv.FormatInt(item.DurationMs, 10),
				formatTime(item.StartedAt),
				// error is usually empty for tools like Bash; the failure text lives
				// in output, so show whichever carries the detail (mirrors the web UI).
				oneLine(textutil.FirstNonBlank(item.Error, item.Output), 80),
			}
		})
}

func runModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	subagents := addSubagentsFlag(fs)
	setFlagUsage(fs, "usage: toktop models [flags]", "Roll up model invocation usage (call / turn / token counts per model).")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	models, err := svc.ListModels(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, models, []string{"provider", "model", "calls", "turns", "input_tokens", "output_tokens", "cache_read", "cache_write", "last_used"}, func(item sqlite.ModelListItem) []string {
		return []string{item.Provider, emptyDash(item.Model),
			strconv.Itoa(item.CallCount), strconv.Itoa(item.TurnCount),
			strconv.Itoa(item.InputTokens), strconv.Itoa(item.OutputTokens),
			strconv.Itoa(item.CacheReadTokens), strconv.Itoa(item.CacheWriteTokens), formatTime(item.LastUsedAt)}
	})
}

var mcpCols = []string{"source", "server", "calls", "tools", "turns", "availability", "scope", "config_path", "last_used"}

func mcpRow(item sqlite.MCPListItem) []string {
	return []string{item.SourceID, item.Server, strconv.Itoa(item.CallCount), strconv.Itoa(item.ToolCount),
		strconv.Itoa(item.TurnCount), strconv.Itoa(item.Availability),
		item.Scope, item.ConfigPath, formatTime(item.LastUsedAt)}
}

func runMCPs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("mcps", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	subagents := addSubagentsFlag(fs)
	setFlagUsageSub(fs, "usage: toktop mcps [flags]",
		[]subcmdDoc{{"unused", "list declared MCP servers with zero observed calls (no filters)"}},
		"Roll up MCP server usage. The availability column counts turns where the server was observed available.")

	// Dispatch the `unused` subcommand with the command's own value-flag
	// vocabulary, so a value flag whose argument is "unused" (e.g.
	// `mcps --sources unused`) is not mistaken for the subcommand.
	_, rest, firstPos, unused := firstLeafSubcommand(args, valueFlagSet(fs), "unused")
	if !unused && firstPos != "" {
		cliErrf(stderr, "unknown mcps subcommand %q (want unused, or flags to list)", firstPos)
		return 2
	}
	if unused {
		// `unused` takes no filters, so it parses with a minimal flag set that
		// rejects the filter flags above.
		uf := flag.NewFlagSet("mcps unused", flag.ContinueOnError)
		uf.SetOutput(stderr)
		uf.StringVar(&format, "format", format, formatFlagUsage)
		columnsU := addColumnsFlag(uf)
		setFlagUsage(uf, "usage: toktop mcps unused [--format ...]", "List declared MCP servers with zero observed calls. Accepts no filters.")
		if code := parseFlagsNoPositionals(uf, rest, stdout, stderr); code >= 0 {
			return code
		}
		if err := validateListFormat(format); err != nil {
			cliErr(stderr, err)
			return 2
		}
		svc, store, err := openService(ctx, home)
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		defer store.Close()
		mcps, err := svc.ListUnusedMCPs(ctx)
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		return emitList(*output, stdout, stderr, format, *columnsU, mcps, mcpCols, mcpRow)
	}
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	mcps, err := svc.ListMCPs(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, mcps, mcpCols, mcpRow)
}

func runSkills(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessions, statuses rootList
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	addFilterFlags(fs, &sources, &projects, &sessions, &statuses)
	subagents := addSubagentsFlag(fs)
	setFlagUsageSub(fs, "usage: toktop skills [flags]",
		[]subcmdDoc{{"unused", "list installed skills with zero inferred uses (no filters)"}},
		"Roll up skill usage (installed skills + inferred-used counts).")

	// Dispatch the `unused` subcommand with the command's own value-flag
	// vocabulary, so a value flag whose argument is "unused" (e.g.
	// `skills --sources unused`) is not mistaken for the subcommand.
	_, rest, firstPos, unused := firstLeafSubcommand(args, valueFlagSet(fs), "unused")
	if !unused && firstPos != "" {
		cliErrf(stderr, "unknown skills subcommand %q (want unused, or flags to list)", firstPos)
		return 2
	}
	if unused {
		// `unused` takes no filters, so it parses with a minimal flag set.
		uf := flag.NewFlagSet("skills unused", flag.ContinueOnError)
		uf.SetOutput(stderr)
		uf.StringVar(&format, "format", format, formatFlagUsage)
		columnsU := addColumnsFlag(uf)
		setFlagUsage(uf, "usage: toktop skills unused [--format ...]", "List installed skills with zero inferred uses. Accepts no filters.")
		if code := parseFlagsNoPositionals(uf, rest, stdout, stderr); code >= 0 {
			return code
		}
		if err := validateListFormat(format); err != nil {
			cliErr(stderr, err)
			return 2
		}
		svc, store, err := openService(ctx, home)
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		defer store.Close()
		skills, err := svc.ListUnusedSkills(ctx)
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		return emitList(*output, stdout, stderr, format, *columnsU, skills, []string{"source", "name", "scope", "description", "source_path"}, func(item sqlite.SkillListItem) []string {
			return []string{item.SourceID, item.Name, item.Scope, textutil.Truncate(item.Description, 80), item.SourcePath}
		})
	}
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessions, statuses, *subagents); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	skills, err := svc.ListSkills(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return emitList(*output, stdout, stderr, format, *columns, skills, []string{"source", "name", "scope", "installed", "inferred_used", "last_used", "source_path"}, func(item sqlite.SkillListItem) []string {
		installed := "no"
		if item.Installed {
			installed = "yes"
		}
		return []string{item.SourceID, item.Name, item.Scope, installed, strconv.Itoa(item.InferredUsedCount), formatTime(item.LastUsedAt), item.SourcePath}
	})
}

func runSuggestions(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	rule := ""
	recompute := false
	fs := flag.NewFlagSet("suggestions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	output := addOutputFlag(fs)
	columns := addColumnsFlag(fs)
	fs.StringVar(&rule, "rule", rule, "filter by rule id")
	setFlagUsageSub(fs, "usage: toktop suggestions [flags]",
		[]subcmdDoc{{"recompute", "rerun the rule engine, then list"}},
		"List rule-engine suggestions with severity, scope, and recommendation.")
	// `recompute` keyword works regardless of where flags sit.
	if _, rest, firstPos, found := firstLeafSubcommand(args, valueFlagSet(fs), "recompute"); found {
		recompute = true
		args = rest
	} else if firstPos != "" {
		cliErrf(stderr, "unknown suggestions subcommand %q (want recompute, or flags to list)", firstPos)
		return 2
	}
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateListFormat(format); err != nil {
		cliErr(stderr, err)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	if recompute {
		if _, err := svc.RecomputeSuggestions(ctx, time.Now().UTC()); err != nil {
			cliErrf(stderr, "recompute: %v", err)
			return 1
		}
	}
	sugs, err := svc.Suggestions(ctx, rule)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	// confidence surfaces provenance (observed vs estimated/inferred) in the default
	// table/csv views, not just json — a synthesized finding must never read as
	// authoritative.
	return emitList(*output, stdout, stderr, format, *columns, sugs, []string{"id", "rule_id", "severity", "confidence", "scope_kind", "scope_id", "recommendation"}, func(item trace.Suggestion) []string {
		return []string{strconv.FormatInt(item.ID, 10), item.RuleID, item.Severity, string(item.Confidence), item.ScopeKind, item.ScopeID, item.Recommendation}
	})
}

// retentionStatusFlagSet defines the `data retention status` flags, binding
// --profile and --format. runRetention derives its dispatch value-flag set
// from it, so keep every status flag here — a flag defined elsewhere would be
// invisible to the dispatcher.
func retentionStatusFlagSet(profile, format *string) *flag.FlagSet {
	fs := flag.NewFlagSet("data retention status", flag.ContinueOnError)
	fs.StringVar(profile, "profile", *profile, "privacy|balanced|archive")
	fs.StringVar(format, "format", *format, formatSingleEntityUsage)
	setFlagUsage(fs, "usage: toktop data retention status [--profile <p>] [--format table|json]",
		"Show the effective retention windows for a profile (does not delete anything).")
	return fs
}

func runRetention(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if printUsageForHelp(args, stdout, "usage: toktop data retention <status|profiles> [flags]") {
		return 0
	}
	// Subcommand keyword works regardless of where flags sit; the subcommand is
	// always explicit (no default — a bare invocation is a usage error). The
	// dispatch value-flag set is derived from the real status flag set so the
	// two cannot drift apart.
	sub, rest, firstPos, found := firstLeafSubcommand(args, valueFlagSet(retentionStatusFlagSet(new(string), new(string))), "status", "profiles")
	if !found {
		if firstPos != "" {
			cliErrf(stderr, "unknown retention subcommand %q (want status|profiles)", firstPos)
			return 2
		}
		return printUsage(stderr, "usage: toktop data retention <status|profiles> [flags]")
	}
	if sub == "profiles" {
		if printUsageForHelp(rest, stdout, "usage: toktop data retention profiles") {
			return 0
		}
		if len(rest) > 0 {
			cliErrf(stderr, "unexpected argument %q", rest[0])
			return 2
		}
		fmt.Fprint(stdout, retention.FormatProfileList())
		return 0
	}
	profile := "balanced"
	format := "table"
	fs := retentionStatusFlagSet(&profile, &format)
	fs.SetOutput(stderr)
	if code := parseFlagsNoPositionals(fs, rest, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateFormat(format, "table", "json"); err != nil {
		cliErr(stderr, err)
		return 2
	}
	policy, err := retention.PolicyFor(retention.Profile(profile))
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if format == "json" {
		return writeJSON(stdout, stderr, map[string]string{
			"profile":      string(policy.Profile),
			"raw":          textutil.FormatDuration(policy.RawAge),
			"redact_after": textutil.FormatDuration(policy.RedactRawAfter),
		})
	}
	fmt.Fprintf(stdout, "profile: %s\nraw: %s\nredact_after: %s\n",
		policy.Profile, textutil.FormatDuration(policy.RawAge), textutil.FormatDuration(policy.RedactRawAfter))
	return 0
}

// runProfilePrune applies a retention profile. It prefers the daemon (the sole
// owner of the event log; a second process can't take the exclusive bbolt lock),
// falling back to a local sqlite-only prune when no daemon is up. Shared by the
// consolidated `data prune --profile` path.
func runProfilePrune(ctx context.Context, home, profile string, dryRun bool, token string, noAuth bool, stdout, stderr io.Writer) int {
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	addr := clientAddr(loader.Current())
	policy, err := retention.PolicyFor(retention.Profile(profile))
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	switch report, err := pruneRetentionViaServer(ctx, addr, clientToken(token, noAuth), profile, dryRun); {
	case err == nil:
		printRetentionReport(stdout, report, true)
		return 0
	case !errors.Is(err, errStreamServerUnreachable):
		cliErr(stderr, err)
		return 1
	default:
		fmt.Fprintf(stderr, "toktop: no live server at %s; running a local prune (sqlite only; event-log pruning is left to the daemon)\n", addr)
	}
	store, err := openStore(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	report, err := retention.Apply(ctx, store, nil, policy, time.Now().UTC(), dryRun)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	printRetentionReport(stdout, report, false)
	return 0
}

// printRetentionReport renders a retention prune report. eventLogHandled is
// false for a local (sqlite-only) prune, where the event log was deliberately
// not touched, so its line says "skipped" rather than a misleading 0.
func printRetentionReport(w io.Writer, report retention.Report, eventLogHandled bool) {
	normalized := strconv.FormatInt(report.NormalizedRowsRedacted, 10)
	eventLog := strconv.Itoa(report.EventLogPruned)
	if report.DryRun {
		// RedactNormalized and the event-log prune have no count-only path, so
		// they are skipped in dry-run; report n/a instead of a misleading 0 that
		// reads as "nothing would change".
		normalized = "n/a (dry-run)"
		eventLog = "n/a (dry-run)"
	}
	if !eventLogHandled {
		eventLog = "skipped (no daemon)"
	}
	fmt.Fprintf(w, "profile: %s dry_run=%v\nraw_events: %d\nnormalized_rows_redacted: %s\nevent_log_pruned: %s\n",
		report.Profile, report.DryRun, report.RawEventsAffected, normalized, eventLog)
}

func runData(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if printUsageForHelp(args, stdout, "usage: toktop data <prune|retention> [flags]") {
		return 0
	}
	sub, rest, firstPos, found := firstLeafSubcommand(args, dataDispatchValueFlags(), "prune", "retention")
	if !found {
		if firstPos != "" {
			cliErrf(stderr, "unknown data subcommand %q (want prune|retention)", firstPos)
			return 2
		}
		return printUsage(stderr, "usage: toktop data <prune|retention> [flags]")
	}
	switch sub {
	case "prune":
		return runPrune(ctx, rest, stdout, stderr)
	case "retention":
		return runRetention(ctx, rest, stdout, stderr)
	}
	return 2
}

// dataDispatchValueFlags derives runData's dispatch value-flag set from the real
// leaf flag sets — prune ∪ retention status — so a flag renamed on either cannot
// drift from the dispatcher (the convention the retention/hooks dispatches use).
func dataDispatchValueFlags() map[string]bool {
	flags := valueFlagSet(pruneFlagSet(new(string), new(string), new(string), new(bool), new(bool)))
	maps.Copy(flags, valueFlagSet(retentionStatusFlagSet(new(string), new(string))))
	return flags
}

func runDB(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: toktop db <stats|path|optimize|reindex|checkpoint> [flags]"
	if printUsageForHelp(args, stdout, usage) {
		return 0
	}
	// Subcommand keyword works regardless of where flags sit (e.g. `db --format
	// json stats`); the subcommand is always explicit (no default — a bare
	// invocation is a usage error).
	sub, rest, firstPos, found := firstLeafSubcommand(args, dbDispatchValueFlags(), "stats", "path", "optimize", "reindex", "checkpoint")
	if !found {
		if firstPos != "" {
			cliErrf(stderr, "unknown db subcommand %q (want stats|path|optimize|reindex|checkpoint)", firstPos)
			return 2
		}
		return printUsage(stderr, usage)
	}
	switch sub {
	case "stats":
		return runDBStats(ctx, rest, stdout, stderr)
	case "path":
		fs := flag.NewFlagSet("db path", flag.ContinueOnError)
		fs.SetOutput(stderr)
		setFlagUsage(fs, "usage: toktop db path", "Print the SQLite database path.")
		if code := parseFlagsNoPositionals(fs, rest, stdout, stderr); code >= 0 {
			return code
		}
		dataDir, err := paths.DataDir()
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, sqlite.DBPath(dataDir))
		return 0
	case "optimize":
		return runDBOptimize(ctx, rest, stdout, stderr)
	case "reindex":
		return runDBReindex(ctx, rest, stdout, stderr)
	case "checkpoint":
		return runDBCheckpoint(ctx, rest, stdout, stderr)
	}
	return 2
}

// dbDispatchValueFlags derives runDB's dispatch value-flag set by unioning the
// db subcommand flag sets that introduce value flags (stats: --format;
// checkpoint: --format, --mode). optimize reuses --format; path and reindex take
// none. Deriving from the real flag sets keeps the dispatcher from drifting when
// a flag is renamed — any db subcommand that adds a *new* value flag must be
// represented here.
func dbDispatchValueFlags() map[string]bool {
	flags := valueFlagSet(dbStatsFlagSet(new(string)))
	maps.Copy(flags, valueFlagSet(dbCheckpointFlagSet(new(string), new(string))))
	return flags
}

type dbTableCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type dbStatsReport struct {
	Path          string         `json:"path"`
	SizeBytes     int64          `json:"size_bytes"`
	WALSizeBytes  int64          `json:"wal_size_bytes"`
	SHMSizeBytes  int64          `json:"shm_size_bytes"`
	SearchFTSRows int64          `json:"search_fts_rows"`
	Tables        []dbTableCount `json:"tables"`
}

// dbStatsFlagSet defines the `db stats` flags, binding --format to *format.
// runDB derives its dispatch value-flag set from it, so keep every db-stats
// flag here — a flag defined elsewhere would be invisible to the dispatcher.
func dbStatsFlagSet(format *string) *flag.FlagSet {
	fs := flag.NewFlagSet("db stats", flag.ContinueOnError)
	fs.StringVar(format, "format", *format, formatSingleEntityUsage)
	setFlagUsage(fs, "usage: toktop db stats [--format table|json]", "Show the DB file path, sidecar sizes, FTS rows, and per-table row counts.")
	return fs
}

func runDBStats(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	fs := dbStatsFlagSet(&format)
	fs.SetOutput(stderr)
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateFormat(format, "table", "json"); err != nil {
		cliErr(stderr, err)
		return 2
	}
	dbPath := sqlite.DBPath(paths.DataDirUnder(home))
	info, err := os.Stat(dbPath)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	store, err := openStore(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	report := dbStatsReport{
		Path:         dbPath,
		SizeBytes:    info.Size(),
		WALSizeBytes: sidecarSize(dbPath + "-wal"),
		SHMSizeBytes: sidecarSize(dbPath + "-shm"),
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts`).Scan(&report.SearchFTSRows); err != nil {
		cliErrf(stderr, "count search_fts: %v", err)
		return 1
	}
	// Enumerate tables from the live schema so new tables show up here without a
	// hand-maintained list (which had drifted). Excluded: SQLite internals, the
	// search_fts virtual table and its shadow tables (search_documents carries
	// the searchable content), and goose's migration-bookkeeping table.
	tables, err := listTables(ctx, store)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	for _, tbl := range tables {
		var n int64
		if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+tbl+`"`).Scan(&n); err != nil {
			cliErrf(stderr, "count %s: %v", tbl, err)
			return 1
		}
		report.Tables = append(report.Tables, dbTableCount{Name: tbl, Count: n})
	}
	if format == "json" {
		return writeJSON(stdout, stderr, report)
	}
	fmt.Fprintf(stdout, "path: %s\nsize: %d bytes (%.2f MiB)\n", report.Path, report.SizeBytes, float64(report.SizeBytes)/1024/1024)
	fmt.Fprintf(stdout, "wal: %d bytes (%.2f MiB)\n", report.WALSizeBytes, float64(report.WALSizeBytes)/1024/1024)
	fmt.Fprintf(stdout, "shm: %d bytes (%.2f MiB)\n", report.SHMSizeBytes, float64(report.SHMSizeBytes)/1024/1024)
	fmt.Fprintf(stdout, "search_fts rows: %d\n", report.SearchFTSRows)
	for _, c := range report.Tables {
		fmt.Fprintf(stdout, "%-18s %d\n", c.Name, c.Count)
	}
	return 0
}

func runDBOptimize(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	fs := flag.NewFlagSet("db optimize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatSingleEntityUsage)
	setFlagUsage(fs, "usage: toktop db optimize [--format table|json]", "Run SQLite/FTS maintenance without rebuilding the projection.")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateFormat(format, "table", "json"); err != nil {
		cliErr(stderr, err)
		return 2
	}
	store, err := openStore(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	result, err := store.Optimize(ctx)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if format == "json" {
		return writeJSON(stdout, stderr, result)
	}
	fmt.Fprintf(stdout, "checkpoint: busy=%t log=%d checkpointed=%d\n",
		result.Checkpoint.Busy,
		result.Checkpoint.LogFrames,
		result.Checkpoint.CheckpointedFrames)
	fmt.Fprintf(stdout, "search_fts optimized: %t\n", result.FTSOptimized)
	return 0
}

func runDBReindex(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	fs := flag.NewFlagSet("db reindex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setFlagUsage(fs, "usage: toktop db reindex", "Rebuild the FTS search index from search_documents.")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	store, err := openStore(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	if err := store.RebuildSearchIndex(ctx); err != nil {
		cliErr(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "search_fts reindexed")
	return 0
}

// dbCheckpointFlagSet defines the `db checkpoint` flags, binding --format and
// --mode. runDB derives part of its dispatch value-flag set from it, so keep
// every db-checkpoint flag here.
func dbCheckpointFlagSet(format, mode *string) *flag.FlagSet {
	fs := flag.NewFlagSet("db checkpoint", flag.ContinueOnError)
	fs.StringVar(format, "format", *format, formatSingleEntityUsage)
	fs.StringVar(mode, "mode", *mode, "checkpoint mode: passive, full, restart, or truncate")
	setFlagUsage(fs, "usage: toktop db checkpoint [--mode passive|full|restart|truncate] [--format table|json]", "Run a SQLite WAL checkpoint.")
	return fs
}

func runDBCheckpoint(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	mode := "passive"
	fs := dbCheckpointFlagSet(&format, &mode)
	fs.SetOutput(stderr)
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if err := validateFormat(format, "table", "json"); err != nil {
		cliErr(stderr, err)
		return 2
	}
	normalizedMode, err := sqlite.NormalizeCheckpointMode(mode)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	store, err := openStore(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	result, err := store.Checkpoint(ctx, normalizedMode)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if format == "json" {
		if writeJSON(stdout, stderr, result) != 0 {
			return 1
		}
		if result.Busy {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "checkpoint: mode=%s busy=%t log=%d checkpointed=%d\n",
		strings.ToLower(normalizedMode),
		result.Busy,
		result.LogFrames,
		result.CheckpointedFrames)
	if result.Busy {
		fmt.Fprintln(stderr, "warning: checkpoint blocked by a concurrent reader/writer; WAL not reclaimed")
		return 1
	}
	return 0
}

func sidecarSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// listTables returns the user tables in the open database, excluding SQLite
// internals, the FTS5 virtual table and its shadow tables, and goose's
// migration bookkeeping.
func listTables(ctx context.Context, store *sqlite.Store) ([]string, error) {
	rows, err := store.DB().QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE 'search_fts%' AND name != 'goose_db_version'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func resolveHome(stderr io.Writer) (string, bool) {
	dir, err := paths.Home()
	if err != nil {
		cliErrf(stderr, "resolve home dir: %v", err)
		return "", false
	}
	return dir, true
}

func openService(ctx context.Context, home string) (*query.Service, *sqlite.Store, error) {
	store, err := openStore(ctx, home)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	return query.New(store), store, nil
}

func parseFilterFlags(since, until, sort string, allowedSorts ...string) (sqlite.Filter, error) {
	filter := sqlite.Filter{}
	now := time.Now().UTC()
	if since != "" {
		t, err := query.ParseSince(since, now)
		if err != nil {
			return sqlite.Filter{}, err
		}
		filter.Since = t
	}
	if until != "" {
		t, err := query.ParseSince(until, now)
		if err != nil {
			return sqlite.Filter{}, err
		}
		filter.Until = t
	}
	if sort != "" {
		filter.SortBy, filter.SortDesc = query.ParseSort(sort)
		// Validate against the command's sortable columns when supplied: an unknown
		// --sort would otherwise be silently ignored (wrong order, exit 0). Mirrors
		// the --since/--sources contract — bad value => error => caller exit 2.
		if len(allowedSorts) > 0 && !slices.Contains(allowedSorts, filter.SortBy) {
			return sqlite.Filter{}, fmt.Errorf("invalid --sort %q (want one of %s, each with optional _asc/_desc)", sort, strings.Join(allowedSorts, ", "))
		}
	}
	return filter, nil
}

// validateStatuses rejects any --status token outside the canonical set, sharing
// query.ValidateStatuses with the HTTP filter builder so the check and message stay
// identical across surfaces.
func validateStatuses(statuses rootList) error {
	return query.ValidateStatuses(splitFlagValues(statuses))
}

// checkPaging validates the shared --limit/--offset bounds for paginated listings:
// a page of <1 row is meaningless and the offset must be non-negative. Returns 2
// after reporting (caller returns it), else -1 to continue — mirrors parseFlags.
func checkPaging(limit, offset int, stderr io.Writer) int {
	if limit < 1 || offset < 0 {
		cliErrf(stderr, "--limit must be >= 1 and --offset >= 0")
		return 2
	}
	return -1
}

// applyMultiFilter populates the shared list filters. Source tokens are folded
// and validated, and status tokens are checked against the canonical set (unknown
// name => error, callers exit 2); project/session are opaque ids passed through.
func applyMultiFilter(filter *sqlite.Filter, sources, projects, sessions, statuses rootList, includeSubagents bool) error {
	ids, err := resolveFilterTokens(sources)
	if err != nil {
		return err
	}
	if err := validateStatuses(statuses); err != nil {
		return err
	}
	filter.SourceIDs = append(filter.SourceIDs, ids...)
	filter.ProjectIDs = append(filter.ProjectIDs, splitFlagValues(projects)...)
	filter.SessionIDs = append(filter.SessionIDs, splitFlagValues(sessions)...)
	filter.Statuses = append(filter.Statuses, splitFlagValues(statuses)...)
	filter.IncludeSubagents = includeSubagents
	return nil
}

// subagentsFlagUsage is the one description every list/stats command gives the
// --subagents toggle, so the help text never drifts between commands.
const subagentsFlagUsage = "include subagent transcripts (sub-runs spawned by Task/Agent/Workflow); excluded by default"

// addSubagentsFlag registers the shared --subagents toggle on a command's flag set
// and returns the bound value, mirroring how each command registers --sources etc.
func addSubagentsFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("subagents", false, subagentsFlagUsage)
}

// Canonical help for the shared --sources/--project/--session/--status filter
// quartet, so the text never drifts between commands.
const (
	sourcesFlagUsage = "provider filter such as claude-code or codex; may be repeated or comma-separated"
	projectFlagUsage = "project id filter; may be repeated or comma-separated"
	sessionFlagUsage = "session id or external session id filter; may be repeated or comma-separated"
	statusFlagUsage  = "status filter; may be repeated or comma-separated"
)

// defaultPageLimit is the shared default --limit for every paginated listing.
const defaultPageLimit = 20

// addFilterFlags registers the shared --sources/--project/--session/--status
// quartet with canonical help. The bound rootLists are passed in (not returned) so
// a dropped argument is a compile error. Mirrors addSubagentsFlag.
func addFilterFlags(fs *flag.FlagSet, sources, projects, sessions, statuses *rootList) {
	fs.Var(sources, "sources", sourcesFlagUsage)
	fs.Var(projects, "project", projectFlagUsage)
	fs.Var(sessions, "session", sessionFlagUsage)
	fs.Var(statuses, "status", statusFlagUsage)
}

// addLimitOffsetFlags registers the shared --limit/--offset paging flags with
// canonical help, so every paginated command pages identically.
func addLimitOffsetFlags(fs *flag.FlagSet, limit, offset *int) {
	fs.IntVar(limit, "limit", *limit, "maximum rows per page")
	fs.IntVar(offset, "offset", *offset, "rows to skip (page past --limit)")
}

// splitFlagValues flattens repeat/comma-joined flag values into a deduped token
// list. resolveTokens (sourceflag.go) layers alias-fold + a second dedup on top.
func splitFlagValues(values rootList) []string {
	var parts []string
	for _, value := range values {
		parts = append(parts, textutil.SplitTrim(value)...)
	}
	return textutil.DedupNonEmpty(parts)
}
