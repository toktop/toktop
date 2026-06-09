package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
)

// validateFormat rejects a --format value outside `allowed` (treating "" as the
// default). For the limited table|json commands; the full table/json/ndjson/csv/
// markdown/html set is validated inside writeFormatted itself.
func validateFormat(format string, allowed ...string) error {
	if format == "" || slices.Contains(allowed, format) {
		return nil
	}
	return fmt.Errorf("unknown --format %q (want %s)", format, strings.Join(allowed, " or "))
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
		cliErr(stderr, fmt.Errorf("unknown --format %q (want table|json|ndjson|csv|markdown|html)", format))
		return 2
	}
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
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
		out[i] = truncate(c, max)
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
