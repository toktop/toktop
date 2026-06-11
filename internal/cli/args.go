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

// partitionArgs splits args into flag/value pairs and positionals, using
// valueFlags to keep each value flag's argument with the flag. dangling names a
// value flag that ends args with no value; the caller must surface that as a
// parse error rather than letting flag.Parse silently consume whatever follows.
func partitionArgs(args []string, valueFlags map[string]bool) (flagArgs, positional []string, dangling string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && valueFlags[name] {
				if i+1 < len(args) {
					i++
					flagArgs = append(flagArgs, args[i])
				} else {
					dangling = name
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	return flagArgs, positional, dangling
}

// valueFlagSet reports, for every flag defined on fs, whether it consumes a
// following value (true) or is boolean (false). It lets the arg-order helpers
// skip "--flag value" pairs correctly without a hand-maintained list, and lets
// firstPositional tell known booleans apart from unknown flags.
func valueFlagSet(fs *flag.FlagSet) map[string]bool {
	value := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		bf, ok := f.Value.(interface{ IsBoolFlag() bool })
		value[f.Name] = !ok || !bf.IsBoolFlag()
	})
	return value
}

// reorderArgs rewrites args so flags are honored no matter where they sit
// relative to positionals. Go's flag.Parse stops at the first non-flag argument
// and leaves everything after it in fs.Args(), so a trailing `--format json`
// would otherwise become extra query terms for `search <query> --format json`
// (silently wrong output) or extra positionals that trip a leaf's
// positional-count check (a spurious usage error). Flags move to the front;
// positionals follow a "--" guard so a positional that looks like a flag is
// still treated as positional. When a value flag ends args with no value
// (`... --format`), the positionals are dropped instead, so flag.Parse reports
// "flag needs an argument" rather than silently taking the "--" guard as the
// value.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	flagArgs, positional, dangling := partitionArgs(args, valueFlagSet(fs))
	if dangling != "" || len(positional) == 0 {
		return flagArgs
	}
	return append(append(flagArgs, "--"), positional...)
}

// firstLeafSubcommand finds the first positional argument (skipping flags and
// their values per valueFlags) and, if it matches one of subs, returns that
// subcommand plus args with only that token removed — so the leaf still receives
// every flag regardless of order. found is false when there is no positional
// (list mode) or the first positional is not a known subcommand (firstPos names
// it so the caller can report "unknown subcommand"). When an undefined flag
// precedes a non-subcommand positional, firstPos stays empty: the positional
// may just be that flag's value, so the caller falls through to its own
// flag.Parse, whose "flag provided but not defined" error names the real
// culprit instead of blaming the value as an unknown subcommand.
func firstLeafSubcommand(args []string, valueFlags map[string]bool, subs ...string) (sub string, rest []string, firstPos string, found bool) {
	idx, tok, unknownFlag := firstPositional(args, valueFlags)
	if idx < 0 {
		return "", args, "", false
	}
	if !slices.Contains(subs, tok) {
		if unknownFlag {
			return "", args, "", false
		}
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
// skipping each flag and, for value-taking flags, its following value. A flag
// absent from valueFlags is undefined here (a typo, or a leaf-only flag the
// parent doesn't know); it is assumed to take a value, so the next token is
// skipped too and unknownFlag is set. The assumption is safe: an undefined
// flag always fails the eventual flag.Parse, so skipping one token can only
// change which accurate error is reported — it lets `db --home X stats`
// dispatch and fail on -home instead of misreporting X as an unknown
// subcommand, and lets a leaf-only pair like `--kind skill` before the keyword
// ride through to the leaf. Returns -1 when there is no positional.
func firstPositional(args []string, valueFlags map[string]bool) (idx int, tok string, unknownFlag bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return i + 1, args[i+1], unknownFlag
			}
			return -1, "", unknownFlag
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if isHelpArg(a) {
				continue // -h/--help is a universal boolean; it never consumes a token
			}
			name := strings.TrimLeft(a, "-")
			takesValue, known := valueFlags[name]
			if !known {
				unknownFlag = true
			}
			if !strings.Contains(a, "=") && (takesValue || !known) && i+1 < len(args) {
				i++ // skip this flag's value
			}
			continue
		}
		return i, a, unknownFlag
	}
	return -1, "", unknownFlag
}
