import { useMemo } from "react";
import { useJobs, useProjects, useUpdates } from "./queries";
import type { JobRow, Project, Update } from "@/api/types";

export type Dot = "red" | "amber" | "green";
export interface ProjectHealth {
  updates: number;
  dot: Dot;
}

/**
 * Sidebar health per project, derived from data the app already has.
 *
 * red:   the project's most recent job failed
 * amber: the project has open updates
 * green: otherwise
 *
 * `useJobs()` is capped at 100 rows, so a project whose last job has aged out
 * of that window shows no red dot. Deliberate: the alternative is a per-project
 * endpoint, and a stale failure is not worth one.
 */
export function projectHealth(projects: Project[], updates: Update[], jobs: JobRow[]): Map<number, ProjectHealth> {
  const open = new Set<number>(); // service ids with an open update
  for (const u of updates) {
    if (u.status === "available") open.add(u.service_id);
  }

  // created_at is RFC3339 from the API, so string compare orders it correctly.
  const latest = new Map<number, JobRow>();
  for (const j of jobs) {
    if (j.project_id == null) continue;
    const prev = latest.get(j.project_id);
    if (!prev || j.created_at > prev.created_at) latest.set(j.project_id, j);
  }

  const out = new Map<number, ProjectHealth>();
  for (const p of projects) {
    const count = p.services.reduce((n, s) => n + (open.has(s.id) ? 1 : 0), 0);
    const failed = latest.get(p.id)?.status === "failed";
    out.set(p.id, { updates: count, dot: failed ? "red" : count > 0 ? "amber" : "green" });
  }
  return out;
}

export function useProjectHealth(): { projects: Project[]; health: Map<number, ProjectHealth> } {
  const projects = useProjects();
  const updates = useUpdates();
  const jobs = useJobs();
  const health = useMemo(
    () => projectHealth(projects.data ?? [], updates.data ?? [], jobs.data ?? []),
    [projects.data, updates.data, jobs.data],
  );
  return { projects: projects.data ?? [], health };
}
