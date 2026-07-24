import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import type {
  Project, Update, Settings, Registry, Me, SetupStatus,
  ServiceEvent, CommandPreview, Job, JobRow, Scope, SystemStatus, SelfUpdate, ComposeFiles, SystemInfo,
} from "@/api/types";

// Dashboard freshness is push-driven now: the global SSE stream (useEventStream)
// invalidates these queries on detected / job_finished / reconciled events, so no
// polling interval is needed here (see audit A10).
export const useProjects = () =>
  useQuery({ queryKey: keys.projects, queryFn: () => apiFetch<Project[]>("/api/projects") });
export const useUpdates = () =>
  useQuery({ queryKey: keys.updates, queryFn: () => apiFetch<Update[]>("/api/updates") });
export const useLastApplied = () =>
  useQuery({ queryKey: keys.lastApplied, queryFn: () => apiFetch<Update[]>("/api/updates/last-applied") });
export const useSettings = () =>
  useQuery({ queryKey: keys.settings, queryFn: () => apiFetch<Settings>("/api/settings") });
export const useRegistries = () =>
  useQuery({ queryKey: keys.registries, queryFn: () => apiFetch<Registry[]>("/api/registries") });
export const useMe = () =>
  useQuery({ queryKey: keys.me, queryFn: () => apiFetch<Me>("/api/auth/me") });
export const useSetupStatus = () =>
  useQuery({ queryKey: keys.setupStatus, queryFn: () => apiFetch<SetupStatus>("/api/setup/status") });
export const useStatus = () =>
  useQuery({ queryKey: keys.status, queryFn: () => apiFetch<SystemStatus>("/api/status"), refetchInterval: 60_000 });
export const useSelfUpdate = () =>
  useQuery({
    queryKey: keys.selfUpdate,
    queryFn: () => apiFetch<SelfUpdate>("/api/updates/self"),
    refetchInterval: 6 * 60 * 60 * 1000,
  });
export const useSystemInfo = () =>
  useQuery({ queryKey: keys.systemInfo, queryFn: () => apiFetch<SystemInfo>("/api/system/info") });
export const useJobs = (limit = 100) =>
  useQuery({ queryKey: keys.jobs, queryFn: () => apiFetch<JobRow[]>(`/api/jobs?limit=${limit}`) });
export const useServiceEvents = (id: number) =>
  useQuery({ queryKey: keys.serviceEvents(id), queryFn: () => apiFetch<ServiceEvent[]>(`/api/services/${id}/events`) });
export const useUpdatePreview = (id: number, scope: Scope, enabled = true) =>
  useQuery({
    queryKey: keys.updatePreview(id, scope), enabled,
    queryFn: () => apiFetch<CommandPreview>(`/api/updates/${id}/preview?scope=${scope}`),
  });
export const useProjectCompose = (id: number, enabled = true) =>
  useQuery({
    queryKey: keys.projectCompose(id), enabled,
    queryFn: () => apiFetch<ComposeFiles>(`/api/projects/${id}/compose`),
  });
// Terminal + failure job-status vocabularies (store/jobs.go). Shared so the
// single-job panel, the bulk panel, and the poll interval never drift.
export const TERMINAL_JOB_STATUSES: ReadonlySet<string> = new Set(["success", "failed", "canceled"]);
export const FAILED_JOB_STATUSES: ReadonlySet<string> = new Set(["failed", "canceled"]);

// Shared query options for a single job, so useJob (one job) and useQueries
// (a bulk apply's N jobs) build identical keys/fetchers/polling. Terminal job
// statuses per store/jobs.go: success|failed|canceled. Stop polling once the
// job finishes, keep polling while queued|running.
export function jobQueryOptions(id: number) {
  return {
    queryKey: keys.job(id),
    queryFn: () => apiFetch<Job>(`/api/jobs/${id}`),
    refetchInterval: (q: { state: { data?: Job } }) => {
      const s = q.state.data?.status;
      return s && TERMINAL_JOB_STATUSES.has(s) ? false : 1500;
    },
  };
}
export const useJob = (id: number, enabled = true) =>
  useQuery({ ...jobQueryOptions(id), enabled });
// Plain fetch helper (not a query hook): the logs drawer (Task 8) fetches on
// demand rather than subscribing, so there is no cache key to register here.
export function fetchServiceLogs(serviceId: number, tail = 500): Promise<{ logs: string }> {
  return apiFetch<{ logs: string }>(`/api/services/${serviceId}/logs?tail=${tail}`);
}
