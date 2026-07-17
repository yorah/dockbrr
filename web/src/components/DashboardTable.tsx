import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table";
import {
  AlertTriangle,
  ArrowUpCircle,
  ChevronRight,
  Eye,
  FileText,
  Loader2,
  Play,
  RefreshCw,
  RotateCw,
  ScrollText,
  Square,
  Trash2,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { relative } from "@/components/RelativeTime";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { StatusBadge, computeStatus, isStopped } from "@/components/StatusBadge";
import { SeverityDelta } from "@/components/SeverityDelta";
import { DigestShort } from "@/components/DigestShort";
import { ComposeModal } from "@/components/ComposeModal";
import { ProjectHealthIndicator } from "@/components/ProjectHealthIndicator";
import { useProjectHealth } from "@/hooks/useProjectHealth";
import {
  useApply,
  useCheck,
  useLifecycle,
  useRemoveContainer,
  useToggleProjectAuto,
} from "@/hooks/mutations";
import { ApplyAllButton, CheckAllButton } from "@/components/BulkActions";
import type { Row } from "@/hooks/useDashboardRows";
import type { Project, Service, Update } from "@/api/types";

const EMPTY = <span className="text-muted-foreground">-</span>;

// A pinned service's image_ref can itself be a digest reference (compose
// pins by digest, e.g. "nginx@sha256:<64 hex>"), which otherwise renders the
// full digest inline and blows out the column width. Split off the "@sha256:…"
// suffix so it can be rendered short via DigestShort, keeping the repo/tag
// prefix intact.
function ImageRefLabel({ imageRef }: { imageRef: string }) {
  const at = imageRef.indexOf("@sha256:");
  if (at === -1) return <span>{imageRef}</span>;
  return (
    <span>
      {imageRef.slice(0, at)}
      <span className="opacity-50">@</span>
      <DigestShort digest={imageRef.slice(at + 1)} />
    </span>
  );
}

export interface DashboardTableProps {
  rows: Row[];
  onReview: (update: Update | undefined, service: Service, project: Project) => void;
  /** Open updates keyed by service id, so a project row can find an update to apply project-wide. */
  updatesByService: Map<number, Update>;
  /** Called with the new job id after a row Apply enqueues, so the caller can open the live-log panel. */
  onApplied: (jobId: number) => void;
  /** Opens the read-only changelog view for a service's pending or last-applied update. */
  onChangelog: (update: Update, service: Service) => void;
  /** Opens the live tail-logs drawer for a service. Defaults to a no-op (wired in Task 8). */
  onLogs?: (service: Service) => void;
  /** When true (dashboard only), auto-named projects render inside a collapsed "Loose" group. */
  groupLoose?: boolean;
  /** Initial + synced open-state for the Loose group (dashboard passes filters-active). */
  looseDefaultOpen?: boolean;
  /** When true (dashboard only), every top-level project loads collapsed. */
  defaultCollapsed?: boolean;
  /** When true, an active filter reveals services regardless of collapse state. */
  filtersActive?: boolean;
}

// Resolve to whichever candidate the eye opens, preferring the pending update.
// A pending update can lack changelog content (non-GitHub image, missing token,
// rate limit). When it was rate-limited we still open it, so ChangelogDrawer can
// show the "add a token" hint; otherwise fall back to the last applied update's
// cached changelog rather than showing nothing.
function changelogUpdate(row: Row): Update | undefined {
  if (row.kind !== "service") return undefined;
  const has = (u?: Update) =>
    !!u && (!!u.changelog_text || !!u.changelog_url || u.changelog_status === "rate_limited");
  return has(row.update) ? row.update : has(row.lastApplied) ? row.lastApplied : undefined;
}

function ActionsCell({
  service,
  update,
  changelog,
  onApplied,
  onChangelog,
  onLogs,
  canRemove,
  removing,
  onRemove,
}: {
  service: Service;
  update: Update | undefined;
  /** Update whose cached changelog the eye opens: pending, else last applied. */
  changelog: Update | undefined;
  onApplied: DashboardTableProps["onApplied"];
  onChangelog: DashboardTableProps["onChangelog"];
  onLogs: (service: Service) => void;
  /** True only for stopped standalone containers (backend guard mirror). */
  canRemove: boolean;
  /** A remove job for this container has been enqueued and not yet finished. */
  removing: boolean;
  onRemove: (serviceId: number) => void;
}) {
  const check = useCheck();
  const apply = useApply();
  const lifecycle = useLifecycle();
  // A gone service has no container to recreate. Applying would just create
  // a fresh one for something the user (or something else) removed.
  const canApply = update?.status === "available" && service.state !== "gone";
  const isHistory = !!changelog && changelog !== update;
  // "gone" services have no container left to start/stop/restart: only Logs
  // (which reads cached history, not a live container) still makes sense.
  const gone = service.state === "gone";
  const stopped = isStopped(service.state);
  const runLifecycle = (action: "start" | "stop" | "restart") => {
    lifecycle.mutate(
      { serviceId: service.id, action },
      { onSuccess: (res) => onApplied(res.job_id) },
    );
  };
  return (
    <div className="flex items-center gap-1">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0"
              disabled={!changelog}
              aria-label={
                isHistory
                  ? `Last applied changelog for ${service.name}`
                  : `Changelog for ${service.name}`
              }
              onClick={(e) => {
                e.stopPropagation();
                if (changelog) onChangelog(changelog, service);
              }}
            >
              <Eye className="h-4 w-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{isHistory ? "Last applied changelog" : "Changelog"}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0"
              disabled={!canApply || apply.isPending}
              aria-label={`Apply update to ${service.name}`}
              onClick={(e) => {
                e.stopPropagation();
                if (!update) return;
                const msg = service.pinned
                  ? `Apply update to "${service.name}"? It is pinned, and applying overrides the pin and recreates the container.`
                  : `Apply update to "${service.name}"? This recreates the container.`;
                if (!window.confirm(msg)) return;
                apply.mutate(
                  { id: update.id, scope: "service" },
                  { onSuccess: (res) => onApplied(res.job_id) },
                );
              }}
            >
              <ArrowUpCircle className="h-4 w-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Apply update</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0"
              disabled={check.isPending}
              aria-label={`Check ${service.name} now`}
              onClick={(e) => {
                e.stopPropagation();
                check.mutate(service.id);
              }}
            >
              <RefreshCw className={check.isPending ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Check now</TooltipContent>
        </Tooltip>
        {!gone && stopped && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 w-7 p-0"
                disabled={lifecycle.isPending}
                aria-label={`Start ${service.name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  runLifecycle("start");
                }}
              >
                <Play className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Start</TooltipContent>
          </Tooltip>
        )}
        {!gone && !stopped && (
          <>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 w-7 p-0"
                  disabled={lifecycle.isPending}
                  aria-label={`Stop ${service.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    runLifecycle("stop");
                  }}
                >
                  <Square className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Stop</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 w-7 p-0"
                  disabled={lifecycle.isPending}
                  aria-label={`Restart ${service.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    runLifecycle("restart");
                  }}
                >
                  <RotateCw className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Restart</TooltipContent>
            </Tooltip>
          </>
        )}
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0"
              aria-label={`Logs for ${service.name}`}
              onClick={(e) => {
                e.stopPropagation();
                onLogs(service);
              }}
            >
              <ScrollText className="h-4 w-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Logs</TooltipContent>
        </Tooltip>
        {canRemove && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 w-7 p-0"
                disabled={removing}
                aria-label={`Remove ${service.name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  if (removing) return;
                  if (!window.confirm(`Remove stopped container "${service.name}"? This cannot be undone.`)) return;
                  onRemove(service.id);
                }}
              >
                {removing ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
              </Button>
            </TooltipTrigger>
            <TooltipContent>Remove</TooltipContent>
          </Tooltip>
        )}
      </div>
  );
}

// Project-level auto-update, inline on the project header row (the same flag
// Settings > Auto-update writes). The header row itself collapses on click and
// on Enter, so the switch swallows both.
function ProjectAutoToggle({ project }: { project: Project }) {
  const toggle = useToggleProjectAuto();
  return (
    <div
      className="flex items-center gap-1.5"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="text-xs text-muted-foreground">Auto</span>
        </TooltipTrigger>
        <TooltipContent>
          Apply this project&apos;s updates without review, on each poll interval. Genuinely
          pinned services are never auto-updated.
        </TooltipContent>
      </Tooltip>
      <Switch
        checked={project.auto_update_enabled}
        disabled={toggle.isPending}
        aria-label={`Auto-update ${project.name}`}
        onCheckedChange={(checked) => toggle.mutate({ id: project.id, enabled: checked })}
      />
    </div>
  );
}

// Per-project bulk actions: check every service, and apply every available
// update in the project. Both reuse the shared BulkActions buttons.
function ProjectBulkActions({
  project,
  updatesByService,
  onApplied,
}: {
  project: Project;
  updatesByService: Map<number, Update>;
  onApplied: DashboardTableProps["onApplied"];
}) {
  // Excludes gone services even though "Show removed" being off already hides
  // their row from the table. Otherwise Apply all would silently reanimate
  // a removed container the user never saw in this list.
  const pending = project.services
    .filter((s) => s.state !== "gone")
    .map((s) => updatesByService.get(s.id))
    .filter((u): u is Update => u?.status === "available");
  return (
    <>
      <CheckAllButton
        serviceIds={project.services.map((s) => s.id)}
        ariaLabel={`Check all services in ${project.name}`}
      />
      <ApplyAllButton
        updates={pending}
        onApplied={onApplied}
        scopeNoun={`in "${project.name}"`}
        ariaLabel={`Apply all updates in ${project.name}`}
      />
    </>
  );
}

function buildColumns(
  onApplied: DashboardTableProps["onApplied"],
  onChangelog: DashboardTableProps["onChangelog"],
  onLogs: (service: Service) => void,
  removingIds: Set<number>,
  onRemove: (serviceId: number) => void,
): ColumnDef<Row>[] {
  return [
    {
      id: "name",
      header: "Name",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        return (
          <div className="flex items-center gap-2 pl-6">
            <Link
              to="/service/$id"
              params={{ id: String(r.service.id) }}
              className="hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {r.service.name}
            </Link>
          </div>
        );
      },
    },
    {
      id: "current-image",
      header: "Current image",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        // A floating tag (latest, stable) hides the actual running version. When
        // detection reverse-resolved it (update.from_version), surface it here so
        // the list matches the changelog's "v1.13.0 → v1.14.1", instead of just
        // showing ":latest". Skipped when the ref tag already IS the version.
        const from = r.update?.from_version;
        const showFrom = !!from && !r.service.image_ref.endsWith(`:${from}`);
        return (
          <div className="flex flex-col gap-0.5">
            <ImageRefLabel imageRef={r.service.image_ref} />
            {showFrom && <span className="text-xs text-muted-foreground">{from}</span>}
            <DigestShort digest={r.service.current_digest} />
          </div>
        );
      },
    },
    {
      id: "latest",
      header: "Latest",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        if (!r.update) return EMPTY;
        // Prefer the resolved target version over the raw tag. For a floating
        // tag (latest) that reverse-resolves to a release, show "v1.14.1
        // (latest)"; for a semver tag they coincide, so just the version.
        const { to_version: to, tag } = r.update;
        return (
          <div className="flex flex-col gap-0.5">
            <span>
              {to && to !== tag ? (
                <>
                  {to} <span className="opacity-50">({tag})</span>
                </>
              ) : (
                tag
              )}
            </span>
            <DigestShort digest={r.update.to_digest} />
          </div>
        );
      },
    },
    {
      id: "status",
      header: "Status",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        const status = computeStatus(
          r.service,
          r.update
            ? {
                open: r.update.status === "available",
                dismissed: r.update.status === "dismissed",
                // rolledBack intentionally omitted: joinRows (useDashboardRows.ts)
                // only ever maps available/dismissed updates into r.update, so
                // r.update.status is never "rolled_back" here. Do not restore
                // this flag without also making joinRows surface rolled_back
                // updates (a separate, deliberately declined change).
              }
            : undefined,
        );
        return <StatusBadge status={status} />;
      },
    },
    {
      id: "delta",
      header: "Delta",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service" || !r.update) return EMPTY;
        return <SeverityDelta severity={r.update.severity} />;
      },
    },
    {
      id: "last-checked",
      header: "Last checked",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        const s = r.service;
        return (
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            {s.last_checked ? relative(s.last_checked) : "-"}
            {s.check_status === "rate_limited" && (
              <AlertTriangle aria-label="Registry rate-limited" className="h-3.5 w-3.5 text-warning" />
            )}
            {s.check_status === "error" && (
              <AlertTriangle aria-label="Registry error" className="h-3.5 w-3.5 text-danger" />
            )}
          </span>
        );
      },
    },
    {
      id: "actions",
      header: "Actions",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        return (
          <ActionsCell
            service={r.service}
            update={r.update}
            changelog={changelogUpdate(r)}
            canRemove={r.project.kind === "standalone" && isStopped(r.service.state)}
            removing={removingIds.has(r.service.id)}
            onRemove={onRemove}
            onApplied={onApplied}
            onChangelog={onChangelog}
            onLogs={onLogs}
          />
        );
      },
    },
  ];
}

