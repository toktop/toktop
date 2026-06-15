package cli

import (
	"fmt"
	"io"
)

func cliErr(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "toktop: %v\n", err)
}

func cliErrf(stderr io.Writer, format string, args ...any) {
	fmt.Fprintf(stderr, "toktop: "+format+"\n", args...)
}

// printUsage writes a usage line to stderr and returns the usage exit code (2),
// so every bad-invocation path reports identically.
func printUsage(stderr io.Writer, usage string) int {
	fmt.Fprintln(stderr, usage)
	return 2
}
