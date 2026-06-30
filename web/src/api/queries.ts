import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiGet, apiPost } from "./client"
import type {
  ActivityBucket,
  ConfigResponse,
  DaemonStatus,
  HandoffPackage,
  LiveSessionItem,
  MCPListItem,
  ModelListItem,
  Page,
  ProjectListItem,
  SearchResponse,
  Session,
  SessionDetail,
  SkillListItem,
  SourceRoot,
  Summary,
  ToolCallListItem,
  ToolListItem,
} from "./types"

type Filter = Record<string, string | number | boolean | undefined>

export const useLiveStatus = (f?: Filter) =>
  useQuery({
    queryKey: ["status", f],
    queryFn:  () => apiGet<Page<LiveSessionItem>>("/status", f),
  })

export const useSessions = (f?: Filter) =>
  useQuery({
    queryKey: ["sessions", f],
    queryFn:  () => apiGet<Page<Session>>("/sessions", f),
  })

export const useSession = (id: string) =>
  useQuery({
    queryKey: ["session", id],
    queryFn:  () => apiGet<SessionDetail>(`/sessions/${id}`),
    enabled:  id.length > 0,
  })

export const useHandoff = (id: string) =>
  useQuery({
    queryKey: ["handoff", id],
    queryFn:  () => apiGet<HandoffPackage>(`/sessions/${id}/handoff`),
    enabled:  id.length > 0,
  })

export const useSummary = (f?: Filter) =>
  useQuery({
    queryKey: ["summary", f],
    queryFn:  () => apiGet<Summary>("/summary", f),
  })

interface SearchParams {
  q:          string
  kind?:      string
  subagents?: boolean
}

export const useSearch = ({ q, kind, subagents }: SearchParams) =>
  useQuery({
    queryKey: ["search", q, kind, subagents],
    queryFn:  () => apiGet<SearchResponse>("/search", {
      q,
      kind:      kind      || undefined,
      subagents: subagents ? 1 : undefined,
    }),
    enabled:  q.length > 0,
  })

// ── analytics list hooks ──────────────────────────────────────────────────────

// Time-bucketed activity series. `bucket` is a duration (e.g. "5m", "1h"); the
// rest of the filter (since/until/sources/…) rides the shared Filter shape.
export const useActivity = (f?: Filter) =>
  useQuery({
    queryKey: ["activity", f],
    queryFn:  () => apiGet<ActivityBucket[]>("/activity", f),
  })

export const useProjects = (f?: Filter) =>
  useQuery({
    queryKey: ["projects", f],
    queryFn:  () => apiGet<ProjectListItem[]>("/projects", f),
  })

export const useTools = (f?: Filter) =>
  useQuery({
    queryKey: ["tools", f],
    queryFn:  () => apiGet<ToolListItem[]>("/tools", f),
  })

interface ToolCallParams {
  name:        string
  kind?:       string
  mcp_server?: string
  status?:     string
  // since/until scope the drill-down to the same time window as the aggregate it
  // was opened from, so the listed calls match the windowed failed/rejected count.
  since?:      string
  until?:      string
  limit?:      number
}

// useToolCalls drills into one tool's individual calls (GET /v1/tool-calls).
// Gated by `enabled` so it only fires while a drill-down is open.
export const useToolCalls = (p: ToolCallParams, enabled: boolean) =>
  useQuery({
    queryKey: ["tool-calls", p],
    queryFn:  () => apiGet<ToolCallListItem[]>("/tool-calls", {
      name:       p.name,
      kind:       p.kind       || undefined,
      mcp_server: p.mcp_server || undefined,
      status:     p.status     || undefined,
      since:      p.since      || undefined,
      until:      p.until      || undefined,
      limit:      p.limit,
    }),
    enabled,
  })

export const useModels = (f?: Filter) =>
  useQuery({
    queryKey: ["models", f],
    queryFn:  () => apiGet<ModelListItem[]>("/models", f),
  })

export const useMcps = (f?: Filter) =>
  useQuery({
    queryKey: ["mcps", f],
    queryFn:  () => apiGet<MCPListItem[]>("/mcps", f),
  })

export const useUnusedMcps = () =>
  useQuery({
    queryKey: ["mcps", "unused"],
    queryFn:  () => apiGet<MCPListItem[]>("/mcps/unused"),
  })

export const useSkills = (f?: Filter) =>
  useQuery({
    queryKey: ["skills", f],
    queryFn:  () => apiGet<SkillListItem[]>("/skills", f),
  })

export const useUnusedSkills = () =>
  useQuery({
    queryKey: ["skills", "unused"],
    queryFn:  () => apiGet<SkillListItem[]>("/skills/unused"),
  })

// ── daemon / config / sources hooks ──────────────────────────────────────────

export const useDaemon = () =>
  useQuery({
    queryKey:        ["daemon"],
    queryFn:         () => apiGet<DaemonStatus>("/daemon"),
    refetchInterval: 5_000,
  })

export const useConfig = () =>
  useQuery({
    queryKey: ["config"],
    queryFn:  () => apiGet<ConfigResponse>("/config"),
  })

export const useSources = () =>
  useQuery({
    queryKey: ["sources"],
    queryFn:  () => apiGet<SourceRoot[]>("/sources"),
  })

interface SetConfigVars {
  key:   string
  value: string
}

interface SetConfigResult {
  key:      string
  value:    string
  reloaded: boolean
}

export function useSetConfig() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (vars: SetConfigVars) =>
      apiPost<SetConfigResult>("/config:set", vars),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["config"] })
    },
  })
}
