package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"toktop.unceas.dev/internal/handoff"
)

func runHandoff(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: toktop handoff create --session <id> [--output <dir>]"
	if printUsageForHelp(args, stdout, usage) {
		return 0
	}
	sub, rest, firstPos, found := firstLeafSubcommand(args, valueFlagSet(handoffCreateFlagSet(new(string), new(string), new(int))), "create")
	if !found {
		if firstPos != "" {
			cliErrf(stderr, "unknown handoff subcommand %q (want create)", firstPos)
			return 2
		}
		return printUsage(stderr, usage)
	}
	switch sub {
	case "create":
		return runHandoffCreate(ctx, rest, stdout, stderr)
	}
	return 2
}

// handoffCreateFlagSet defines the `handoff create` flags. runHandoff derives its
// dispatch value-flag set from it, so keep every flag here.
func handoffCreateFlagSet(session, output *string, maxOutputBytes *int) *flag.FlagSet {
	fs := flag.NewFlagSet("handoff create", flag.ContinueOnError)
	fs.StringVar(session, "session", *session, "session id or external session id to package")
	fs.StringVar(output, "output", *output, "output directory for the handoff package (default ~/.toktop/handoff/<session>)")
	fs.IntVar(maxOutputBytes, "max-output-bytes", *maxOutputBytes, "clip tool outputs / agent results larger than N bytes to head+tail (0 = full; raw pointers always reach the full bytes)")
	setFlagUsage(fs, "usage: toktop handoff create --session <id> [--output <dir>] [--max-output-bytes N]",
		"Assemble an Evidence-based Handoff Package for one session so another agent can",
		"continue the workflow without re-deriving or re-running the original agents.")
	return fs
}

func runHandoffCreate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	session := ""
	output := ""
	maxOutputBytes := 0
	fs := handoffCreateFlagSet(&session, &output, &maxOutputBytes)
	fs.SetOutput(stderr)
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if strings.TrimSpace(session) == "" {
		cliErrf(stderr, "handoff create requires --session <id>")
		return 2
	}
	if maxOutputBytes < 0 {
		cliErrf(stderr, "--max-output-bytes must be >= 0 (0 = full)")
		return 2
	}
	svc, store, err := openService(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	defer store.Close()
	matches, err := svc.FindSessions(ctx, session)
	if err != nil {
		return reportLookupErr(stderr, "session", session, err)
	}
	sess := selectSessionMatch(session, matches, stderr)
	if sess.ID == "" {
		cliErrf(stderr, "session not found: %s", session)
		return 1
	}
	// Default the package dir under the toktop home, keyed by the resolved session id,
	// so a bare `handoff create` never writes into the user's project tree (and needs
	// no .gitignore). --output overrides.
	if strings.TrimSpace(output) == "" {
		output = filepath.Join(home, "handoff", sess.ID)
	}
	turns, err := svc.SessionTurns(ctx, sess.ID)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	subagentRuns, err := svc.SubagentRuns(ctx, sess.ID)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	pkg := handoff.Build(time.Now().UTC(), sess, turns, subagentRuns, maxOutputBytes)
	// Record the same disambiguation the stderr note above reported, so the
	// written manifest.json is self-describing about an ambiguous external id.
	if len(matches) > 1 {
		pkg.Manifest.AmbiguousSessionIDs = make([]string, len(matches))
		for i, m := range matches {
			pkg.Manifest.AmbiguousSessionIDs[i] = m.ID
		}
	}
	if err := pkg.Write(output); err != nil {
		cliErr(stderr, err)
		return 1
	}
	m := pkg.Manifest
	fmt.Fprintf(stdout, "handoff written to %s\n", output)
	fmt.Fprintf(stdout, "  status=%s  turns=%d  agent_runs=%d (%d ok, %d failed, %d stopped, %d in-flight)  final_synthesis=%v\n",
		m.WorkflowStatus, m.Turns, m.AgentRuns, m.CompletedAgentRuns, m.FailedAgentRuns, m.InterruptedAgentRuns, m.IncompleteAgentRuns, m.FinalSynthesisPresent)
	return 0
}
