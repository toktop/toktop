package trace

import (
	"encoding/json"
	"time"
)

type Confidence string

const (
	ConfidenceObserved  Confidence = "observed"
	ConfidenceEstimated Confidence = "estimated"
	ConfidenceInferred  Confidence = "inferred"
)

const (
	StatusUnknown              = "unknown"
	StatusActive               = "active"
	StatusCompleted            = "completed"
	StatusInterrupted          = "interrupted"
	StatusSuccess              = "success"
	StatusFailed               = "failed"
	StatusAwaitingConfirmation = "awaiting_confirmation"
	StatusPending              = "pending"
)

func StatusValues() []string {
	return []string{
		StatusUnknown,
		StatusActive,
		StatusCompleted,
		StatusInterrupted,
		StatusSuccess,
		StatusFailed,
		StatusAwaitingConfirmation,
		StatusPending,
	}
}

const (
	ToolKindBuiltin = "builtin"
	ToolKindMCP     = "mcp"
)

const (
	ComponentKindBuiltinTool = "builtin_tool"
	ComponentKindMCPServer   = "mcp_server"
	ComponentKindMCPTool     = "mcp_tool"
	ComponentKindSkill       = "skill"
)

const (
	RelationInvoked      = "invoked"
	RelationObservedUsed = "observed_used"
	RelationInferredUsed = "inferred_used"
)

type Index struct {
	GeneratedAt    time.Time       `json:"generated_at"`
	Source         string          `json:"source,omitzero"`
	ParserVersion  string          `json:"parser_version,omitzero"`
	SourceRoots    []string        `json:"source_roots,omitzero"`
	RawEventCount  int             `json:"raw_event_count"`
	// The record arrays carry NO omitzero: an export has a stable schema so a
	// consumer can always index sessions/turns/... A since-window that matched
	// nothing serializes [] (LoadIndex guarantees non-nil), never a dropped key.
	Sessions       []Session       `json:"sessions"`
	Turns          []Turn          `json:"turns"`
	Invocations    []Invocation    `json:"invocations"`
	TurnComponents []TurnComponent `json:"turn_components"`
	Skills         []Skill         `json:"skills"`
	MCPServers     []MCPServer     `json:"mcp_servers"`
	ParseErrorList []ParseError    `json:"parse_errors"`

	SessionCount    int `json:"session_count"`
	TurnCount       int `json:"turn_count"`
	InvocationCount int `json:"invocation_count"`
	ToolCallCount   int `json:"tool_call_count"`
}

type Tokens struct {
	Input      int `json:"input_tokens,omitzero"`
	Output     int `json:"output_tokens,omitzero"`
	CacheRead  int `json:"cache_read_tokens,omitzero"`
	CacheWrite int `json:"cache_write_tokens,omitzero"`
	// CacheWriteLong is the subset of CacheWrite written with a long-lived
	// cache TTL, which providers bill at a premium over their default tier
	// (for Claude Code: Anthropic's ephemeral_1h vs the 5m default). Parsers
	// guarantee CacheWriteLong <= CacheWrite; the short subset is the
	// difference. Providers without tiered cache writes leave it 0.
	CacheWriteLong int `json:"cache_write_long_tokens,omitzero"`
}

func (t *Tokens) Add(other Tokens) {
	t.Input += other.Input
	t.Output += other.Output
	t.CacheRead += other.CacheRead
	t.CacheWrite += other.CacheWrite
	t.CacheWriteLong += other.CacheWriteLong
}

type Session struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	ExternalID     string    `json:"external_id,omitzero"`
	ProjectID      string    `json:"project_id,omitzero"`
	ProjectName    string    `json:"project_name,omitzero"`
	ProjectPath    string    `json:"project_path,omitzero"`
	TranscriptPath string    `json:"transcript_path"`
	StartedAt      time.Time `json:"started_at,omitzero"`
	EndedAt        time.Time `json:"ended_at,omitzero"`
	Status         string    `json:"status"`
	TurnCount      int       `json:"turn_count"`
	ToolCallCount  int       `json:"tool_call_count"`
	Tokens         Tokens    `json:"tokens"`

	// Subagent linkage. A subagent session is a provider's nested agent transcript
	// (Claude Code: a Task/Agent run or a Workflow's internal agent), ingested as a
	// first-class but marked+linked session so aggregations can include it while
	// default listings exclude it. All fields are empty for a top-level session and
	// for providers without subagents (e.g. codex) — parity: a field is filled only
	// when the source carries it.
	IsSubagent bool `json:"is_subagent,omitzero"`
	// ParentExternalID is the external/thread id of the launching session, as each
	// provider reports it (claude-code: the subagent's in-file sessionId, which IS
	// the parent's external id; codex: payload.parent_thread_id). It is the neutral
	// link the parser sets; the store resolves it to the internal ParentSessionID by
	// matching a top-level session's external id (same provider). One mechanism for
	// both providers — claude-code's nested path is no longer needed to link.
	ParentExternalID string `json:"parent_external_id,omitzero"`
	ParentSessionID  string `json:"parent_session_id,omitzero"`  // the launching session's internal ID (resolved by the store from ParentExternalID)
	ParentToolUseID  string `json:"parent_tool_use_id,omitzero"` // the parent tool_use that spawned it (Task/Agent); empty for workflow agents
	WorkflowRunID    string `json:"workflow_run_id,omitzero"`    // the workflow run id (wf_…) for a Workflow's internal agent
	SubagentKind     string `json:"subagent_kind,omitzero"`      // "task" | "workflow" (claude-code) | "agent" (codex)
	AgentType        string `json:"agent_type,omitzero"`         // the subagent's declared type / role (e.g. "Explore", "explorer")
}

