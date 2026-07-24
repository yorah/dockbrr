import { useCallback, useMemo, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { Filters } from "@/components/Filters";
import { ApplyAllButton, CheckAllButton } from "@/components/BulkActions";
import { DashboardStats } from "@/components/DashboardStats";
import { DashboardTable } from "@/components/DashboardTable";
import { ScanProgress } from "@/components/ScanProgress";
import { ReviewDrawer } from "@/components/ReviewDrawer";
import { ChangelogDrawer } from "@/components/ChangelogDrawer";
import { LogsDrawer } from "@/components/LogsDrawer";
import { ApplyPanel } from "@/components/ApplyPanel";
import { BulkApplyPanel } from "@/components/BulkApplyPanel";
import { useDashboardRows, type FilterState } from "@/hooks/useDashboardRows";
import { useStatus } from "@/hooks/queries";
import type { AppliedJob, Project, Service, Update } from "@/api/types";

interface Selected {
  update: Update;
  service: Service;
  project: Project;
}

export function ProjectRoute() {
  const { id } = useParams({ from: "/project/$id" });
  const [filters, setFilters] = useState<Omit<FilterState, "project">>({
    onlyUpdates: false,
    status: "",
    search: "",
    showRemoved: false,
  });
  const [selected, setSelected] = useState<Selected | null>(null);
  const [changelogFor, setChangelogFor] = useState<{ update: Update; service: Service } | null>(null);
  const [logsFor, setLogsFor] = useState<Service | null>(null);
  // Set by ReviewDrawer's onApplied / a row Apply / a project or global Apply-all,
  // so the live-log/health-gate panel can be opened for the enqueued job(s).
  type PanelState =
    | { kind: "single"; jobId: number }
    | { kind: "bulk"; jobs: AppliedJob[] }
    | null;
  const [panel, setPanel] = useState<PanelState>(null);

  // A batch of exactly 1 renders the plain single panel (no bulk chrome).
  const openBatch = (jobs: AppliedJob[]) =>
    setPanel(jobs.length === 1 ? { kind: "single", jobId: jobs[0].jobId } : { kind: "bulk", jobs });
  const closePanel = useCallback(() => setPanel(null), []);

  // The project filter is pinned to the route param: a status/search change
  // must never widen the scope back to every project. Memoized so joinRows'
  // useMemo (keyed on this object) doesn't recompute on every render.
  const scoped = useMemo<FilterState>(() => ({ ...filters, project: id }), [filters, id]);
  const { rows, projects, updates, updatesByService, isLoading, isError } = useDashboardRows(scoped);
  const statusQuery = useStatus();

  const project = projects.find((p) => String(p.id) === id);
  const projectUpdates = project
    ? updates.filter((u) => project.services.some((s) => s.id === u.service_id))
    : [];
  // Apply all must never reanimate a service the table hides by default,
  // exclude gone services regardless of the "Show removed" filter state.
  const applicableProjectUpdates = project
    ? projectUpdates.filter((u) => project.services.find((s) => s.id === u.service_id)?.state !== "gone")
    : [];
  const serviceNames = new Map<number, string>(
    (project?.services ?? []).map((s) => [s.id, s.name] as [number, string]),
  );

  if (isLoading) {
    return (
      <div className="space-y-2" role="status" aria-label="Loading project">
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="h-9 animate-pulse rounded-md bg-muted" />
        ))}
      </div>
    );
  }
  if (isError) return <p className="text-sm text-danger">Failed to load project data.</p>;
  if (!project) return <p className="text-sm text-muted-foreground">Project not found.</p>;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="mb-4">
        <h1 className="text-xl font-semibold">{project.name}</h1>
        <p className="font-mono text-xs text-muted-foreground">{project.working_dir}</p>
      </div>

      <DashboardStats
        projects={[project]}
        updates={projectUpdates}
        rows={rows}
        status={statusQuery.data}
        activeStatus={filters.status}
        onFilter={(patch) => setFilters({ ...filters, status: "", ...patch })}
      />
      <Filters
        value={scoped}
        onChange={setFilters}
        actions={
          <>
            <ScanProgress />
            <CheckAllButton
              projectId={project.id}
              serviceIds={project.services.map((s) => s.id)}
              ariaLabel={`Check all services in "${project.name}"`}
            />
            <ApplyAllButton
              updates={applicableProjectUpdates}
              onApplied={openBatch}
              scopeNoun={`in "${project.name}"`}
              ariaLabel={`Apply all available updates in ${project.name}`}
            />
          </>
        }
      />

      {project.services.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          No services in this project.
        </div>
      )}
      {project.services.length > 0 && rows.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          No services match the current filters.
        </div>
      )}
      {rows.length > 0 && (
        <DashboardTable
          rows={rows}
          updatesByService={updatesByService}
          onApplied={(jobId) => setPanel({ kind: "single", jobId })}
          onAppliedBatch={openBatch}
          onReview={(update, service, proj) => {
            if (!update) return;
            setSelected({ update, service, project: proj });
          }}
          onChangelog={(update, service) => setChangelogFor({ update, service })}
          onLogs={setLogsFor}
        />
      )}

      {changelogFor && (
        <ChangelogDrawer
          update={changelogFor.update}
          service={changelogFor.service}
          onClose={() => setChangelogFor(null)}
        />
      )}

      <LogsDrawer
        service={logsFor}
        open={logsFor != null}
        onOpenChange={(o) => {
          if (!o) setLogsFor(null);
        }}
      />

      {selected && (
        <ReviewDrawer
          update={selected.update}
          service={selected.service}
          project={selected.project}
          onClose={() => setSelected(null)}
          onApplied={(jobId) => {
            setPanel({ kind: "single", jobId });
            setSelected(null);
          }}
        />
      )}

      {panel?.kind === "single" && (
        <ApplyPanel key={panel.jobId} jobId={panel.jobId} onClose={closePanel} />
      )}
      {panel?.kind === "bulk" && (
        <BulkApplyPanel jobs={panel.jobs} serviceNames={serviceNames} onClose={closePanel} />
      )}
    </div>
  );
}

// Stable named export for web/src/router.tsx.
export const ProjectScreen = ProjectRoute;
