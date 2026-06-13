package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"toktop.unceas.dev/internal/trace"
)

const queryInChunk = 400

func (s *Store) attachChildrenToTurns(ctx context.Context, turns []trace.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	invocations, err := s.loadInvocationsForTurns(ctx, ids)
	if err != nil {
		return err
	}
	toolCalls, err := s.loadToolCallsForTurns(ctx, ids)
	if err != nil {
		return err
	}
	components, err := s.loadComponentsForTurns(ctx, ids)
	if err != nil {
		return err
	}
	for i := range turns {
		turns[i].Invocations = invocations[turns[i].ID]
		turns[i].ToolCalls = toolCalls[turns[i].ID]
		turns[i].Components = components[turns[i].ID]
	}
	return nil
}

func eachTurnChunk(ids []string, fn func(placeholders string, args []any) error) error {
	for start := 0; start < len(ids); start += queryInChunk {
		end := min(start+queryInChunk, len(ids))
		chunk := ids[start:end]
		markers := bindMarkers(len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if err := fn(markers, args); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadInvocationsForTurns(ctx context.Context, turnIDs []string) (map[string][]trace.Invocation, error) {
	out := make(map[string][]trace.Invocation, len(turnIDs))
	err := eachTurnChunk(turnIDs, func(placeholders string, args []any) error {
		rows, err := s.reader().QueryContext(ctx, `
			SELECT turn_id, id, COALESCE(provider, ''), session_id, invocation_index,
			       COALESCE(model, ''),
			       COALESCE(started_at, ''), COALESCE(ended_at, ''),
			       COALESCE(stop_reason, ''), status,
			       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cache_write_long_tokens, COALESCE(context_window_tokens, 0),
			       COALESCE(raw_event_id, '')
			FROM invocations
			WHERE turn_id IN (`+placeholders+`)
			ORDER BY turn_id, invocation_index
		`, args...)
		if err != nil {
			return fmt.Errorf("load invocations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var inv trace.Invocation
			var startedAt, endedAt sql.NullString
			if err := rows.Scan(
				&inv.TurnID, &inv.ID, &inv.Provider, &inv.SessionID, &inv.Index,
				&inv.Model,
				&startedAt, &endedAt,
				&inv.StopReason, &inv.Status,
				&inv.Tokens.Input, &inv.Tokens.Output, &inv.Tokens.CacheRead, &inv.Tokens.CacheWrite, &inv.Tokens.CacheWriteLong, &inv.ContextWindowTokens,
				&inv.RawEventID,
			); err != nil {
				return fmt.Errorf("scan invocation: %w", err)
			}
			inv.StartedAt = parseTimeOpt(startedAt)
			inv.EndedAt = parseTimeOpt(endedAt)
			out[inv.TurnID] = append(out[inv.TurnID], inv)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) loadToolCallsForTurns(ctx context.Context, turnIDs []string) (map[string][]trace.ToolCall, error) {
	out := make(map[string][]trace.ToolCall, len(turnIDs))
	err := eachTurnChunk(turnIDs, func(placeholders string, args []any) error {
		rows, err := s.reader().QueryContext(ctx, `
			SELECT turn_id, id, session_id, COALESCE(invocation_id, ''), call_index,
			       tool_kind, tool_name, COALESCE(mcp_server, ''), COALESCE(mcp_tool, ''), COALESCE(use_id, ''),
			       COALESCE(input_json, ''), COALESCE(output_text, ''), output_bytes,
			       status, COALESCE(error, ''),
			       COALESCE(started_at, ''), COALESCE(ended_at, ''), duration_ms,
			       COALESCE(raw_use_event_id, ''), COALESCE(raw_result_event_id, '')
			FROM tool_calls
			WHERE turn_id IN (`+placeholders+`)
			ORDER BY turn_id, call_index
		`, args...)
		if err != nil {
			return fmt.Errorf("load tool calls: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var call trace.ToolCall
			var startedAt, endedAt sql.NullString
			if err := rows.Scan(
				&call.TurnID, &call.ID, &call.SessionID, &call.InvocationID, &call.CallIndex,
				&call.Kind, &call.Name, &call.MCPServer, &call.MCPTool, &call.UseID,
				&call.Input, &call.Output, &call.OutputBytes,
				&call.Status, &call.Error,
				&startedAt, &endedAt, &call.DurationMs,
				&call.RawUseEventID, &call.RawResultEventID,
			); err != nil {
				return fmt.Errorf("scan tool_call: %w", err)
			}
			call.StartedAt = parseTimeOpt(startedAt)
			call.EndedAt = parseTimeOpt(endedAt)
			out[call.TurnID] = append(out[call.TurnID], call)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) loadComponentsForTurns(ctx context.Context, turnIDs []string) (map[string][]trace.TurnComponent, error) {
	out := make(map[string][]trace.TurnComponent, len(turnIDs))
	err := eachTurnChunk(turnIDs, func(placeholders string, args []any) error {
		rows, err := s.reader().QueryContext(ctx, `
			SELECT turn_id, component_kind, COALESCE(component_id, ''), component_name, relation, token_estimate, COALESCE(evidence, ''), confidence
			FROM turn_components
			WHERE turn_id IN (`+placeholders+`)
			ORDER BY turn_id, id
		`, args...)
		if err != nil {
			return fmt.Errorf("load turn_components: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c trace.TurnComponent
			var confidence string
			if err := rows.Scan(&c.TurnID, &c.ComponentKind, &c.ComponentID, &c.ComponentName, &c.Relation, &c.TokenEstimate, &c.Evidence, &confidence); err != nil {
				return fmt.Errorf("scan turn_component: %w", err)
			}
			c.Confidence = trace.Confidence(confidence)
			out[c.TurnID] = append(out[c.TurnID], c)
		}
		return rows.Err()
	})
	return out, err
}
