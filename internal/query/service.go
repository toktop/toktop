package query

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"toktop.unceas.dev/internal/handoff"
	"toktop.unceas.dev/internal/rules"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/trace"
)

type Service struct {
	store *sqlite.Store
}

func New(store *sqlite.Store) *Service {
	return &Service{store: store}
}

type Page[T any] struct {
	Items      []T `json:"items"`
	Total      int `json:"total"`
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	NextOffset int `json:"next_offset"`
}

func (s *Service) Summary(ctx context.Context, f sqlite.Filter) (sqlite.Summary, error) {
	return s.store.SummaryFiltered(ctx, f)
}

func (s *Service) ListTurns(ctx context.Context, f sqlite.Filter) (Page[trace.Turn], error) {
	turns, total, err := s.store.ListTurnsFiltered(ctx, f)
	if err != nil {
		return Page[trace.Turn]{}, err
	}
	return makePage(turns, total, f, 50), nil
}

func (s *Service) ListSessions(ctx context.Context, f sqlite.Filter) (Page[trace.Session], error) {
	sessions, total, err := s.store.ListSessionsFiltered(ctx, f)
	if err != nil {
		return Page[trace.Session]{}, err
	}
	return makePage(sessions, total, f, 50), nil
}

func (s *Service) ListLiveSessions(ctx context.Context, f sqlite.Filter) (Page[sqlite.LiveSessionItem], error) {
	sessions, total, err := s.store.ListLiveSessions(ctx, f)
	if err != nil {
		return Page[sqlite.LiveSessionItem]{}, err
	}
	return makePage(sessions, total, f, 100), nil
}

func (s *Service) GetTurn(ctx context.Context, turnID string) (trace.Turn, error) {
	turn, err := s.store.GetTurn(ctx, turnID)
	if err != nil {
		return trace.Turn{}, mapNotFound(err)
	}
	return turn, nil
}

func (s *Service) FindSessions(ctx context.Context, id string) ([]trace.Session, error) {
	sessions, err := s.store.FindSessions(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, ErrNotFound
	}
	return sessions, nil
}

func (s *Service) SessionTurns(ctx context.Context, sessionID string) ([]trace.Turn, error) {
	return s.store.TurnsForSession(ctx, sessionID)
}

// SubagentRuns returns the completed sub-agent runs linked to parentID, mapped to
// the neutral handoff input. The store→handoff mapping lives here once so the
// handoff package stays store-agnostic.
func (s *Service) SubagentRuns(ctx context.Context, parentID string) ([]handoff.SubagentRun, error) {
	rows, err := s.store.SubagentRunsForParent(ctx, parentID)
	if err != nil {
		return nil, err
	}
	runs := make([]handoff.SubagentRun, len(rows))
	for i, r := range rows {
		runs[i] = handoff.SubagentRun{
			SessionID:       r.SessionID,
			ExternalID:      r.ExternalID,
			TranscriptPath:  r.TranscriptPath,
			AgentType:       r.AgentType,
			SubagentKind:    r.SubagentKind,
			WorkflowRunID:   r.WorkflowRunID,
			ParentToolUseID: r.ParentToolUseID,
			Status:          r.Status,
			Result:          r.Result,
			StartedAt:       r.StartedAt,
			EndedAt:         r.EndedAt,
		}
	}
	return runs, nil
}

func (s *Service) ListProjects(ctx context.Context, f sqlite.Filter) ([]sqlite.ProjectListItem, error) {
	return s.store.ListProjects(ctx, f)
}

func (s *Service) ListTools(ctx context.Context, f sqlite.Filter) ([]sqlite.ToolListItem, error) {
	return s.store.ListTools(ctx, f)
}

func (s *Service) ListToolCalls(ctx context.Context, tcf sqlite.ToolCallFilter) ([]sqlite.ToolCallListItem, error) {
	return s.store.ListToolCalls(ctx, tcf)
}

func (s *Service) ListModels(ctx context.Context, f sqlite.Filter) ([]sqlite.ModelListItem, error) {
	return s.store.ListModels(ctx, f)
}

func (s *Service) ListMCPs(ctx context.Context, f sqlite.Filter) ([]sqlite.MCPListItem, error) {
	return s.store.ListMCPs(ctx, f)
}

func (s *Service) ListUnusedMCPs(ctx context.Context) ([]sqlite.MCPListItem, error) {
	return s.store.ListUnusedMCPs(ctx)
}

func (s *Service) ListSkills(ctx context.Context, f sqlite.Filter) ([]sqlite.SkillListItem, error) {
	return s.store.ListSkills(ctx, f)
}

func (s *Service) ListUnusedSkills(ctx context.Context) ([]sqlite.SkillListItem, error) {
	return s.store.ListUnusedSkills(ctx)
}

func (s *Service) ListComponents(ctx context.Context, turnID string) ([]trace.TurnComponent, error) {
	return s.store.ListComponentsForTurn(ctx, turnID)
}

func (s *Service) Search(ctx context.Context, query string, limit int, kind, source string, includeSubagents bool) ([]sqlite.SearchResult, error) {
	return s.store.Search(ctx, query, limit, kind, source, includeSubagents)
}

func (s *Service) Suggestions(ctx context.Context, ruleID string) ([]trace.Suggestion, error) {
	return s.store.ListSuggestions(ctx, ruleID)
}

func (s *Service) RecomputeSuggestions(ctx context.Context, now time.Time) ([]trace.Suggestion, error) {
	// Load the full history: ToolOutputBloat, RetryLoop, and
	// LongSessionDegradation ignore `now` and scan the whole index, so a windowed
	// load silently hid older signals here while the CLI's full-history path still
	// surfaced them. MCPUnused30d applies its own 30-day cutoff, so a full load
	// does not widen it.
	// Rules analyze the user's own (top-level) sessions; subagents are excluded so
	// 2000+ nested runs don't flood suggestions.
	index, err := s.store.LoadIndex(ctx, time.Time{}, false)
	if err != nil {
		return nil, err
	}
	out := rules.Run(ctx, index, now)
	if err := s.store.ReplaceSuggestions(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Snapshot loads the trace index for export. A zero since loads everything; a
// non-zero since selects sessions by effective time and includes all turns for
// those sessions.
func (s *Service) Snapshot(ctx context.Context, since time.Time, includeSubagents bool) (trace.Index, error) {
	return s.store.LoadIndex(ctx, since, includeSubagents)
}

var ErrNotFound = errors.New("not found")

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// makePage builds a page envelope whose Limit/Offset mirror the effective
// pagination the store actually applied. defaultLimit must match the value the
// corresponding store query passes to Filter.pagination so the reported Limit
// is truthful.
func makePage[T any](items []T, total int, f sqlite.Filter, defaultLimit int) Page[T] {
	limit, offset := sqlite.EffectivePagination(f, defaultLimit)
	next := sqlite.NextOffset(offset, len(items), total)
	return Page[T]{Items: items, Total: total, Limit: limit, Offset: offset, NextOffset: next}
}
