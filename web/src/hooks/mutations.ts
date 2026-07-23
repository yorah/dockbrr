import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { notify } from "@/lib/notify";
import { apiFetch, ApiError } from "@/api/client";
import { keys } from "@/api/keys";
import type { Scope, SelfUpdate } from "@/api/types";
import { clearDismissedUpdate } from "@/hooks/useDismissedUpdate";
import { selfUpdateErrorMessage } from "@/lib/selfUpdate";

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
      apiFetch<{ job_id: number; self_update?: boolean }>(`/api/updates/${v.id}/apply`, {
        method: "POST",
        body: { scope: v.scope },
      }),
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
// A 409 means a scan-run is already in flight (buttons are disabled, so this
// is only a race). Swallow it; surface anything else.
const scanError = (e: unknown) => {
  if (e instanceof ApiError && e.status === 409) return;
  notify.error(e instanceof Error ? e.message : "Check failed");
};

// Global sweep: scope "all". Progress + completion arrive over SSE; the
// scan-run store drives the UI, so there is no onSuccess refetch here.
export function useScanAll() {
  return useMutation({
    mutationFn: () => apiFetch<{ running: boolean; total: number }>("/api/scan", { method: "POST" }),
    onError: scanError,
  });
}

// Abort the in-flight scan-run (DELETE /api/scan -> 204). The scan-run store
// clears from the SSE scan_finished the abort triggers, so there is no
// onSuccess state change here.
export function useScanAbort() {
  return useMutation({
    mutationFn: () => apiFetch<void>("/api/scan", { method: "DELETE" }),
    onError: scanError,
  });
}

// Scoped sweep of one project (single request, not a per-service fan-out).
export function useProjectScan() {
  return useMutation({
    mutationFn: (projectId: number) =>
      apiFetch<{ running: boolean; total: number }>("/api/scan", { method: "POST", body: { project_id: projectId } }),
    onError: scanError,
  });
}

// Single-service check, routed through the same scan-run.
export function useServiceCheck() {
  return useMutation({
    mutationFn: (serviceId: number) =>
      apiFetch<{ running: boolean; total: number }>(`/api/services/${serviceId}/check`, { method: "POST" }),
    onError: scanError,
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
    onSuccess: (data) => {
      qc.setQueryData(keys.selfUpdate, data);
      clearDismissedUpdate();
      const msg = selfUpdateErrorMessage(data.error_kind);
      if (msg) notify.error(msg);
    },
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
