import { useMemo } from "react";
import { useProjects, useUpdates, useLastApplied } from "./queries";
import { isStopped } from "@/components/StatusBadge";
import type { Project, Service, Update } from "@/api/types";

export interface FilterState {
  onlyUpdates: boolean;
  project: string;
  status: string;
  search: string;
  showRemoved: boolean;
}

export type Row =
  | { kind: "project"; project: Project }
  | { kind: "service"; project: Project; service: Service; update?: Update; lastApplied?: Update }
  | { kind: "loose"; count: number };

const openStatuses = new Set(["available"]);

export function joinRows(
  projects: Project[],
  updates: Update[],
  f: FilterState,
  lastApplied: Update[] = [],
): Row[] {
  const byService = new Map<number, Update>();
  for (const u of updates) {
    if (u.status === "available") byService.set(u.service_id, u);
    else if (u.status === "dismissed" && !byService.has(u.service_id)) byService.set(u.service_id, u);
  }
  // Presentational only. The filters below never read it, so an applied update
  // never makes a service look like it has a pending one.
  const appliedByService = new Map<number, Update>();
  for (const u of lastApplied) appliedByService.set(u.service_id, u);

  const rows: Row[] = [];
  for (const p of projects) {
    if (f.project && String(p.id) !== f.project) continue;
    const svcRows: Row[] = [];
    for (const s of p.services) {
      // "gone" rows are hidden unless Show removed is on OR the user is
      // explicitly filtering for gone.
      if (!f.showRemoved && s.state === "gone" && f.status !== "gone") continue;
      const update = byService.get(s.id);
      if (f.onlyUpdates && update?.status !== "available") continue;
      if (f.status === "pinned" && !s.pinned) continue;
      if (f.status === "update-available" && update?.status !== "available") continue;
      if (f.status === "up-to-date" && (update || s.pinned)) continue;
      if (f.status === "stopped" && !isStopped(s.state)) continue;
      if (f.status === "restarting" && s.state !== "restarting") continue;
      if (f.status === "gone" && s.state !== "gone") continue;
      if (f.search) {
        const q = f.search.toLowerCase();
        if (
          !s.name.toLowerCase().includes(q) &&
          !s.image_ref.toLowerCase().includes(q) &&
          !p.name.toLowerCase().includes(q)
        )
          continue;
      }
      svcRows.push({
        kind: "service",
        project: p,
        service: s,
        update,
        lastApplied: appliedByService.get(s.id),
      });
    }
    // Drop the project header whenever filtering leaves no visible services:
    // covers explicit filters (onlyUpdates/status/search) and the default
    // gone-hiding behavior (a project whose only services are Gone while
    // showRemoved is off).
    if (svcRows.length === 0) continue;
    rows.push({ kind: "project", project: p }, ...svcRows);
  }
  return rows;
}

export function useDashboardRows(filters: FilterState) {
  const projects = useProjects();
  const updates = useUpdates();
  const lastApplied = useLastApplied();
  const rows = useMemo(
    () => joinRows(projects.data ?? [], updates.data ?? [], filters, lastApplied.data ?? []),
    [projects.data, updates.data, filters, lastApplied.data],
  );
  const updatesByService = useMemo(() => {
    const m = new Map<number, Update>();
    for (const u of updates.data ?? []) if (openStatuses.has(u.status)) m.set(u.service_id, u);
    return m;
  }, [updates.data]);
  const lastAppliedByService = useMemo(() => {
    const m = new Map<number, Update>();
    for (const u of lastApplied.data ?? []) m.set(u.service_id, u);
    return m;
  }, [lastApplied.data]);
  return {
    rows,
    projects: projects.data ?? [],
    updates: updates.data ?? [],
    updatesByService,
    lastAppliedByService,
    // last-applied is decoration: a failure there must not blank the dashboard,
    // so it is deliberately excluded from isLoading / isError.
    isLoading: projects.isLoading || updates.isLoading,
    isError: projects.isError || updates.isError,
  };
}
