import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { notify } from "@/lib/notify";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import type { Scope, SelfUpdate } from "@/api/types";

const invalidate = (qc: QueryClient, ...ks: readonly (readonly unknown[])[]) =>
  Promise.all(ks.map((queryKey) => qc.invalidateQueries({ queryKey })));

// Auth transitions (login/setup/logout) must refetch `me`/`setup-status` so
// AuthGate's mounted observers flip screens immediately, then drop every
// other cached query so the next session doesn't see stale per-user data.
// A bare qc.clear() is unreliable here: when several other queries are
// cleared in the same batch, the me/setup-status observers can fail to
// re-fetch, leaving AuthGate stuck showing the previous screen.
const resetAuthCaches = async (qc: QueryClient) => {
  await invalidate(qc, keys.me, keys.setupStatus);
  qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "me" && q.queryKey[0] !== "setup-status" });
};

const toastError = (e: unknown) => notify.error(e instanceof Error ? e.message : "Request failed");

export function useApply() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: number; scope: Scope }) =>
      apiFetch<{ job_id: number }>(`/api/updates/${v.id}/apply`, { method: "POST", body: { scope: v.scope } }),
    onSuccess: () => invalidate(qc, keys.updates, keys.projects),
    onError: toastError,
  });
}
export function useLifecycle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { serviceId: number; action: "start" | "stop" | "restart" }) =>
      apiFetch<{ job_id: number }>(`/api/services/${v.serviceId}/lifecycle`, {
        method: "POST",
        body: { action: v.action },
      }),
    onSuccess: () => invalidate(qc, keys.projects),
    onError: toastError,
  });
}
export function useRemoveContainer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (serviceId: number) =>
      apiFetch<{ job_id: number }>(`/api/services/${serviceId}/remove`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.projects),
    onError: toastError,
  });
}
export function useDismiss() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => apiFetch(`/api/updates/${id}/dismiss`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.updates, keys.projects),
    onError: toastError,
  });
}
export function useRestore() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => apiFetch(`/api/updates/${id}/restore`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.updates, keys.projects),
    onError: toastError,
  });
}
export function useCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (serviceId: number) => apiFetch(`/api/services/${serviceId}/check`, { method: "POST" }),
    onSuccess: async () => {
      await invalidate(qc, keys.updates, keys.projects);
      notify.success("Check complete");
    },
    onError: (e) => notify.error(e instanceof Error ? e.message : "Check failed"),
  });
}
// useClearJobs purges the finished job history (success/failed/canceled) and
// their logs. Queued and running jobs are kept by the backend, so the list is
// never emptied out from under an in-flight job.
export function useClearJobs() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<{ deleted: number }>("/api/jobs", { method: "DELETE" }),
    onSuccess: async (res) => {
      await invalidate(qc, keys.jobs);
      notify.success(`Cleared ${res.deleted} finished job${res.deleted === 1 ? "" : "s"}`);
    },
    onError: (e) => notify.error(e instanceof Error ? e.message : "Clear failed"),
  });
}
export function useCheckAll() {
  const qc = useQueryClient();
  return useMutation({
    // Scoped (e.g. one project): fan out one check per service. The global
    // sweep uses useScanAll instead, which also stamps last_check_all.
    mutationFn: (serviceIds: number[]) =>
      Promise.all(serviceIds.map((id) => apiFetch(`/api/services/${id}/check`, { method: "POST" }))),
    onSuccess: async (_res, ids) => {
      await invalidate(qc, keys.updates, keys.projects);
      notify.success(`Checked ${ids.length} service${ids.length > 1 ? "s" : ""}`);
    },
    onError: (e) => notify.error(e instanceof Error ? e.message : "Check failed"),
  });
}
// useScanAll runs a full detection sweep via the single POST /api/scan
// endpoint (unlike useCheckAll's per-service fan-out). The backend stamps
// last_check_all and publishes a "scanned" SSE event, so the dashboard's
// "Last scan" tile updates immediately, the reason this exists separately
// from useCheckAll, whose per-service checks never touch last_check_all.
export function useScanAll() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch("/api/scan", { method: "POST" }),
    onSuccess: async () => {
      await invalidate(qc, keys.status, keys.updates, keys.projects);
      notify.success("Scan complete");
    },
    onError: (e) => notify.error(e instanceof Error ? e.message : "Scan failed"),
  });
}
export function useRollback() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (jobId: number) => apiFetch<{ job_id: number }>(`/api/jobs/${jobId}/rollback`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.updates, keys.projects),
    onError: toastError,
  });
}
export function useToggleProjectAuto() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: number; enabled: boolean }) =>
      apiFetch(`/api/projects/${v.id}/auto-update`, { method: "PUT", body: { enabled: v.enabled } }),
    onSuccess: () => invalidate(qc, keys.projects),
    onError: toastError,
  });
}
export function useToggleServiceAuto() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: number; enabled: boolean | null }) =>
      apiFetch(`/api/services/${v.id}/auto-update`, { method: "PUT", body: { enabled: v.enabled } }),
    onSuccess: () => invalidate(qc, keys.projects),
    onError: toastError,
  });
}
export function useSaveSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: Record<string, string>) => apiFetch("/api/settings", { method: "PUT", body: patch }),
    onSuccess: () => invalidate(qc, keys.settings),
    // log_level is mirrored by GET /api/logs/config, which backs the Logs page's
    // (controlled) level select. Refreshing it here rather than at the call site
    // means no page that saves log_level can forget to. On settle rather than on
    // success, so the select also re-syncs to the persisted level when the save
    // FAILS, rolling back the Logs page's optimistic cache write.
    onSettled: (_res, _err, patch) => {
      if ("log_level" in patch) return invalidate(qc, keys.logConfig);
    },
    onError: toastError,
  });
}
export function useAddRegistry() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { host: string; username: string; password: string }) =>
      apiFetch("/api/registries", { method: "POST", body: v }),
    onSuccess: () => invalidate(qc, keys.registries),
    onError: toastError,
  });
}
export function useDeleteRegistry() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (host: string) => apiFetch(`/api/registries/${encodeURIComponent(host)}`, { method: "DELETE" }),
    onSuccess: () => invalidate(qc, keys.registries),
    onError: toastError,
  });
}
export function useChangePassword() {
  return useMutation({
    mutationFn: (v: { current: string; new: string }) =>
      apiFetch("/api/auth/password", { method: "POST", body: v }),
  });
}
export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { name: string; working_dir: string; config_files: string[] }) =>
      apiFetch<{ id: number; name: string }>("/api/projects", { method: "POST", body: v }),
    onSuccess: () => invalidate(qc, keys.projects),
    onError: toastError,
  });
}
// useApplySelfUpdate triggers the watchtower-style self-update (Task 12's
// POST /api/updates/self/apply), which enqueues a self_update job that swaps
// the running dockbrr binary/container. The job list (keys.jobs) is
// invalidated so ApplyPanel/the jobs screen pick up the new job; the browser
// connection is expected to drop mid-job when the process restarts.
// useCheckForUpdates forces a fresh GitHub check (bypassing the 6h cache TTL)
// and writes the verdict into the shared keys.selfUpdate cache, so the sidebar
// UpdateNotice and the Settings build card both reflect it immediately.
export function useCheckForUpdates() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<SelfUpdate>("/api/updates/self?force=true"),
    onSuccess: (data) => qc.setQueryData(keys.selfUpdate, data),
    onError: toastError,
  });
}

export function useApplySelfUpdate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch<{ job_id: number }>("/api/updates/self/apply", { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.jobs),
    onError: toastError,
  });
}
export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { username: string; password: string }) =>
      apiFetch("/api/auth/login", { method: "POST", body: v }),
    onSuccess: () => resetAuthCaches(qc),
  });
}
export function useSetup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { username: string; password: string }) =>
      apiFetch("/api/setup", { method: "POST", body: v }),
    onSuccess: () => resetAuthCaches(qc),
  });
}
export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch("/api/auth/logout", { method: "POST" }),
    onSuccess: () => resetAuthCaches(qc),
  });
}
