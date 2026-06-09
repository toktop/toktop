package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"toktop.unceas.dev/internal/httpapi"
	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// Read/query commands: the list + single-entity views over the trace store
// (sessions, turns, summary, search, export). Their shared helpers (parseFlags,
// writeFormatted, loadIndex, the detail renderers, …) live in cli.go / detail.go.

func runSessions(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	limit := 20
	format := "table"
	since := ""
	until := ""
	sortFlag := "started_desc"
	var sources, projects, sessionsFilter, statuses rootList
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offset := 0
	fs.IntVar(&limit, "limit", limit, "maximum sessions per page")
	fs.IntVar(&offset, "offset", offset, "rows to skip (page past --limit)")
	fs.StringVar(&format, "format", format, "output format: table|json|ndjson|csv")
	fs.Var(&sources, "sources", "provider filter such as claude-code or codex; may be repeated or comma-separated")
	fs.Var(&projects, "project", "project id filter; may be repeated or comma-separated")
	fs.Var(&sessionsFilter, "session", "session id or external session id filter; may be repeated or comma-separated")
	fs.Var(&statuses, "status", "status filter; may be repeated or comma-separated")
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&sortFlag, "sort", sortFlag, "started_desc|started_asc|turns_desc")
	setFlagUsage(fs, "usage: toktop sessions [flags]   (sessions inspect <id> for one session)", "List sessions, most-recent first; page with --limit/--offset.")
	// Dispatch `sessions inspect <id>` regardless of where flags sit.
	if _, rest, firstPos, ok := firstLeafSubcommand(args, valueFlagSet(fs), "inspect"); ok {
		return runSession(ctx, rest, stdout, stderr)
	} else if firstPos != "" {
		cliErrf(stderr, "unknown sessions subcommand %q (want inspect, or flags to list)", firstPos)
		return 2
	}
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if code := checkPaging(limit, offset, stderr); code >= 0 {
		return code
	}

	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	filter, err := parseFilterFlags(since, until, sortFlag, "started", "turns")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter.Limit = limit
	filter.Offset = offset
	if err := applyMultiFilter(&filter, sources, projects, sessionsFilter, statuses); err != nil {
		cliErr(stderr, err)
		return 2
	}
	page, err := svc.ListSessions(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return writeFormatted(stdout, stderr, format, page.Items, []string{"id", "external", "provider", "status", "turns", "tools", "tokens", "project", "started"}, func(session trace.Session) []string {
		return []string{session.ID, emptyDash(session.ExternalID), session.Provider, session.Status,
			strconv.Itoa(session.TurnCount), strconv.Itoa(session.ToolCallCount),
			textutil.FormatCount(session.Tokens.Input + session.Tokens.Output), session.ProjectName, formatTime(session.StartedAt)}
	})
}

func runSession(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	format := "table"
	fs := flag.NewFlagSet("sessions inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: table or json")
	setFlagUsage(fs, "usage: toktop sessions inspect [flags] <session_id>", "Show one session (turns, tools, skills) by internal or external id.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: toktop sessions inspect [flags] <session_id>")
		return 2
	}
	sessionID := fs.Arg(0)

	index, err := loadIndex(ctx, home)
	if err != nil {
		cliErrf(stderr, "load index: %v", err)
		return 1
	}
	session, ok := findSession(index, sessionID)
	if !ok {
		cliErrf(stderr, "session not found: %s", sessionID)
		return 1
	}
	// A provider UUID can back several internal sessions (e.g. a resumed/branched
	// session). findSession picks one; surface the ambiguity instead of hiding it.
	if dupes := sessionsWithExternalID(index, sessionID); len(dupes) > 1 {
		shown, more := dupes, ""
		if len(shown) > 6 {
			more = fmt.Sprintf(" +%d more", len(shown)-6)
			shown = shown[:6]
		}
		fmt.Fprintf(stderr, "note: external id %s maps to %d sessions (%s%s); showing %s — use an internal session id to disambiguate\n",
			sessionID, len(dupes), strings.Join(shown, ", "), more, session.ID)
	}
	detail := sessionDetail{
		Session: session,
		Turns:   turnsForSession(index, session.ID),
	}
	if format == "json" {
		return writeJSON(stdout, stderr, detail)
	}
	printSessionDetail(stdout, detail)
	return 0
}

