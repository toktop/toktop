package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/paths"
)

type loaderCtxKey struct{}

// withLoader threads the process-wide config loader (resolved once in Execute)
// onto ctx so commands reuse it instead of each reparsing config.json.
func withLoader(ctx context.Context, l *config.Loader) context.Context {
	return context.WithValue(ctx, loaderCtxKey{}, l)
}

// configPath is the fixed config.json location under home.
func configPath(home string) string {
	return filepath.Join(paths.ConfigDirUnder(home), "config.json")
}

// configFor returns the startup-resolved loader, or builds one for home when it
// is absent — e.g. startup resolution failed on a broken file, so a command
// like `config get` can surface the parse error itself rather than inherit a
// nil. home is always the process default (there is no --home flag), so the
// threaded loader always matches.
func configFor(ctx context.Context, home string) (*config.Loader, error) {
	if l, ok := ctx.Value(loaderCtxKey{}).(*config.Loader); ok && l != nil {
		return l, nil
	}
	return config.NewLoader(configPath(home))
}

type exitErr struct{ code int }

func (e *exitErr) Error() string { return fmt.Sprintf("exit %d", e.code) }

type runFn func(ctx context.Context, args []string, stdout, stderr io.Writer) int

func NewRootCmd(info Info) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           info.Name,
		Short:         info.Name + " is a local-first execution trace tool for AI coding agents.",
		Version:       info.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	rootCmd.SetVersionTemplate(fmt.Sprintf("%s {{.Version}}\ncommit: %s\ndate: %s\n", info.Name, info.Commit, info.Date))

	rootCmd.AddCommand(

		buildSimpleCmd("init", "Create local config and data directories", runInit),
		buildSimpleCmd("doctor", "Check local environment readiness", runDoctor),
		buildSimpleCmd("ingest", "Import Claude Code or Codex transcripts", runIngest),
		buildSimpleCmd("daemon", "Watch transcript roots and ingest changes (run | serve | stop | status | pause | resume | trigger)", runDaemon),
		buildSimpleCmd("serve", "Start the local HTTP API v1 server", runServe),

		buildSimpleCmd("summary", "Show imported trace counts", runSummary),
		buildSimpleCmd("sessions", "List sessions; `sessions inspect <id>` for one", runSessions),
		buildSimpleCmd("turns", "List turns; `turns inspect|timeline|components <id>` for one", runTurns),
		buildSimpleCmd("projects", "List projects with session / turn / tool counts", runProjects),
		buildSimpleCmd("activity", "Roll activity into time buckets (turns / tools / tokens per bucket)", runActivity),
		buildSimpleCmd("tools", "Roll up tool call usage", runTools),
		buildSimpleCmd("models", "Roll up model invocation usage", runModels),
		buildSimpleCmd("mcps", "Roll up MCP usage; `mcps unused` for declared-but-uncalled", runMCPs),
		buildSimpleCmd("skills", "Show skill usage; `skills unused` for installed-but-unused", runSkills),
		buildSimpleCmd("suggestions", "List rule-engine suggestions; `suggestions recompute` to rerun", runSuggestions),
		buildSimpleCmd("handoff", "Build an Evidence-based Handoff Package for cross-agent recovery (`handoff create`)", runHandoff),
		buildSimpleCmd("search", "Search turns and tool calls", runSearch),

		buildSimpleCmd("status", "Show current live session-state snapshot (one-shot)", runStatus),
		buildSimpleCmd("stream", "Tail the live event stream in real time", runStream),
		buildSimpleCmd("emit", "Write one live event into a running server", runEmit),
		buildSimpleCmd("ui", "Open the local web UI in your browser", runUI),

		buildSimpleCmd("data", "Data lifecycle: prune | retention", runData),
		buildSimpleCmd("export", "Export the current trace index", runExport),
		buildSimpleCmd("hooks", "Install/manage observer hooks (status | install | uninstall)", runHook),
		buildSimpleCmd("sources", "List configured providers and discovery roots", runSources),
		buildSimpleCmd("config", "Read or write configuration (get | path | set | unset)", runConfig),
		buildSimpleCmd("db", "Database utilities (stats | path | optimize | reindex | checkpoint)", runDB),
		buildSimpleCmd("uninstall", "Stop the daemon, remove observer hooks, and delete the home directory", runUninstall),
	)
	return rootCmd
}

func buildSimpleCmd(name, short string, fn runFn) *cobra.Command {
	return &cobra.Command{
		Use:                name,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := fn(cmd.Context(), args, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if code != 0 {
				return &exitErr{code: code}
			}
			return nil
		},
	}
}

func Execute(ctx context.Context, info Info) int {
	root := NewRootCmd(info)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	initSlog(os.Stderr)
	// Resolve config once at startup and thread the loader to commands (home is
	// global — there is no --home flag). Best-effort: a broken config.json just
	// leaves the default timezone here and lets each command surface the error.
	// The presentation-layer timezone (storage stays UTC) comes from the
	// "timezone" config key; an invalid value warns and falls back to UTC.
	if home, err := paths.Home(); err == nil {
		if loader, lerr := config.NewLoader(configPath(home)); lerr == nil {
			ctx = withLoader(ctx, loader)
			if e := resolveDisplayLocation(loader.Current().Timezone); e != nil {
				fmt.Fprintln(os.Stderr, e)
			}
		}
	}
	if err := root.ExecuteContext(ctx); err != nil {
		if ee, ok := errors.AsType[*exitErr](err); ok {
			return ee.code
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}
