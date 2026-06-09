package sqlite

import (
	"context"
	"database/sql"
	"strings"
)

// maxBulkBindVars caps the bind-variable count per multi-row INSERT. The bundled
// sqlite (mattn/go-sqlite3) allows SQLITE_MAX_VARIABLE_NUMBER=32766; 20000 leaves
// generous headroom.
//
// Batching is not about allocation (the bound args scale with row×col either way)
// — it is about write latency. With the ingest write pipelined onto a dedicated
// goroutine (see StreamSessions), the writer must keep up with parse+redact or the
// producer stalls. Collapsing ~160k per-row Execs into a few hundred multi-row
// statements cuts the per-row round trips (one sqlite3_step per chunk instead of
// per row), which roughly halves ingest wall time; a per-row writer becomes the
// pipeline bottleneck and the scheduler churns parking the producer on it.
const maxBulkBindVars = 20000

// placeholders returns a single "(?,?,…)" row group with cols bind markers.
func placeholders(cols int) string {
	if cols <= 0 {
		return "()"
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", cols), ",") + ")"
}

// execRows inserts n rows via chunked multi-row INSERT statements. prefix is the
// statement head up to (but not including) VALUES, e.g.
// `INSERT OR IGNORE INTO t(a, b, c)`; rowGroup is one VALUES tuple template (e.g.
// placeholders(3) → "(?,?,?)", or a literal-bearing form like
// "('turn', ?, ?, ? || ' ' || ?)"); binds is the number of bind markers in
// rowGroup; suffix is appended after the VALUES list (e.g. "" or
// " ON CONFLICT(content_hash) DO NOTHING"). args(i) returns the binds values for
// row i, in rowGroup order.
//
// It is the bulk-insert counterpart to a prepared-statement-per-row loop:
// semantically identical (the conflict clause in prefix/suffix is evaluated per
// row by sqlite) but with far fewer Execs.
func execRows(ctx context.Context, tx *sql.Tx, prefix, rowGroup string, binds, n int, suffix string, args func(i int) []any) error {
	if n == 0 || binds == 0 {
		return nil
	}
	maxRows := max(maxBulkBindVars/binds, 1)
	var b strings.Builder
	buf := make([]any, 0, min(n, maxRows)*binds)
	for start := 0; start < n; start += maxRows {
		end := min(start+maxRows, n)
		b.Reset()
		b.WriteString(prefix)
		b.WriteString(" VALUES ")
		buf = buf[:0]
		for i := start; i < end; i++ {
			if i > start {
				b.WriteByte(',')
			}
			b.WriteString(rowGroup)
			buf = append(buf, args(i)...)
		}
		b.WriteString(suffix)
		if _, err := tx.ExecContext(ctx, b.String(), buf...); err != nil {
			return err
		}
	}
	return nil
}