export function DashboardTable({
  rows,
  onReview,
  updatesByService,
  onApplied,
  onChangelog,
  onLogs = () => {},
  groupLoose = false,
  looseDefaultOpen = false,
  defaultCollapsed = false,
  filtersActive = false,
}: DashboardTableProps) {
  const [collapsed, setCollapsed] = useState<Set<number>>(() => new Set());
  // Ids we've already applied the collapse default to. Lets a user-expanded
  // project survive a rows refetch: once seen, the effect never re-collapses it.
  const seenProjects = useRef<Set<number>>(new Set());
  const [composeProject, setComposeProject] = useState<Project | null>(null);
  const [looseOpen, setLooseOpen] = useState(looseDefaultOpen);
  const removeContainer = useRemoveContainer();
  // Same per-project health (open-update count + status dot) the sidebar shows,
  // so the project row carries the identical glyph.
  const { health } = useProjectHealth();
  // Services with a remove job enqueued but not yet finished. The mutation's
  // isPending only covers the (sub-second) enqueue POST, not the async job, so
  // without this the button would re-enable immediately and let the user queue
  // a second remove. We keep the id here until the container disappears from
  // rows (job_finished SSE refetches projects → the gone service is filtered out).
  const [removingIds, setRemovingIds] = useState<Set<number>>(() => new Set());

  // Auto-expand under an active filter, re-collapse when it clears.
  useEffect(() => setLooseOpen(looseDefaultOpen), [looseDefaultOpen]);

  // Self-heal the removing set: drop any id whose service is no longer a
  // removable (stopped) row. Success removes the container (row gone); a failed
  // remove leaves it stopped-but-present, and the hook's error toast already
  // fired, so clearing here re-enables the button rather than spinning forever.
  const removableIds = useMemo(() => {
    const s = new Set<number>();
    for (const r of rows) {
      if (r.kind === "service" && r.project.kind === "standalone" && isStopped(r.service.state)) {
        s.add(r.service.id);
      }
    }
    return s;
  }, [rows]);
  useEffect(() => {
    setRemovingIds((prev) => {
      const next = new Set([...prev].filter((id) => removableIds.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [removableIds]);

  // Collapse-by-default: seed each newly-seen TOP-LEVEL project into `collapsed`
  // exactly once. Auto-named (Loose) projects are skipped — their visibility is
  // gated by looseOpen, not collapsed, so seeding them would keep their services
  // hidden even after the Loose group is opened.
  useEffect(() => {
    if (!defaultCollapsed) return;
    const present = new Set<number>();
    const fresh: number[] = [];
    for (const r of rows) {
      if (r.kind !== "project") continue;
      if (r.project.auto_named) continue;
      present.add(r.project.id);
      if (seenProjects.current.has(r.project.id)) continue;
      fresh.push(r.project.id);
    }
    // Forget projects that have gone away, but only when no filter is active:
    // under a filter `rows` is narrowed to matches, so an absent project may
    // just be filtered out, not deleted. With no filter `rows` is the full set,
    // so a genuine deletion drops the id — letting it re-collapse at the default
    // if it ever reappears, and keeping `seenProjects` from growing unbounded.
    if (!filtersActive) {
      for (const id of seenProjects.current) {
        if (!present.has(id)) seenProjects.current.delete(id);
      }
    }
    if (fresh.length === 0) return;
    for (const id of fresh) seenProjects.current.add(id);
    setCollapsed((prev) => {
      const next = new Set(prev);
      for (const id of fresh) next.add(id);
      return next;
    });
  }, [rows, defaultCollapsed, filtersActive]);

  const handleRemove = (serviceId: number) => {
    setRemovingIds((prev) => new Set(prev).add(serviceId));
    // The hook already toasts on enqueue error; here we just clear the marker
    // so the button recovers. Job-level failures self-heal via removableIds.
    removeContainer.mutate(serviceId, {
      onError: () =>
        setRemovingIds((prev) => {
          const next = new Set(prev);
          next.delete(serviceId);
          return next;
        }),
    });
  };

  // All loose (auto-named standalone) service rows, independent of the group's
  // open/closed state: the bulk-remove header needs every stopped service's
  // name for the confirm prompt even though only the open ones are on screen.
  const looseServices = useMemo(
    () => rows.filter((r): r is Extract<Row, { kind: "service" }> => r.kind === "service" && r.project.auto_named),
    [rows],
  );

  const stoppedLooseServices = useMemo(
    () => looseServices.filter((r) => isStopped(r.service.state)),
    [looseServices],
  );
  const looseRemoving = stoppedLooseServices.some((r) => removingIds.has(r.service.id));

  function removeStoppedLoose() {
    if (stoppedLooseServices.length === 0) return;
    const names = stoppedLooseServices.map((r) => r.service.name).join(", ");
    const n = stoppedLooseServices.length;
    if (!window.confirm(`Remove ${n} stopped container${n > 1 ? "s" : ""}: ${names}? This cannot be undone.`)) return;
    for (const r of stoppedLooseServices) handleRemove(r.service.id);
  }

  const visibleRows = useMemo(() => {
    const shown = (r: Row) => r.kind !== "service" || filtersActive || !collapsed.has(r.project.id);
    if (!groupLoose) {
      return rows.filter(shown);
    }
    const normal = rows.filter((r) => r.kind !== "loose" && !r.project.auto_named);
    const loose = rows.filter((r) => r.kind !== "loose" && r.project.auto_named);
    const looseCount = loose.filter((r) => r.kind === "project").length;
    const out: Row[] = normal.filter(shown);
    if (looseCount > 0) {
      out.push({ kind: "loose", count: looseCount });
      if (looseOpen) out.push(...loose.filter(shown));
    }
    return out;
  }, [rows, collapsed, filtersActive, groupLoose, looseOpen]);

  const columns = useMemo(
    () => buildColumns(onApplied, onChangelog, onLogs, removingIds, handleRemove),
    // handleRemove is stable enough (only closes over setState + the mutation);
    // removingIds is what actually needs to re-derive the cells.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [onApplied, onChangelog, onLogs, removingIds],
  );

  const table = useReactTable({
    data: visibleRows,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (row) =>
      row.kind === "project"
        ? `project-${row.project.id}`
        : row.kind === "loose"
          ? "loose-header"
          : `service-${row.service.id}`,
  });

  function toggle(projectId: number) {
    // Under an active filter every service is force-shown, so a header click
    // would silently mutate `collapsed` and change the collapse state the user
    // returns to once the filter clears. Ignore toggles while filtering.
    if (filtersActive) return;
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
  }

  const headerGroup = table.getHeaderGroups()[0];

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <Table wrapperClassName="min-h-0 flex-1 overflow-auto rounded-lg border border-border">
        <TableHeader>
          <TableRow>
            {headerGroup.headers.map((header) => (
              <TableHead key={header.id}>
                {flexRender(header.column.columnDef.header, header.getContext())}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {table.getRowModel().rows.map((row) => {
            const original = row.original;

            if (original.kind === "loose") {
              return (
                <TableRow
                  key={row.id}
                  tabIndex={0}
                  className="cursor-pointer bg-muted/20 font-medium text-muted-foreground"
                  onClick={() => setLooseOpen((o) => !o)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") setLooseOpen((o) => !o);
                  }}
                >
                  <TableCell colSpan={row.getVisibleCells().length}>
                    <div className="flex items-center justify-between gap-2">
                      <button
                        type="button"
                        className="flex items-center gap-2 font-medium"
                        aria-expanded={looseOpen}
                        onClick={(e) => {
                          e.stopPropagation();
                          setLooseOpen((o) => !o);
                        }}
                      >
                        <ChevronRight className={cn("h-4 w-4 transition-transform", looseOpen && "rotate-90")} />
                        Loose ({original.count})
                      </button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 px-2 text-xs"
                        disabled={stoppedLooseServices.length === 0 || looseRemoving}
                        onClick={(e) => {
                          e.stopPropagation();
                          removeStoppedLoose();
                        }}
                      >
                        {looseRemoving ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <Trash2 className="h-3.5 w-3.5" />
                        )}
                        Remove stopped containers
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              );
            }

            if (original.kind === "project") {
              const expanded = filtersActive || !collapsed.has(original.project.id);
              return (
                <TableRow
                  key={row.id}
                  tabIndex={0}
                  className="cursor-pointer bg-muted/40 font-medium"
                  onClick={() => toggle(original.project.id)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") toggle(original.project.id);
                  }}
                >
                  <TableCell colSpan={row.getVisibleCells().length}>
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <button
                          type="button"
                          className="flex items-center gap-2 font-medium"
                          onClick={(e) => {
                            e.stopPropagation();
                            toggle(original.project.id);
                          }}
                        >
                          <ChevronRight className={cn("h-4 w-4 transition-transform", expanded && "rotate-90")} />
                          {original.project.name}
                          {original.project.unmanaged && (
                            <Badge variant="danger" className="ml-2">Unmanaged</Badge>
                          )}
                        </button>
                        {(() => {
                          const h = health.get(original.project.id) ?? { updates: 0, dot: "green" as const };
                          return <ProjectHealthIndicator updates={h.updates} dot={h.dot} />;
                        })()}
                      </div>
                      <div className="flex items-center gap-1">
                        <ProjectAutoToggle project={original.project} />
                        <ProjectBulkActions
                          project={original.project}
                          updatesByService={updatesByService}
                          onApplied={onApplied}
                        />
                        {original.project.kind === "compose" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7 gap-1 px-2 text-xs"
                            onClick={(e) => {
                              e.stopPropagation();
                              setComposeProject(original.project);
                            }}
                          >
                            <FileText className="h-3.5 w-3.5" />
                            Compose
                          </Button>
                        )}
                      </div>
                    </div>
                  </TableCell>
                </TableRow>
              );
            }

            return (
              <TableRow
                key={row.id}
                tabIndex={0}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && original.update) onReview(original.update, original.service, original.project);
                }}
              >
                {row.getVisibleCells().map((cell) => (
                  <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
                ))}
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {composeProject && (
        <ComposeModal
          projectId={composeProject.id}
          projectName={composeProject.name}
          open
          onOpenChange={(open) => {
            if (!open) setComposeProject(null);
          }}
        />
      )}
    </div>
  );
}
