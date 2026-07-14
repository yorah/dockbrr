import type { Scope } from "./types";

export const keys = {
  projects: ["projects"] as const,
  updates: ["updates"] as const,
  // Nested under the ["updates"] prefix on purpose: every existing
  // invalidateQueries({ queryKey: keys.updates }) call then refreshes this too.
  lastApplied: ["updates", "last-applied"] as const,
  settings: ["settings"] as const,
  registries: ["registries"] as const,
  me: ["me"] as const,
  setupStatus: ["setup-status"] as const,
  status: ["status"] as const,
  systemInfo: ["system-info"] as const,
  jobs: ["jobs"] as const,
  job: (id: number) => ["job", id] as const,
  serviceEvents: (id: number) => ["service-events", id] as const,
  updatePreview: (id: number, scope: Scope) => ["update-preview", id, scope] as const,
  projectCompose: (id: number) => ["project-compose", id] as const,
  logConfig: ["logs", "config"] as const,
  logFiles: ["logs", "files"] as const,
};
