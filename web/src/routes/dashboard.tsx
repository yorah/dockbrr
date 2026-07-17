import { useState } from "react";
import { Plus } from "lucide-react";
import { Filters } from "@/components/Filters";
import { ApplyAllButton, ScanAllButton } from "@/components/BulkActions";
import { DashboardStats } from "@/components/DashboardStats";
import { DashboardTable } from "@/components/DashboardTable";
import { ReviewDrawer } from "@/components/ReviewDrawer";
import { ChangelogDrawer } from "@/components/ChangelogDrawer";
import { LogsDrawer } from "@/components/LogsDrawer";
import { ApplyPanel } from "@/components/ApplyPanel";
import { AddProjectDialog } from "@/components/AddProjectDialog";
import { Button } from "@/components/ui/button";
import { useDashboardRows, type FilterState } from "@/hooks/useDashboardRows";
import { useStatus } from "@/hooks/queries";
import type { Project, Service, Update } from "@/api/types";

const DEFAULT_FILTERS: FilterState = {
  onlyUpdates: false,
  project: "",
  status: "",
  search: "",
  showRemoved: false,
};

interface Selected {
  update: Update;
  service: Service;
  project: Project;
}

export function DashboardRoute() {
  const [filters, setFilters] = useState<FilterState>(DEFAULT_FILTERS);
  const [selected, setSelected] = useState<Selected | null>(null);
  const [changelogFor, setChangelogFor] = useState<{ update: Update; service: Service } | null>(null);
  const [logsFor, setLogsFor] = useState<Service | null>(null);
  // Set by ReviewDrawer's onApplied. Task 13 wires this job id into the live-log/health-gate panel.
  const [appliedJobId, setAppliedJobId] = useState<number | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const { rows, projects, updates: updatesData, updatesByService, isLoading, isError } = useDashboardRows(filters);
  const statusQuery = useStatus();

  // Apply all must never reanimate a service the table hides by default,
  // exclude gone services regardless of the "Show removed" filter state.
  const goneServiceIds = new Set(
    projects.flatMap((p) => p.services).filter((s) => s.state === "gone").map((s) => s.id),
  );
  const applicableUpdates = updatesData.filter((u) => !goneServiceIds.has(u.service_id));

  const looseDefaultOpen = filters.search !== "" || filters.status !== "" || filters.onlyUpdates;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <DashboardStats
        projects={projects}
        updates={updatesData}
        rows={rows}
        status={statusQuery.data}
        activeStatus={filters.status}
        onFilter={(patch) => setFilters({ ...DEFAULT_FILTERS, ...patch })}
      />
      <Filters
        value={filters}
        onChange={setFilters}
        actions={
          <>
            <ScanAllButton ariaLabel="Check all services" />
            <ApplyAllButton
              updates={applicableUpdates}
              onApplied={setAppliedJobId}
              scopeNoun="across all projects"
              ariaLabel="Apply all available updates"
            />
            <Button variant="outline" onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add project
            </Button>
          </>
        }
      />

      {isLoading && (
        <div className="space-y-2" role="status" aria-label="Loading dashboard">
          {Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="h-9 animate-pulse rounded-md bg-muted" />
          ))}
        </div>
      )}
      {isError && <p className="text-sm text-danger">Failed to load dashboard data.</p>}
      {!isLoading && !isError && rows.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          {projects.length === 0 ? (
            <div className="space-y-3">
              <p>No workloads discovered. Is the Docker socket mounted and reachable?</p>
              <Button variant="outline" onClick={() => setAddOpen(true)}>
                <Plus className="mr-2 h-4 w-4" />
                Add your first project
              </Button>
            </div>
          ) : (
            "No services match the current filters."
          )}
        </div>
      )}
      {!isLoading && !isError && rows.length > 0 && (
        <DashboardTable
          rows={rows}
          groupLoose
          defaultCollapsed
          looseDefaultOpen={looseDefaultOpen}
          filtersActive={looseDefaultOpen}
          updatesByService={updatesByService}
          onApplied={setAppliedJobId}
          onReview={(update, service, project) => {
            // Guard against being invoked with no open update (defense in depth:
            // DashboardTable already disables the button and gates the Enter key).
            if (!update) return;
            setSelected({ update, service, project });
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
            setAppliedJobId(jobId);
            setSelected(null);
          }}
        />
      )}

      {/* Live-log / health-gate panel for the job the ReviewDrawer just applied.
          Keyed by job id so each apply starts a fresh panel (resetting any
          in-place rollback swap from a previous job). */}
      {appliedJobId !== null && (
        <ApplyPanel
          key={appliedJobId}
          jobId={appliedJobId}
          onClose={() => setAppliedJobId(null)}
        />
      )}

      <AddProjectDialog open={addOpen} onOpenChange={setAddOpen} />
    </div>
  );
}

// Kept as a stable named export for web/src/router.tsx.
export const DashboardScreen = DashboardRoute;
