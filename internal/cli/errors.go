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
