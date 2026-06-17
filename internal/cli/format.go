package cli

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"

	"toktop.unceas.dev/internal/textutil"
)

// formatList is the single source of truth for the list commands' --format set:
// formatFlagUsage (flag help), validateListFormat (the accept gate), and
// writeFormatted's reject message all derive from it so the advertised, accepted,
// and rendered sets cannot drift apart.
const formatList = "table|json|ndjson|csv|markdown|html"

// formatFlagUsage is the --format help for every writeFormatted-backed list command.
const formatFlagUsage = "output format: " + formatList

// formatSingleEntityUsage is the --format help for single-object views (inspect /
// summary / config get / db stats), which render one object as table or json.
const formatSingleEntityUsage = "output format: table or json"

// validateFormat rejects a --format value outside `allowed` (treating "" as the
// default). For the limited table|json commands; the full list set is
// validateListFormat, and writeFormatted renders the same set.
func validateFormat(format string, allowed ...string) error {
	if format == "" || slices.Contains(allowed, format) {
		return nil
	}
	return fmt.Errorf("unknown --format %q (want %s)", format, strings.Join(allowed, " or "))
}

// validateListFormat gates the list commands' --format to the full rendered set
// (table/json/ndjson/csv/markdown/html). Kept in lockstep with writeFormatted's
// switch so every list command accepts the same formats — a command that skipped
// this check would silently accept markdown/html that another rejects.
func validateListFormat(format string) error {
	return validateFormat(format, strings.Split(formatList, "|")...)
}

// Output rendering shared by every command: format dispatch (table/json/ndjson/
// csv/markdown/html), the go-pretty terminal table, and timestamp presentation.

func writeFormatted[T any](stdout, stderr io.Writer, format string, items []T, headers []string, row func(T) []string) int {
	switch format {
	case "json":
		// A nil slice marshals to "null"; emit "[]" so JSON consumers always get an
		// array for an empty result.
		if items == nil {
			items = []T{}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(items); err != nil {
			cliErr(stderr, err)
			return 1
		}
		return 0
	case "ndjson":
		encoder := json.NewEncoder(stdout)
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				cliErr(stderr, err)
				return 1
			}
		}
		return 0
	case "csv":
		w := csv.NewWriter(stdout)
		_ = w.Write(headers)
		for _, item := range items {
			_ = w.Write(row(item))
		}
		w.Flush()
		return 0
	case "markdown":
		writeTable(stdout, headers, items, row, "markdown")
		return 0
	case "html":
		writeTable(stdout, headers, items, row, "html")
		return 0
	case "table", "":
		writeTable(stdout, headers, items, row, "table")
		return 0
	default:
		cliErr(stderr, fmt.Errorf("unknown --format %q (want %s)", format, formatList))
		return 2
	}
}

const columnsFlagUsage = "comma-separated columns to display, in order (table/csv/markdown/html only; omit for all)"

// addColumnsFlag registers the shared --columns flag (empty = all columns) and
// returns the bound value. Mirrors addOutputFlag; the comma-separated value is
// resolved + projected against the rendered headers by applyColumns inside
// emitList.
func addColumnsFlag(fs *flag.FlagSet) *string {
	return fs.String("columns", "", columnsFlagUsage)
}

// columnsRejectedForJSON reports whether --columns was given together with a JSON
// format (json/ndjson), printing the usage error if so. --columns shapes the
// rendered table; JSON output is the full typed struct (it never goes through
// headers/row, and the json tags differ from the header names), so the two don't
// compose — project JSON fields with a tool like jq instead. Both emitList and
// the commands that emit JSON before reaching emitList (turns timeline) gate on
// this, so the rule is identical everywhere.
func columnsRejectedForJSON(columns, format string, stderr io.Writer) bool {
	if columns != "" && (format == "json" || format == "ndjson") {
		cliErrf(stderr, "--columns cannot be combined with --format %s; it selects table columns — project JSON fields with a tool like jq", format)
		return true
	}
	return false
}

// applyColumns projects headers and the row func down to the --columns selection
// (comma-separated header names, in the given order — so it doubles as a column
// reorder). It runs before any --output file is opened, so an invalid selection
// fails without truncating the target. Returns code -1 to proceed; a non-negative
// code is the exit code to return (2 for a usage error). columns=="" is a no-op.
func applyColumns[T any](columns, format string, headers []string, row func(T) []string, stderr io.Writer) ([]string, func(T) []string, int) {
	if columns == "" {
		return headers, row, -1
	}
	if columnsRejectedForJSON(columns, format, stderr) {
		return nil, nil, 2
	}
	idx, err := selectColumns(columns, headers)
	if err != nil {
		cliErr(stderr, err)
		return nil, nil, 2
	}
	return projectCells(headers, idx), func(item T) []string { return projectCells(row(item), idx) }, -1
}

