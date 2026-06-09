package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
)

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

func hasHelpArg(args []string) bool {
	return slices.ContainsFunc(args, isHelpArg)
}

func parseFlags(fs *flag.FlagSet, args []string, stdout io.Writer) int {
	if hasHelpArg(args) {
		fs.SetOutput(stdout)
	}
	// Reorder so flags are honored wherever they appear relative to positionals;
	// stdlib flag.Parse otherwise stops at the first non-flag arg and silently
	// drops trailing flags (e.g. `search <query> --format json` ignoring --format).
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	return -1
}

func printUsageForHelp(args []string, stdout io.Writer, usage string) bool {
	if len(args) > 0 && isHelpArg(args[0]) {
		fmt.Fprintln(stdout, usage)
		return true
	}
	return false
}

// subcmdDoc documents one positional subcommand (e.g. `unused`) so --help can
// list it in its own "subcommands:" block, on par with the flag list, instead
// of burying it in a parenthetical aside on the synopsis line.
type subcmdDoc struct {
	name, desc string
}

// setFlagUsage replaces the bare "Usage of <name>:" that flag.FlagSet prints
// for --help with a synopsis line, optional body/example lines, and the flag
// list. Use it on commands whose positional arguments or workflow are not
// obvious from the flags alone.
func setFlagUsage(fs *flag.FlagSet, synopsis string, body ...string) {
	setFlagUsageSub(fs, synopsis, nil, body...)
}

// setFlagUsageSub is setFlagUsage plus a "subcommands:" block, for commands that
// dispatch a positional leaf (e.g. `skills unused`) the stdlib flag list cannot
// surface on its own.
func setFlagUsageSub(fs *flag.FlagSet, synopsis string, subs []subcmdDoc, body ...string) {
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, synopsis)
		for _, line := range body {
			fmt.Fprintln(out, line)
		}
		if len(subs) > 0 {
			width := 0
			for _, s := range subs {
				width = max(width, len(s.name))
			}
			fmt.Fprintln(out, "\nsubcommands:")
			for _, s := range subs {
				fmt.Fprintf(out, "  %-*s  %s\n", width, s.name, s.desc)
			}
		}
		fmt.Fprintln(out, "\nflags:")
		fs.PrintDefaults()
	}
}