type Turn struct {
	ID              string    `json:"id"`
	Provider        string    `json:"provider"`
	SessionID       string    `json:"session_id"`
	ProjectID       string    `json:"project_id,omitzero"`
	ProjectName     string    `json:"project_name,omitzero"`
	ProjectPath     string    `json:"project_path,omitzero"`
	TranscriptPath  string    `json:"transcript_path"`
	Index           int       `json:"index"`
	UserMessage     string    `json:"user_message,omitzero"`
	AssistantFinal  string    `json:"assistant_final,omitzero"`
	StartedAt       time.Time `json:"started_at,omitzero"`
	EndedAt         time.Time `json:"ended_at,omitzero"`
	DurationMs      int64     `json:"duration_ms,omitzero"`
	Status          string    `json:"status"`
	InvocationCount int       `json:"invocation_count"`
	ToolCallCount   int       `json:"tool_call_count"`
	Tokens          Tokens    `json:"tokens"`

	ToolCalls   []ToolCall      `json:"tool_calls,omitzero"`
	Invocations []Invocation    `json:"invocations,omitzero"`
	Components  []TurnComponent `json:"components,omitzero"`
}

type Invocation struct {
	ID                  string    `json:"id"`
	Provider            string    `json:"provider"`
	SessionID           string    `json:"session_id"`
	TurnID              string    `json:"turn_id"`
	Index               int       `json:"index"`
	Model               string    `json:"model,omitzero"`
	StartedAt           time.Time `json:"started_at,omitzero"`
	EndedAt             time.Time `json:"ended_at,omitzero"`
	StopReason          string    `json:"stop_reason,omitzero"`
	Status              string    `json:"status"`
	ContextWindowTokens int       `json:"context_window_tokens,omitzero"`
	Tokens              Tokens    `json:"tokens"`
	RawEventID          string    `json:"raw_event_id,omitzero"`
}

type ToolCall struct {
	ID               string    `json:"id"`
	TurnID           string    `json:"turn_id"`
	SessionID        string    `json:"session_id"`
	InvocationID     string    `json:"invocation_id,omitzero"`
	CallIndex        int       `json:"call_index"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	MCPServer        string    `json:"mcp_server,omitzero"`
	MCPTool          string    `json:"mcp_tool,omitzero"`
	UseID            string    `json:"use_id,omitzero"`
	Input            string    `json:"input,omitzero"`
	Output           string    `json:"output,omitzero"`
	OutputBytes      int64     `json:"output_bytes,omitzero"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitzero"`
	StartedAt        time.Time `json:"started_at,omitzero"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	DurationMs       int64     `json:"duration_ms,omitzero"`
	RawUseEventID    string    `json:"raw_use_event_id,omitzero"`
	RawResultEventID string    `json:"raw_result_event_id,omitzero"`
}

type TurnComponent struct {
	TurnID        string     `json:"turn_id"`
	ComponentKind string     `json:"component_kind"`
	ComponentID   string     `json:"component_id,omitzero"`
	ComponentName string     `json:"component_name"`
	Relation      string     `json:"relation"`
	TokenEstimate int        `json:"token_estimate,omitzero"`
	Evidence      string     `json:"evidence,omitzero"`
	Confidence    Confidence `json:"confidence"`
}

type Skill struct {
	ID            string          `json:"id"`
	SourceID      string          `json:"source_id,omitzero"`
	Name          string          `json:"name"`
	Scope         string          `json:"scope"`
	SourcePath    string          `json:"source_path,omitzero"`
	SourceHash    string          `json:"source_hash,omitzero"`
	Description   string          `json:"description,omitzero"`
	Version       string          `json:"version,omitzero"`
	ArgumentHint  string          `json:"argument_hint,omitzero"`
	UserInvocable *bool           `json:"user_invocable,omitempty"`
	Triggers      json.RawMessage `json:"triggers,omitempty"`
	AllowedTools  json.RawMessage `json:"allowed_tools,omitempty"`
	Tools         json.RawMessage `json:"tools,omitempty"`
	Compatibility string          `json:"compatibility,omitzero"`
	License       string          `json:"license,omitzero"`
}

type MCPServer struct {
	ID         string `json:"id"`
	SourceID   string `json:"source_id,omitzero"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Transport  string `json:"transport"`
	ConfigPath string `json:"config_path,omitzero"`
	ConfigHash string `json:"config_hash,omitzero"`
	Enabled    bool   `json:"enabled"`
}

type ParseError struct {
	SourceID      string `json:"source_id"`
	SourceRootID  string `json:"source_root_id,omitzero"`
	SourceFile    string `json:"source_file,omitzero"`
	LineNo        int    `json:"line_no,omitzero"`
	RawEventID    string `json:"raw_event_id,omitzero"`
	Message       string `json:"message"`
	ParserVersion string `json:"parser_version,omitzero"`
}

type Suggestion struct {
	ID             int64      `json:"id"`
	RuleID         string     `json:"rule_id"`
	Severity       string     `json:"severity"`
	Confidence     Confidence `json:"confidence"`
	ScopeKind      string     `json:"scope_kind,omitzero"`
	ScopeID        string     `json:"scope_id,omitzero"`
	EvidenceJSON   string     `json:"evidence,omitzero"`
	Recommendation string     `json:"recommendation,omitzero"`
}