func runTurns(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	limit := 20
	format := "table"
	since := ""
	until := ""
	sortFlag := "started_desc"
	var sources, projects, sessionsFilter, statuses rootList
	fs := flag.NewFlagSet("turns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offset := 0
	fs.IntVar(&limit, "limit", limit, "maximum turns per page")
	fs.IntVar(&offset, "offset", offset, "rows to skip (page past --limit)")
	fs.StringVar(&format, "format", format, "output format: table|json|ndjson|csv")
	fs.Var(&sources, "sources", "provider filter such as claude-code or codex; may be repeated or comma-separated")
	fs.Var(&projects, "project", "project id filter; may be repeated or comma-separated")
	fs.Var(&sessionsFilter, "session", "session id or external session id filter; may be repeated or comma-separated")
	fs.Var(&statuses, "status", "status filter; may be repeated or comma-separated")
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&sortFlag, "sort", sortFlag, "started_desc|started_asc|tokens_desc|duration_desc")
	setFlagUsage(fs, "usage: toktop turns [flags]   (turns inspect|timeline|components <id> for one)", "List turns, most-recent first; page with --limit/--offset.")
	// Dispatch leaf subcommands regardless of where flags sit (e.g.
	// `turns --home X inspect ID`); the keyword is the first positional.
	if sub, rest, firstPos, ok := firstLeafSubcommand(args, valueFlagSet(fs), "inspect", "timeline", "components"); ok {
		switch sub {
		case "inspect":
			return runShow(ctx, rest, stdout, stderr)
		case "timeline":
			return runTurnTimeline(ctx, rest, stdout, stderr)
		case "components":
			return runTurnComponents(ctx, rest, stdout, stderr)
		}
	} else if firstPos != "" {
		cliErrf(stderr, "unknown turns subcommand %q (want inspect|timeline|components, or flags to list)", firstPos)
		return 2
	}
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if code := checkPaging(limit, offset, stderr); code >= 0 {
		return code
	}

	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	filter, err := parseFilterFlags(since, until, sortFlag, "started", "tokens", "duration")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	filter.Limit = limit
	filter.Offset = offset
	if err := applyMultiFilter(&filter, sources, projects, sessionsFilter, statuses); err != nil {
		cliErr(stderr, err)
		return 2
	}
	page, err := svc.ListTurns(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	return writeFormatted(stdout, stderr, format, page.Items, []string{"id", "provider", "session_id", "status", "tools", "tokens", "project", "started", "user"}, func(turn trace.Turn) []string {
		return []string{turn.ID, turn.Provider, turn.SessionID, turn.Status,
			strconv.Itoa(turn.ToolCallCount), textutil.FormatCount(turn.Tokens.Input + turn.Tokens.Output),
			turn.ProjectName, formatTime(turn.StartedAt), oneLine(turn.UserMessage, 80)}
	})
}

func runShow(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	format := "table"
	fs := flag.NewFlagSet("turns inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: table or json")
	setFlagUsage(fs, "usage: toktop turns inspect [flags] <turn_id>", "Show one turn (user/assistant text, tools, skills) by id.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: toktop turns inspect [flags] <turn_id>")
		return 2
	}
	turnID := fs.Arg(0)

	index, err := loadIndex(ctx, home)
	if err != nil {
		cliErrf(stderr, "load index: %v", err)
		return 1
	}
	turn, ok := findTurn(index, turnID)
	if !ok {
		cliErrf(stderr, "turn not found: %s", turnID)
		return 1
	}
	if format == "json" {
		return writeJSON(stdout, stderr, turn)
	}

	fmt.Fprintf(stdout, "Turn %s\n", turn.ID)
	fmt.Fprintf(stdout, "Project: %s\n", turn.ProjectName)
	fmt.Fprintf(stdout, "Status: %s\n", turn.Status)
	fmt.Fprintf(stdout, "Tokens: input %s  output %s  cache read %s  cache write %s\n",
		textutil.FormatCount(turn.Tokens.Input), textutil.FormatCount(turn.Tokens.Output),
		textutil.FormatCount(turn.Tokens.CacheRead), textutil.FormatCount(turn.Tokens.CacheWrite))
	fmt.Fprintf(stdout, "Transcript: %s\n", turn.TranscriptPath)
	fmt.Fprintf(stdout, "User: %s\n", turn.UserMessage)
	if turn.AssistantFinal != "" {
		fmt.Fprintf(stdout, "Assistant: %s\n", turn.AssistantFinal)
	}
	printComponentDetails(stdout, turn)
	return 0
}

