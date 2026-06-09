package sqlite

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) PruneRawEvents(ctx context.Context, cutoff time.Time, dryRun bool) (int64, error) {
	cutoffText := timeBound(cutoff)
	if dryRun {
		var count int64
		err := s.reader().QueryRowContext(ctx, `
			SELECT COUNT(*) FROM raw_events
			WHERE COALESCE(NULLIF(event_time, ''), imported_at) < ?
		`, cutoffText).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("count raw events for prune: %w", err)
		}
		return count, nil
	}
	result, err := s.writer().ExecContext(ctx, `
		DELETE FROM raw_events
		WHERE COALESCE(NULLIF(event_time, ''), imported_at) < ?
	`, cutoffText)
	if err != nil {
		return 0, fmt.Errorf("prune raw events: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) PruneToolOutputs(ctx context.Context, cutoff time.Time, dryRun bool) (int64, error) {
	cutoffText := timeBound(cutoff)
	if dryRun {
		var count int64
		err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_outputs WHERE created_at < ?`, cutoffText).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("count tool_outputs for prune: %w", err)
		}
		return count, nil
	}
	result, err := s.writer().ExecContext(ctx, `DELETE FROM tool_outputs WHERE created_at < ?`, cutoffText)
	if err != nil {
		return 0, fmt.Errorf("prune tool_outputs: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) RedactNormalized(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffText := timeBound(cutoff)
	tx, err := s.writer().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin redact normalized: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Fall back to ended_at then created_at (NOT NULL) when started_at is
	// missing, so sessions without a start time are aged by a real timestamp
	// instead of collapsing to '' (which sorts before every cutoff and would
	// redact in-retention content).
	oldSessions := `(
		SELECT id FROM sessions
		WHERE COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), created_at) < ?
	)`

	updates := []struct {
		name string
		sql  string
		args []any
	}{
		{"turns", `UPDATE turns SET user_message = '', assistant_final = '', summary = ''
			WHERE session_id IN ` + oldSessions + `
			  AND (user_message <> '' OR assistant_final <> '' OR summary <> '')`, []any{cutoffText}},
		{"tool_calls", `UPDATE tool_calls SET input_json = '', output_text = '', error = ''
			WHERE session_id IN ` + oldSessions + `
			  AND (input_json <> '' OR output_text <> '' OR error <> '')`, []any{cutoffText}},
		// context_events.session_id is nullable, so a turn_id-only row (legitimate:
		// SessionID is omitzero, the read path joins on turn_id) would never match
		// `session_id IN oldSessions` and keep expired evidence. Also age it via its
		// turn's session.
		{"context_events", `UPDATE context_events SET evidence = ''
			WHERE evidence <> ''
			  AND (session_id IN ` + oldSessions + `
			       OR turn_id IN (SELECT id FROM turns WHERE session_id IN ` + oldSessions + `))`,
			[]any{cutoffText, cutoffText}},
	}

	var total int64
	for _, u := range updates {
		res, execErr := tx.ExecContext(ctx, u.sql, u.args...)
		if execErr != nil {
			err = fmt.Errorf("redact normalized %s: %w", u.name, execErr)
			return 0, err
		}
		n, raErr := res.RowsAffected()
		if raErr != nil {
			err = fmt.Errorf("rows affected %s: %w", u.name, raErr)
			return 0, err
		}
		total += n
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE tool_outputs SET content_text = ''
		WHERE created_at < ? AND content_text <> ''
	`, cutoffText)
	if err != nil {
		return 0, fmt.Errorf("redact normalized tool_outputs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected tool_outputs: %w", err)
	}
	total += n

	// search_fts is an external-content index over search_documents; blanking
	// the normalized rows above leaves the inverted index (and snippet() output)
	// intact, so expired text would stay searchable. Blank the matching
	// search_documents rows so the AFTER UPDATE trigger re-syncs search_fts.
	// Not added to total: these are index rows for turns/tool_calls already
	// counted above, not distinct redactions.
	if _, err = tx.ExecContext(ctx, `
		UPDATE search_documents SET text = ''
		WHERE session_id IN `+oldSessions+`
		  AND text <> ''
	`, cutoffText); err != nil {
		return 0, fmt.Errorf("redact normalized search_documents: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit redact normalized: %w", err)
	}
	return total, nil
}
