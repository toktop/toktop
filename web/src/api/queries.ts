import { useQuery } from "@tanstack/react-query"
import { apiGet } from "./client"
import type {
  HandoffPackage,
  LiveSessionItem,
  Page,
  SearchResponse,
  Session,
  SessionDetail,
  Summary,
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

export const useSearch = (q: string) =>
  useQuery({
    queryKey: ["search", q],
    queryFn:  () => apiGet<SearchResponse>("/search", { q }),
    enabled:  q.length > 0,
  })