func runTurnComponents(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	kind := ""
	fs := flag.NewFlagSet("turns components", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: table|json|ndjson|csv")
	fs.StringVar(&kind, "kind", kind, "filter by kind: builtin_tool|mcp_server|mcp_tool|skill")
	setFlagUsage(fs, "usage: toktop turns components [flags] <turn_id>", "List the components (tools, skills) attributed to one turn.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: toktop turns components [flags] <turn_id>")
		return 2
	}
	turnID := fs.Arg(0)
	switch kind {
	case "", trace.ComponentKindBuiltinTool, trace.ComponentKindMCPServer, trace.ComponentKindMCPTool, trace.ComponentKindSkill:
	default:
		cliErrf(stderr, "unknown --kind %q (want builtin_tool|mcp_server|mcp_tool|skill)", kind)
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	// Report a missing turn instead of a silent empty list, matching the
	// `turns inspect` / `turns timeline` behavior.
	if _, err := svc.GetTurn(ctx, turnID); err != nil {
		cliErrf(stderr, "turn not found: %s", turnID)
		return 1
	}
	components, err := svc.ListComponents(ctx, turnID)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if kind != "" {
		filtered := components[:0]
		for _, c := range components {
			if c.ComponentKind == kind {
				filtered = append(filtered, c)
			}
		}
		components = filtered
	}
	return writeFormatted(stdout, stderr, format, components, []string{"kind", "name", "relation", "confidence"}, func(c trace.TurnComponent) []string {
		return []string{c.ComponentKind, c.ComponentName, c.Relation, string(c.Confidence)}
	})
}

func runTurnTimeline(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	fs := flag.NewFlagSet("turns timeline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: table|json|ndjson|csv")
	setFlagUsage(fs, "usage: toktop turns timeline [flags] <turn_id>", "Show the chronological event timeline (prompt, invocations, tool calls) for one turn.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: toktop turns timeline [flags] <turn_id>")
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	turn, err := svc.GetTurn(ctx, fs.Arg(0))
	if err != nil {
		cliErrf(stderr, "turn not found: %s", fs.Arg(0))
		return 1
	}
	timeline := httpapi.BuildTimeline(turn)
	if format == "json" {
		return writeJSON(stdout, stderr, timeline)
	}
	return writeFormatted(stdout, stderr, format, timeline.Entries, []string{"at", "kind", "label", "status", "duration_ms", "tokens", "detail"}, func(e httpapi.TimelineEntry) []string {
		tokens := textutil.FormatCount(e.Tokens)
		if e.Tokens == 0 && e.TokenEstimate > 0 {
			tokens = "~" + textutil.FormatCount(e.TokenEstimate) // estimated (carries confidence in JSON)
		}
		return []string{formatTime(e.At), e.Kind, e.Label, emptyDash(e.Status), strconv.FormatInt(e.DurationMs, 10), tokens, oneLine(e.Detail, 80)}
	})
}

func runSummary(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "table"
	since := ""
	until := ""
	var sources, projects, sessionsFilter, statuses rootList
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: table or json")
	fs.Var(&sources, "sources", "provider filter such as claude-code or codex; may be repeated or comma-separated")
	fs.Var(&projects, "project", "project id filter; may be repeated or comma-separated")
	fs.Var(&sessionsFilter, "session", "session id or external session id filter; may be repeated or comma-separated")
	fs.Var(&statuses, "status", "status filter; may be repeated or comma-separated")
	fs.StringVar(&since, "since", since, "duration like 7d, 24h, or RFC3339 timestamp")
	fs.StringVar(&until, "until", until, "upper time bound: duration like 7d, 24h, or RFC3339 timestamp")
	setFlagUsage(fs, "usage: toktop summary [flags]", "Show imported trace counts (raw events, sessions, turns, invocations, tool calls) and token totals.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	filter, err := parseFilterFlags(since, until, "")
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	if err := applyMultiFilter(&filter, sources, projects, sessionsFilter, statuses); err != nil {
		cliErr(stderr, err)
		return 2
	}
	summary, err := svc.Summary(ctx, filter)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if format == "json" {
		return writeJSON(stdout, stderr, summary)
	}
	fmt.Fprintf(stdout, "raw events: %d\n", summary.RawEvents)
	fmt.Fprintf(stdout, "sessions: %d\n", summary.Sessions)
	fmt.Fprintf(stdout, "turns: %d\n", summary.Turns)
	fmt.Fprintf(stdout, "invocations: %d\n", summary.Invocations)
	fmt.Fprintf(stdout, "tool calls: %d\n", summary.ToolCalls)
	fmt.Fprintf(stdout, "input tokens: %s\n", textutil.FormatCount(summary.InputTokens))
	fmt.Fprintf(stdout, "output tokens: %s\n", textutil.FormatCount(summary.OutputTokens))
	fmt.Fprintf(stdout, "cache read tokens: %s\n", textutil.FormatCount(summary.CacheReadTokens))
	fmt.Fprintf(stdout, "cache write tokens: %s\n", textutil.FormatCount(summary.CacheWriteTokens))
	fmt.Fprintf(stdout, "parse errors: %d\n", summary.ParseErrors)
	return 0
}

func runSearch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	limit := 20
	format := "table"
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&limit, "limit", limit, "maximum results")
	fs.StringVar(&format, "format", format, "output format: table or json")
	setFlagUsage(fs, "usage: toktop search [flags] <query>", "Full-text search over turn text and tool calls (FTS5). Filter with kind:/source: tokens.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if limit < 1 {
		cliErrf(stderr, "--limit must be >= 1")
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: toktop search [flags] <query>")
		return 2
	}
	store, err := sqlite.Open(ctx, paths.DataDirUnder(home))
	if err != nil {
		cliErrf(stderr, "open store: %v", err)
		return 1
	}
	defer store.Close()
	terms, filters := splitSearchTokens(fs.Args())
	results, err := store.Search(ctx, strings.Join(terms, " "), limit, filters["kind"], filters["source"])
	if err != nil {
		cliErrf(stderr, "search: %v", err)
		return 1
	}
	if format == "json" {
		return writeJSON(stdout, stderr, results)
	}
	for _, result := range results {
		fmt.Fprintf(stdout, "%-10s %-16s %-16s %s\n", result.Kind, result.ID, emptyDash(result.TurnID), result.Snippet)
	}
	return 0
}

