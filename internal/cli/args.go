package cli

import (
	"flag"
	"slices"
	"strings"
)

// Argument-order helpers shared by the subcommand runners. They let flags appear
// anywhere relative to positionals (Go's flag.Parse stops at the first non-flag
// arg) and let a parent command dispatch a leaf subcommand without disturbing the
// leaf's own flags.

// extractSubcommand removes the first bare-word positional token matching name
// (a subcommand such as "unused") from anywhere in args so that flag ordering
// does not change behavior. valueFlags identifies flags that consume a following
// value, so a flag value equal to name (e.g. `--sources unused`) is skipped
// rather than mistaken for the subcommand. It returns the remaining args and
// whether the token was present.
func extractSubcommand(args []string, valueFlags map[string]bool, name string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !found && strings.HasPrefix(a, "-") && a != "-" && a != "--" {
			out = append(out, a)
			flagName := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && valueFlags[flagName] && i+1 < len(args) {
				i++
				out = append(out, args[i]) // a value-flag's value is never the subcommand
			}
			continue
		}
		if !found && a == name {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

func partitionArgs(args []string, valueFlags map[string]bool) (flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && valueFlags[name] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return flagArgs, positional
}

// valueFlagSet reports, per a defined flag set, which flags consume a following
// value (i.e. are not boolean). It lets the arg-order helpers skip "--flag value"
// pairs correctly without a hand-maintained list.
func valueFlagSet(fs *flag.FlagSet) map[string]bool {
	value := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			return
		}
		value[f.Name] = true
	})
	return value
}

// reorderArgs rewrites args so flags are honored no matter where they sit
// relative to positionals. Go's flag.Parse stops at the first non-flag argument,
// so `cmd query --home X` would otherwise silently drop --home (and fall back to
// the default home). Flags move to the front; positionals follow a "--" guard so
// a positional that looks like a flag is still treated as positional.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	flagArgs, positional := partitionArgs(args, valueFlagSet(fs))
	if len(positional) == 0 {
		return flagArgs
	}
	return append(append(flagArgs, "--"), positional...)
}

// firstLeafSubcommand finds the first positional argument (skipping flags and
// their values per valueFlags) and, if it matches one of subs, returns that
// subcommand plus args with only that token removed — so the leaf still receives
// every flag regardless of order. found is false when there is no positional
// (list mode) or the first positional is not a known subcommand (firstPos names
// it so the caller can report "unknown subcommand").
func firstLeafSubcommand(args []string, valueFlags map[string]bool, subs ...string) (sub string, rest []string, firstPos string, found bool) {
	idx, tok := firstPositional(args, valueFlags)
	if idx < 0 {
		return "", args, "", false
	}
	if !slices.Contains(subs, tok) {
		return "", args, tok, false
	}
	// Remove the subcommand token, preserving the original order of everything else,
	// so a leaf-only flag/value pair the parent doesn't know about (e.g.
	// `components <id> -kind builtin_tool`) reaches the leaf intact — the leaf's own
	// FlagSet then parses it. Also drop a `--` that only separated parent flags from
	// the subcommand, otherwise the leaf sees it and treats its own flags as
	// positionals.
	start := idx
	if start > 0 && args[start-1] == "--" {
		start--
	}
	rest = append(append([]string{}, args[:start]...), args[idx+1:]...)
	return tok, rest, tok, true
}

// firstPositional returns the index and value of the first non-flag argument,
// skipping flags and the value of each value-taking flag in valueFlags (the same
// skipping rule partitionArgs uses). Returns -1 when there is no positional.
func firstPositional(args []string, valueFlags map[string]bool) (int, string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return i + 1, args[i+1]
			}
			return -1, ""
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && valueFlags[name] && i+1 < len(args) {
				i++ // skip this flag's value
			}
			continue
		}
		return i, a
	}
	return -1, ""
}
