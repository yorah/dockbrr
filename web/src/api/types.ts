export interface Service {
  id: number;
  name: string;
  image_ref: string;
  current_digest: string;
  state: string;
  pinned: boolean;
  drifted: boolean;
  healthcheck: boolean;
  auto_update_enabled: boolean | null;
  check_status: string;
  last_checked: string;
  /** Reverse-resolved running release for a floating tag with no pending
   * update; "" or absent when unknown. Always emitted by the API. */
  current_version?: string;
}
export interface Project {
  id: number;
  name: string;
  kind: string;
  working_dir: string;
  auto_update_enabled: boolean;
  unmanaged: boolean;
  auto_named: boolean;
  services: Service[];
}
export interface Update {
  id: number;
  service_id: number;
  from_digest: string;
  to_digest: string;
  from_version: string;
  to_version: string;
  tag: string;
  severity: "major" | "minor" | "patch" | "digest-only" | string;
  changelog_url: string;
  changelog_text: string;
  changelog_status?: string;
  status: string;
  detected_at: string;
}
export interface Settings {
  poll_interval_seconds: string;
  scan_on_start: string;
  concurrency: string;
  health_timeout_seconds: string;
  health_poll_seconds: string;
  cache_ttl_seconds: string;
  write_back_compose: string;
  auto_remove_gone: string;
  default_auto_update_enabled: string;
  gone_grace_seconds: string;
  job_retention_days: string;
  github_token_set: boolean;
  restart_required: string[];
  /** Server-side default for each non-secret setting; lets the UI dim untouched fields. */
  defaults: Record<string, string>;
}
export interface Registry {
  ID: number;
  RegistryHost: string;
  Username: string;
  CreatedAt: string;
}
export interface Job {
  id: number;
  type: string;
  status: string;
  scope: string;
  exit_code: number | null;
  error: string;
}
// JobRow is the /api/jobs (list) shape: the base Job fields plus the extra
// columns only the Jobs history screen needs. Kept separate from Job so
// other consumers (ApplyPanel, useJob) don't have to carry fields they don't
// use.
export interface JobRow extends Job {
  project_id: number | null;
  service_id: number | null;
  requested_by: string;
  created_at: string;
  finished_at: string;
  // Resolved display names; absent/"" when the project/service was deleted
  // or the job has no target (e.g. sync).
  project_name?: string;
  service_name?: string;
}
export interface ServiceEvent {
  id: number;
  kind: string;
  ref_job_id: number | null;
  from_digest: string;
  to_digest: string;
  message: string;
  created_at: string;
  changelog_url?: string;
  changelog_text?: string;
}
export interface SystemStatus {
  last_check_all: string;
  // When the scheduler will next run a check-all (RFC3339). Absent when the
  // scheduler isn't running. Never infer it from last_check_all + poll, a
  // manual scan stamps last_check_all without resetting the ticker.
  next_check_all?: string;
  poll_interval_seconds: number;
  docker_reachable: boolean;
  version: string;
}
export interface SelfUpdate {
  current?: string;
  latest?: string;
  html_url?: string;
  update_available: boolean;
  checked_at?: string;
}
export interface CommandPreview { pull: string; up: string }
export interface Me { username: string }
export interface SetupStatus { needs_setup: boolean }
export type Scope = "service" | "project";
export interface LogLine { stream: string; line: string }
export interface ComposeFile { path: string; content: string; error?: string }
export interface ComposeFiles { files: ComposeFile[] }
export interface LogConfig { path: string; level: string; maxSizeMB: number; maxBackups: number }
export interface LogFile { name: string; modified: string; size: number }
export interface DockerInfo {
  reachable: boolean;
  /** Absent when the daemon is unreachable or the version probe failed. */
  version?: string;
  api_version?: string;
}
export interface AuthInfo {
  username: string;
  method: string;
}
export interface SystemInfo {
  version: string;
  /** Empty when built without VCS stamps (go run, -buildvcs=false). */
  commit: string;
  commit_dirty: boolean;
  /** RFC3339, or empty when built without VCS stamps. */
  build_date: string;
  go_version: string;
  platform: string;
  /** RFC3339 process start; uptime is derived client-side from this. */
  started_at?: string;
  docker: DockerInfo;
  db_path: string;
  bind_addr: string;
  data_dir: string;
  auth: AuthInfo;
}
