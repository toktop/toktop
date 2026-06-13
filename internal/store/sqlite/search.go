package sqlite

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

type SearchResult struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`

	Provider  string `json:"provider,omitzero"`
	SessionID string `json:"session_id,omitzero"`
	TurnID    string `json:"turn_id,omitzero"`
	Snippet   string `json:"snippet"`
}

func (s *Store) Search(ctx context.Context, query string, limit int, kind, source string) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxPageSize {
		// Cap like the list handlers (pagination): an unbounded limit reaches
		// make([]SearchResult, 0, limit) below and would let one request pre-allocate
		// gigabytes (OOM/DoS).
		limit = maxPageSize
	}
	match := buildFTSMatch(query)
	// Push the kind/source filters into the WHERE (search_fts.kind is an UNINDEXED
	// FTS column; the provider comes from the sources join) so the LIMIT applies
	// AFTER filtering. Filtering in Go after a rank-ordered LIMIT under-returned —
	// the top-N rows could be mostly of other kinds/sources.
	where := []string{"search_fts MATCH ?"}
	args := []any{match}
	if kind != "" {
		where = append(where, "search_fts.kind = ?")
		args = append(args, kind)
	}
	if source != "" {
		where = append(where, "sources.kind = ?")
		args = append(args, source)
	}
	args = append(args, limit)
	rows, err := s.reader().QueryContext(ctx, `
		SELECT search_fts.kind, search_fts.id, COALESCE(sources.kind, ''),
		       COALESCE(search_fts.session_id, ''), COALESCE(search_fts.turn_id, ''),
		       snippet(search_fts, 6, '[', ']', '…', 12)
		FROM search_fts
		LEFT JOIN sources ON sources.id = search_fts.source_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY rank
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("search fts: %w", err)
	}
	defer rows.Close()
	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Kind, &r.ID, &r.Provider, &r.SessionID, &r.TurnID, &r.Snippet); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func buildFTSMatch(query string) string {
	tokens := tokenizeQuery(query)
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token.quoted {
			parts = append(parts, `"`+escapeFTS(token.value)+`"`)
			continue
		}
		for _, term := range ftsTerms(token.value) {
			parts = append(parts, `"`+escapeFTS(term)+`"*`)
		}
	}
	if len(parts) == 0 {
		return `"` + escapeFTS(query) + `"`
	}
	// Join tokens with AND so multi-word queries require every term (matching
	// FTS5's implicit-AND convention); OR is reserved for explicit operators.
	return strings.Join(parts, " AND ")
}

type queryToken struct {
	value  string
	quoted bool
}

func tokenizeQuery(query string) []queryToken {
	var tokens []queryToken
	var current strings.Builder
	quoted := false
	flush := func() {
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" {
			return
		}
		tokens = append(tokens, queryToken{value: token, quoted: quoted})
	}
	for _, r := range query {
		switch {
		case r == '"':
			flush()
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func ftsTerms(token string) []string {
	var terms []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		terms = append(terms, current.String())
		current.Reset()
	}
	for _, r := range token {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return terms
}

func escapeFTS(value string) string {
	return strings.ReplaceAll(value, `"`, `""`)
}