func splitSearchTokens(args []string) ([]string, map[string]string) {
	terms := make([]string, 0, len(args))
	filters := make(map[string]string)
	for _, arg := range args {
		if idx := strings.IndexByte(arg, ':'); idx > 0 && idx < len(arg)-1 {
			key := arg[:idx]
			val := arg[idx+1:]

			switch key {
			case "source", "kind":
				filters[key] = val
				continue
			}
		}
		terms = append(terms, arg)
	}
	return terms, filters
}

func runExport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	format := "json"
	output := "-"
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, "output format: json or ndjson")
	fs.StringVar(&output, "output", output, "output path or - for stdout")
	setFlagUsage(fs, "usage: toktop export [flags]", "Export the full trace index as json or ndjson (to stdout or --output).")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	index, err := loadIndex(ctx, home)
	if err != nil {
		cliErrf(stderr, "load index: %v", err)
		return 1
	}
	var payload []byte
	switch format {
	case "json":
		payload, err = json.MarshalIndent(index, "", "  ")
	case "ndjson":
		payload, err = marshalNDJSON(index)
	default:
		cliErrf(stderr, "unsupported export format %q", format)
		return 2
	}
	if err != nil {
		cliErrf(stderr, "marshal export: %v", err)
		return 1
	}
	payload = append(payload, '\n')
	if output == "-" {
		_, err = stdout.Write(payload)
	} else {
		err = os.WriteFile(output, payload, 0o600)
	}
	if err != nil {
		cliErrf(stderr, "write export: %v", err)
		return 1
	}
	return 0
}
