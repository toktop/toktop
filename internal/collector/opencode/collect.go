package opencode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	opencodeparser "toktop.unceas.dev/internal/parser/opencode"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

// CollectSessionFile reads one opencode session from the DB and synthesizes a
// source.RawSession whose RawEventList is one RawEvent per DB row (a leading
// session event, then per-message: the message event followed by its part events),
// in chronological order. The provider is DB-backed, so there is no JSONL file;
// each RawEvent.RawJSON is a cloned, re-serialized envelope (see envelope.go) and
// RawEvent.SourceFile is the synthetic "opencode://<id>" key.
func CollectSessionFile(ctx context.Context, file SessionFile) (source.RawSession, []trace.ParseError, error) {
	db, err := openReadOnly(file.DBPath)
	if err != nil {
		return source.RawSession{}, nil, err
	}
	defer db.Close()

	// Project path/name and the subagent markers (IsSubagent/ParentExternalID/
	// AgentType/SubagentKind) are derived by the parser from the leading KindSession
	// envelope, not from RawSession — only ParentToolUseID needs the collector's
	// cross-session DB visibility (resolved once in DiscoverSessions), so it rides here.
	raw := source.RawSession{
		Provider:        "opencode",
		SourceRoot:      file.Root.Path,
		SourceFile:      PathOf(file),
		ParentToolUseID: file.ParentToolUseID,
	}

	sourceID := trace.SourceID(raw.Provider)
	sourceRootID := trace.SourceRootID(sourceID, raw.SourceRoot)
	var parseErrors []trace.ParseError
	ordinal := 0
	add := func(eventType string, when time.Time, payload []byte) {
		ordinal++
		// CRITICAL: RawJSON aliases payload, so it must be a clone — mirrors the
		// bytes.Clone collector.ReadJSONLLines does for file providers. Dropping it
		// would let a reused scan buffer corrupt raw_events silently.
		b := bytes.Clone(payload)
		raw.RawEventList = append(raw.RawEventList, source.RawEvent{
			Provider:   raw.Provider,
			SourceRoot: raw.SourceRoot,
			SourceFile: raw.SourceFile,
			LineNo:     ordinal,
			ByteOffset: 0,
			EventType:  trace.InternString(eventType),
			EventTime:  when,
			SessionID:  trace.InternString(file.SessionID),
			RawJSON:    json.RawMessage(b),
			RawHash:    trace.HashPayload(b),
		})
	}
	addErr := func(message string) {
		parseErrors = append(parseErrors, trace.ParseError{
			SourceID:      sourceID,
			SourceRootID:  sourceRootID,
			SourceFile:    raw.SourceFile,
			LineNo:        ordinal,
			Message:       message,
			ParserVersion: opencodeparser.ParserVersion,
		})
	}

	// 1. Leading session event.
	sessionEnv, err := readSessionRow(ctx, db, file.SessionID)
	if err != nil {
		return source.RawSession{}, parseErrors, err
	}
	if payload, mErr := json.Marshal(sessionEnv); mErr != nil {
		addErr("marshal session envelope: " + mErr.Error())
	} else {
		add(opencodeparser.KindSession, opencodeparser.MsTime(sessionEnv.TimeCreated), payload)
	}

	// 2. Parts grouped under their message, in message order.
	partsByMsg, err := loadParts(ctx, db, file.SessionID)
	if err != nil {
		return source.RawSession{}, parseErrors, err
	}

	msgRows, err := db.QueryContext(ctx, `
		SELECT id, time_created, COALESCE(json_extract(data,'$.role'),''), data
		FROM message WHERE session_id = ? ORDER BY time_created, id`, file.SessionID)
	if err != nil {
		return source.RawSession{}, parseErrors, fmt.Errorf("read opencode messages: %w", err)
	}
	defer msgRows.Close()
	for msgRows.Next() {
		var (
			id   string
			tc   int64
			role string
			data []byte
		)
		if err := msgRows.Scan(&id, &tc, &role, &data); err != nil {
			return source.RawSession{}, parseErrors, fmt.Errorf("scan opencode message: %w", err)
		}
		kind := role // "user" | "assistant"
		if kind != opencodeparser.KindUser && kind != opencodeparser.KindAssistant {
			// Unknown/absent role: keep the event (so raw_events is complete) but
			// flag it; the parser ignores a message kind it does not recognize.
			addErr("message " + id + " has unexpected role " + role)
		}
		env := opencodeparser.MessageEnvelope{ID: id, Data: json.RawMessage(data)}
		if payload, mErr := json.Marshal(env); mErr != nil {
			addErr("marshal message envelope " + id + ": " + mErr.Error())
		} else {
			add(kind, opencodeparser.MsTime(tc), payload)
		}
		for _, p := range partsByMsg[id] {
			penv := opencodeparser.PartEnvelope{ID: p.id, MessageID: id, Data: json.RawMessage(p.data)}
			payload, mErr := json.Marshal(penv)
			if mErr != nil {
				addErr("marshal part envelope " + p.id + ": " + mErr.Error())
				continue
			}
			add(p.typ, opencodeparser.MsTime(p.timeCreated), payload)
		}
	}
	if err := msgRows.Err(); err != nil {
		return source.RawSession{}, parseErrors, fmt.Errorf("iterate opencode messages: %w", err)
	}
	return raw, parseErrors, nil
}

type partRow struct {
	id          string
	typ         string
	timeCreated int64
	data        []byte
}

// loadParts returns every part of a session grouped by its message id, each
// group in (time_created, id) order — the order the global query yields.
func loadParts(ctx context.Context, db *sql.DB, sessionID string) (map[string][]partRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, message_id, time_created, COALESCE(json_extract(data,'$.type'),''), data
		FROM part WHERE session_id = ? ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read opencode parts: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]partRow)
	for rows.Next() {
		var (
			p     partRow
			msgID string
		)
		if err := rows.Scan(&p.id, &msgID, &p.timeCreated, &p.typ, &p.data); err != nil {
			return nil, fmt.Errorf("scan opencode part: %w", err)
		}
		out[msgID] = append(out[msgID], p)
	}
	return out, rows.Err()
}
