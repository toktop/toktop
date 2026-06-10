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
	Sessions       []Session       `json:"sessions,omitzero"`
	Turns          []Turn          `json:"turns,omitzero"`
	Invocations    []Invocation    `json:"invocations,omitzero"`
	SubagentRuns   []SubagentRun   `json:"subagent_runs,omitzero"`
	ToolOutputs    []ToolOutput    `json:"tool_outputs,omitzero"`
	ContextEvents  []ContextEvent  `json:"context_events,omitzero"`
	TurnComponents []TurnComponent `json:"turn_components,omitzero"`
	Skills         []Skill         `json:"skills,omitzero"`
	MCPServers     []MCPServer     `json:"mcp_servers,omitzero"`
	ParseErrorList []ParseError    `json:"parse_errors,omitzero"`

	SessionCount    int `json:"session_count"`
	TurnCount       int `json:"turn_count"`
	InvocationCount int `json:"invocation_count"`
	ToolCallCount   int `json:"tool_call_count"`
	SubagentCount   int `json:"subagent_count"`
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
	Summary         string    `json:"summary,omitzero"`
	StartedAt       time.Time `json:"started_at,omitzero"`
	EndedAt         time.Time `json:"ended_at,omitzero"`
	DurationMs      int64     `json:"duration_ms,omitzero"`
	Status          string    `json:"status"`
	FailureReason   string    `json:"failure_reason,omitzero"`
	InvocationCount int       `json:"invocation_count"`
	ToolCallCount   int       `json:"tool_call_count"`
	SubagentCount   int       `json:"subagent_count"`
	Tokens          Tokens    `json:"tokens"`

	ToolCalls     []ToolCall      `json:"tool_calls,omitzero"`
	Invocations   []Invocation    `json:"invocations,omitzero"`
	SubagentRuns  []SubagentRun   `json:"subagent_runs,omitzero"`
	Components    []TurnComponent `json:"components,omitzero"`
	ContextEvents []ContextEvent  `json:"context_events,omitzero"`
}

type Invocation struct {
	ID                  string    `json:"id"`
	Provider            string    `json:"provider"`
	SessionID           string    `json:"session_id"`
	TurnID              string    `json:"turn_id"`
	SubagentRunID       string    `json:"subagent_run_id,omitzero"`
	Index               int       `json:"index"`
	Model               string    `json:"model,omitzero"`
	StartedAt           time.Time `json:"started_at,omitzero"`
	EndedAt             time.Time `json:"ended_at,omitzero"`
	LatencyMs           int64     `json:"latency_ms,omitzero"`
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
	SubagentRunID    string    `json:"subagent_run_id,omitzero"`
	CallIndex        int       `json:"call_index"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	MCPServer        string    `json:"mcp_server,omitzero"`
	MCPTool          string    `json:"mcp_tool,omitzero"`
	UseID            string    `json:"use_id,omitzero"`
	Input            string    `json:"input,omitzero"`
	Output           string    `json:"output,omitzero"`
	OutputRef        string    `json:"output_ref,omitzero"`
	OutputBytes      int64     `json:"output_bytes,omitzero"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitzero"`
	StartedAt        time.Time `json:"started_at,omitzero"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	DurationMs       int64     `json:"duration_ms,omitzero"`
	RawUseEventID    string    `json:"raw_use_event_id,omitzero"`
	RawResultEventID string    `json:"raw_result_event_id,omitzero"`
}

type ToolOutput struct {
	ID             string    `json:"id"`
	SourceFile     string    `json:"source_file,omitzero"`
	ContentText    string    `json:"content_text,omitzero"`
	ContentHash    string    `json:"content_hash"`
	SizeBytes      int64     `json:"size_bytes"`
	RetentionClass string    `json:"retention_class"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
}

type SubagentRun struct {
	ID               string    `json:"id"`
	ParentTurnID     string    `json:"parent_turn_id"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitzero"`
	AgentName        string    `json:"agent_name,omitzero"`
	AgentType        string    `json:"agent_type,omitzero"`
	Model            string    `json:"model,omitzero"`
	TranscriptPath   string    `json:"transcript_path,omitzero"`
	StartedAt        time.Time `json:"started_at,omitzero"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	DurationMs       int64     `json:"duration_ms,omitzero"`
	Status           string    `json:"status"`
	Tokens           Tokens    `json:"tokens"`

	Invocations []Invocation `json:"invocations,omitzero"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitzero"`
}

type ContextEvent struct {
	ID            string     `json:"id"`
	SessionID     string     `json:"session_id,omitzero"`
	TurnID        string     `json:"turn_id,omitzero"`
	InvocationID  string     `json:"invocation_id,omitzero"`
	SubagentRunID string     `json:"subagent_run_id,omitzero"`
	ComponentType string     `json:"component_type"`
	ComponentName string     `json:"component_name,omitzero"`
	SourcePath    string     `json:"source_path,omitzero"`
	SourceHash    string     `json:"source_hash,omitzero"`
	Phase         string     `json:"phase,omitzero"`
	TokenEstimate int        `json:"token_estimate,omitzero"`
	Evidence      string     `json:"evidence,omitzero"`
	Confidence    Confidence `json:"confidence"`
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