// selectColumns resolves a comma-separated list of header names to their column
// indexes in the requested order (so it doubles as a reorder). An unknown name
// (or an all-blank list) is a usage error that lists the available columns — the
// headers are the only place a caller learns the names.
func selectColumns(columns string, headers []string) ([]int, error) {
	var idx []int
	for _, name := range textutil.SplitTrim(columns) {
		j := slices.Index(headers, name)
		if j < 0 {
			return nil, fmt.Errorf("unknown column %q (available: %s)", name, strings.Join(headers, ", "))
		}
		idx = append(idx, j)
	}
	if len(idx) == 0 {
		return nil, errors.New("--columns lists no columns")
	}
	return idx, nil
}

// projectCells selects/reorders cells by the index slice from selectColumns.
func projectCells(cells []string, idx []int) []string {
	out := make([]string, len(idx))
	for i, j := range idx {
		out[i] = cells[j]
	}
	return out
}

const outputFlagUsage = "output path, or - for stdout"

// addOutputFlag registers the shared --output flag (default "-" = stdout) and
// returns the bound value, so every data-emitting command resolves its destination
// identically. Mirrors addSubagentsFlag; pairs with openOutput.
func addOutputFlag(fs *flag.FlagSet) *string {
	return fs.String("output", "-", outputFlagUsage)
}

// openOutput resolves an --output value to a writer: "-" (or empty) is stdout with
// a no-op Close; any other value opens that path (0o600, truncating). The single
// file-vs-stdout resolver behind every command's --output.
func openOutput(path string, stdout io.Writer) (io.WriteCloser, error) {
	if path == "-" || path == "" {
		return nopWriteCloser{stdout}, nil
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// emitList renders items through writeFormatted to the --output destination,
// closing a file sink and surfacing its close error. The one line every list
// command uses, so --format + --columns + --output behave identically across them.
// applyColumns runs first (before openOutput) so a bad --columns fails without
// truncating the target file.
func emitList[T any](output string, stdout, stderr io.Writer, format, columns string, items []T, headers []string, row func(T) []string) int {
	headers, row, code := applyColumns(columns, format, headers, row, stderr)
	if code >= 0 {
		return code
	}
	w, err := openOutput(output, stdout)
	if err != nil {
		cliErrf(stderr, "write output: %v", err)
		return 1
	}
	code = writeFormatted(w, stderr, format, items, headers, row)
	if cerr := w.Close(); cerr != nil && code == 0 {
		cliErrf(stderr, "write output: %v", cerr)
		return 1
	}
	return code
}

func writeTable[T any](w io.Writer, headers []string, items []T, row func(T) []string, style string) {
	tw := table.NewWriter()
	tw.SetOutputMirror(w)
	tw.AppendHeader(toAnyRow(headers))
	// The plain terminal table caps each cell so one pathological value (e.g. a
	// 400-char cache path) can't widen every row past the screen. markdown/html are
	// for documents/structured output and keep full content.
	cellMax := 0
	if style != "markdown" && style != "html" {
		cellMax = 72
	}
	for _, item := range items {
		tw.AppendRow(toAnyRow(capCells(row(item), cellMax)))
	}
	switch style {
	case "markdown":
		tw.RenderMarkdown()
	case "html":
		tw.RenderHTML()
	default:

		s := table.StyleDefault
		s.Options.DrawBorder = false
		s.Options.SeparateColumns = false
		s.Options.SeparateHeader = false
		s.Options.SeparateRows = false
		s.Box.MiddleVertical = ""
		s.Box.PaddingLeft = ""
		s.Box.PaddingRight = "  "
		tw.SetStyle(s)
		tw.Render()
	}
}

// capCells truncates each cell to max runes (with a … elision) for the plain
// terminal table. max<=0 leaves cells untouched (markdown/html keep full content).
func capCells(cells []string, max int) []string {
	if max <= 0 {
		return cells
	}
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = textutil.Truncate(c, max)
	}
	return out
}

func toAnyRow(cells []string) table.Row {
	out := make(table.Row, len(cells))
	for i, c := range cells {
		out[i] = c
	}
	return out
}

// displayLocation is the timezone for rendered timestamps. Storage stays UTC
// (see timeText/nowUTC in the store); this only affects CLI presentation. It is
// set once at startup from the "timezone" config key (see resolveDisplayLocation)
// and defaults to UTC.
var displayLocation = time.UTC

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(displayLocation).Format(time.RFC3339)
}

// resolveDisplayLocation sets displayLocation from a timezone name: "" / "utc"
// → UTC, "local" → the host zone, otherwise an IANA name via time.LoadLocation.
// An unknown name returns an error and leaves UTC in place.
func resolveDisplayLocation(name string) error {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "utc":
		displayLocation = time.UTC
		return nil
	case "local":
		displayLocation = time.Local
		return nil
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	displayLocation = loc
	return nil
}
