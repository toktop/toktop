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
  input_tokens:            number
  output_tokens:           number
  cache_read_tokens:       number
  cache_write_tokens:      number
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
  is_subagent?:      boolean
  parent_session_id?: string
  parent_tool_use_id?: string
  workflow_run_id?:  string
  subagent_kind?:    string
  agent_type?:       string
  subagent_count?:   number
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
  // tool_calls / invocations / components omitted; add when a page needs them
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
  cache_write_long_tokens?: number
  parse_errors:           number
  raw_events:             number
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
}

export interface EvidenceItem {
  id:         string
  type:       string
  claim:      string
  confidence: string
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
