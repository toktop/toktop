package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// `toktop uninstall` reverses an install: stop the daemon, remove the observer
// hooks toktop injected into each provider's settings, and delete the home
// directory. It deliberately does NOT delete its own binary — a running
// executable can't remove itself on every platform (Windows holds the lock), and
// that lone irreversible step is better left explicit — so it prints the exact
// removal command instead.
func runUninstall(_ context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	keepData := false
	assumeYes := false
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&keepData, "keep-data", keepData, "keep the home directory (config, data, DB); only stop the daemon and remove hooks")
	fs.BoolVar(&assumeYes, "yes", assumeYes, "skip the confirmation prompt (required for non-interactive use)")
	setFlagUsage(fs, "usage: toktop uninstall [--keep-data] [--yes]",
		"Reverse a toktop install: stop the daemon, remove the observer hooks it",
		"injected into Claude Code / Codex (user scope), and delete the home directory,",
		"then print the command to remove the binary itself (left to you — the only",
		"irreversible step toktop won't take on its own). Your Claude Code / Codex",
		"transcripts and history are never touched.",
		"",
		"Project-scope hooks (if you ran `hooks install --scope project`) are per-project;",
		"remove those with `toktop hooks uninstall --scope project` from each project first.")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}

	providers := hookCapableProviders()

	fmt.Fprintln(stdout, "Uninstalling toktop:")
	fmt.Fprintln(stdout, "  • stop the running daemon (if any)")
	if len(providers) > 0 {
		fmt.Fprintf(stdout, "  • remove toktop observer hooks (user scope) from: %s\n", strings.Join(providers, ", "))
	}
	if keepData {
		fmt.Fprintf(stdout, "  • keep the home directory %s (--keep-data)\n", home)
	} else {
		fmt.Fprintf(stdout, "  • delete the home directory %s (config, data, DB, socket)\n", home)
	}

	// Only the home-dir wipe is irreversible, so gate just that on confirmation.
	// --keep-data leaves nothing irreversible (a daemon restarts, hooks reinstall),
	// so it proceeds without a prompt.
	if !keepData && !assumeYes {
		if !confirmStdin(stdout, "\nProceed? [y/N] ") {
			fmt.Fprintln(stdout, "aborted")
			return 0
		}
	}

	// 1. Stop the daemon first: it owns the socket, lock, and pidfile under
	//    home/run, so it must exit before the home directory is removed.
	runDaemonStop(stdout, stderr)

	// 2. Remove the observer hooks while the binary still exists — a removed binary
	//    would leave dangling entries in ~/.claude.json / ~/.codex/config.toml that
	//    shell out to a missing `toktop` on every tool call.
	for _, name := range providers {
		runHookUninstall(name, "user", false, stdout, stderr)
	}

	// 3. Delete the home directory unless asked to keep it.
	if !keepData {
		if err := os.RemoveAll(home); err != nil {
			cliErrf(stderr, "remove %s: %v", home, err)
			return 1
		}
		fmt.Fprintf(stdout, "removed %s\n", home)
	}

	// 4. The binary is the only step toktop leaves to the user (see the doc comment).
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stdout, "\nToktop's state is gone. Remove the toktop binary from your PATH to finish.")
		return 0
	}
	removeCmd := "rm " + exe
	if runtime.GOOS == "windows" {
		removeCmd = "del " + exe
	}
	fmt.Fprintf(stdout, "\nToktop's state is gone. Remove the binary to finish:\n  %s\n", removeCmd)
	return 0
}

// confirmStdin prints prompt and reads a yes/no answer from stdin. A closed or
// empty stdin (piped / non-interactive) reads as "no", so an unattended
// `toktop uninstall` never deletes anything without an explicit --yes.
func confirmStdin(stdout io.Writer, prompt string) bool {
	fmt.Fprint(stdout, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
