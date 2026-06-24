// TS mirrors of the Go API response structs (json tags from internal/trace,
// internal/store/sqlite, internal/handoff, internal/liveevent, internal/httpapi).
// Field names match Go json tags exactly; optional fields (omitzero/omitempty) are ?.

export interface Page<T> {
  items:       T[]
  total:       number
  limit:       number
  offset:      number
  next_offset: number
}

export interface Tokens {
  input_tokens?:            number
  output_tokens?:           number
  cache_read_tokens?:       number
  cache_write_tokens?:      number
  cache_write_long_tokens?: number
}

export interface Session {
  id:                string
  provider:          string
  external_id?:      string
  title?:            string
  project_id?:       string
  project_name?:     string
  project_path?:     string
  transcript_path:   string
  started_at?:       string
  ended_at?:         string
  status:            string
  turn_count:        number
  tool_call_count:   number
  tokens:            Tokens
  is_subagent?:         boolean
  parent_external_id?:  string
  parent_session_id?:   string
  parent_tool_use_id?: string
  workflow_run_id?:  string
  subagent_kind?:    string
  agent_type?:       string
  subagent_count?:   number
}

export interface ToolCall {
  id:           string
  turn_id:      string
  session_id:   string
  call_index:   number
  kind:         string
  name:         string
  mcp_server?:  string
  mcp_tool?:    string
  use_id?:            string
  invocation_id?:     string
  raw_use_event_id?:  string
  raw_result_event_id?: string
  input?:       string
  output?:      string
  output_bytes?: number
  status:       string
  error?:       string
  started_at?:  string
  ended_at?:    string
  duration_ms?: number
}

// One individual tool-call instance behind a ToolListItem aggregate (the
// drill-down row from GET /v1/tool-calls). Carries the call's status/error/output
// plus the session context that answers "where did this happen".
export interface ToolCallListItem {
  id:             string
  session_id:     string
  turn_id:        string
  turn_index:     number
  kind:           string
  name:           string
  mcp_server?:    string
  status:         string
  error?:         string
  input?:         string
  output?:        string
  output_bytes?:  number
  duration_ms?:   number
  started_at?:    string
  session_title?: string
  project_name?:  string
  source_id:      string
}

export interface Turn {
  id:               string
  provider:         string
  session_id:       string
  index:            number
  user_message?:    string
  assistant_final?: string
  started_at?:      string
  ended_at?:        string
  duration_ms?:     number
  status:           string
  invocation_count: number
  tool_call_count:  number
  tokens:           Tokens
  tool_calls?:      ToolCall[]
}

export interface LiveSessionItem {
  source_id:           string
  provider:            string
  session_id:          string
  external_session_id?: string
  title?:              string
  project_id?:         string
  project_name?:       string
  project_path?:       string
  transcript_path?:    string
  session_status:      string
  last_turn_id?:       string
  last_turn_status?:   string
  current_status:      string
  started_at?:         string
  last_activity_at?:   string
  turn_count:          number
  tool_call_count:     number
  last_event_type?:    string
  live_updated_at?:    string
}

export interface LiveEvent {
  event_id?:           string
  type:                string
  raw_event_name?:     string
  at?:                 string
  provider?:           string
  source_id?:          string
  session_id?:         string
  external_session_id?: string
  project_id?:         string
  project_name?:       string
  project_path?:       string
  transcript_path?:    string
  status?:             string
  reason?:             string
  file?:               string
  turn_count?:         number
  raw_event_count?:    number
  size_bytes?:         number
}

export interface Summary {
  sessions:               number
  turns:                  number
  invocations:            number
  tool_calls:             number
  input_tokens:           number
  output_tokens:          number
  cache_read_tokens:      number
  cache_write_tokens:     number
  cache_write_long_tokens: number
  parse_errors:           number
  raw_events:             number
}

export interface SourcePointer {
  provider:        string
  session_id:      string
  turn_id?:        string
  tool_call_id?:   string
  file?:           string
  use_event_id?:   string
  result_event_id?: string
}

export interface AgentRun {
  id:           string
  tool:         string
  type?:        string
  description?: string
  prompt?:      string
  result?:      string
  status:       string
  error?:       string
  started_at?:  string
  ended_at?:    string
  duration_ms?: number
  output_bytes?: number
  source:       SourcePointer
}

export interface EvidenceItem {
  id:         string
  type:       string
  claim:      string
  confidence: string
  source:     SourcePointer
}

export interface HandoffManifest {
  schema:                   string
  generated_at:             string
  session_id:               string
  external_session_id?:     string
  title?:                   string
  provider:                 string
  project?:                 string
  transcript_path?:         string
  workflow_status:          string
  turns:                    number
  agent_runs:               number
  completed_agent_runs:     number
  failed_agent_runs:        number
  interrupted_agent_runs:   number
  incomplete_agent_runs:    number
  rejected_agent_runs:      number
  final_synthesis_present:  boolean
  ambiguous_session_ids?:   string[]
}

export interface HandoffPackage {
  manifest:   HandoffManifest
  session:    Session
  turns:      Turn[]
  agent_runs: AgentRun[]
  evidence:   EvidenceItem[]
  digest:     string // markdown
}

export interface SessionDetail {
  session:               Session
  turns:                 Turn[]
  ambiguous_session_ids: string[] | null
}

export interface SearchResult {
  kind:       string
  id:         string
  provider:   string
  session_id: string
  turn_id?:   string
  snippet:    string
}

export interface SearchResponse {
  query:   string
  results: SearchResult[]
}

export interface ApiErrorBody {
  error: { code: string; message: string }
}

// ── analytics list items (mirrors internal/store/sqlite listings.go) ───────────

export interface ProjectListItem {
  id:             string
  source_id:      string
  name:           string
  path?:          string
  session_count:  number
  turn_count:     number
  tool_call_count: number
  last_activity?: string
}

export interface ToolListItem {
  kind:            string
  name:            string
  mcp_server?:     string
  call_count:      number
  turn_count:      number
  failed_count:    number
  rejected_count:  number
  last_used_at?:   string
}

export interface ModelListItem {
  provider:               string
  model:                  string
  call_count:             number
  turn_count:             number
  input_tokens:           number
  output_tokens:          number
  cache_read_tokens:      number
  cache_write_tokens:     number
  cache_write_long_tokens: number
  last_used_at?:          string
}

export interface MCPListItem {
  source_id:            string
  server:               string
  call_count:           number
  tool_count:           number
  turn_count:           number
  last_used_at?:        string
  availability_observed: number
  declared:             boolean
  scope?:               string
  config_path?:         string
}

export interface SkillListItem {
  source_id:          string
  name:               string
  scope?:             string
  source_path?:       string
  description?:       string
  version?:           string
  argument_hint?:     string
  user_invocable?:    boolean
  triggers?:          unknown
  allowed_tools?:     unknown
  tools?:             unknown
  compatibility?:     string
  license?:           string
  installed:          boolean
  inferred_used_count: number
  last_used_at?:      string
}

// ── daemon status (mirrors internal/runtime/state.go + internal/httpapi/daemon.go) ─

export interface DaemonCounters {
  full_runs:                number
  full_failures:            number
  file_runs:                number
  file_failures:            number
  unmapped_files:           number
  ingest_auto_dropped_total?: number
  emit_dropped_total?:       number
}

export interface DaemonBackpressure {
  persist_queue_full_total:        number
  sse_slow_subscriber_dropped_total: number
  spool_dropped_total:             number
  spool_dropped_bytes:             number
  durable_lag:                     number
  persist_queue_len:               number
  live_sessions:                   number
}

export interface DaemonStatus {
  state:             string
  sources:           string[]
  interval:          string
  debounce:          string
  started_at?:       string
  last_full_at?:     string
  last_full_reason?: string
  last_file_at?:     string
  last_file_path?:   string
  pending_files:     number
  watched_paths:     number
  counters:          DaemonCounters
  backpressure:      DaemonBackpressure
}

// ── config (mirrors internal/httpapi/handlers_ops.go handleConfig) ─────────────

export interface ConfigSetting {
  value:    string
  source:   string
  editable: boolean
}

export interface ConfigResponse {
  home_dir:       string
  config_dir:     string
  data_dir:       string
  api_token_path: string
  api_token_set:  boolean
  redact:         string
  roots:          Record<string, string[]>
  settings:       Record<string, ConfigSetting>
}

// ── sources (mirrors internal/httpapi/handlers_ops.go handleSources) ──────────

export interface SourceRoot {
  source: string
  root:   string
  exists: boolean
}
