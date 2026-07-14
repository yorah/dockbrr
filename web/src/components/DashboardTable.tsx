import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table";
import { AlertTriangle, ArrowUpCircle, ChevronRight, Eye, FileText, RefreshCw } from "lucide-react";
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
import { StatusBadge, computeStatus } from "@/components/StatusBadge";
import { SeverityDelta } from "@/components/SeverityDelta";
import { DigestShort } from "@/components/DigestShort";
import { ComposeModal } from "@/components/ComposeModal";
import { useApply, useCheck, useToggleProjectAuto } from "@/hooks/mutations";
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
}

// Resolve to whichever candidate actually HAS changelog content, preferring the
// pending update. A pending update can exist with no changelog (non-GitHub image,
// missing token, rate limit), in which case fall back to the last applied update's
// cached changelog rather than showing nothing.
function changelogUpdate(row: Row): Update | undefined {
  if (row.kind !== "service") return undefined;
  const has = (u?: Update) => !!u && (!!u.changelog_text || !!u.changelog_url);
  return has(row.update) ? row.update : has(row.lastApplied) ? row.lastApplied : undefined;
}

function ActionsCell({
  service,
  update,
  changelog,
  onApplied,
  onChangelog,
}: {
  service: Service;
  update: Update | undefined;
  /** Update whose cached changelog the eye opens: pending, else last applied. */
  changelog: Update | undefined;
  onApplied: DashboardTableProps["onApplied"];
  onChangelog: DashboardTableProps["onChangelog"];
}) {
  const check = useCheck();
  const apply = useApply();
  // A gone service has no container to recreate. Applying would just create
  // a fresh one for something the user (or something else) removed.
  const canApply = update?.status === "available" && service.state !== "gone";
  const isHistory = !!changelog && changelog !== update;
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
): ColumnDef<Row>[] {
  return [
    {
      id: "name",
      header: "Name",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        return (
          <Link
            to="/service/$id"
            params={{ id: String(r.service.id) }}
            className="pl-6 hover:underline"
            onClick={(e) => e.stopPropagation()}
          >
            {r.service.name}
          </Link>
        );
      },
    },
    {
      id: "current-image",
      header: "Current image",
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        return (
          <div className="flex flex-col gap-0.5">
            <ImageRefLabel imageRef={r.service.image_ref} />
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
        return (
          <div className="flex flex-col gap-0.5">
            <span>{r.update.tag}</span>
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
            onApplied={onApplied}
            onChangelog={onChangelog}
          />
        );
      },
    },
  ];
}

export function DashboardTable({ rows, onReview, updatesByService, onApplied, onChangelog }: DashboardTableProps) {
  const [collapsed, setCollapsed] = useState<Set<number>>(() => new Set());
  const [composeProject, setComposeProject] = useState<Project | null>(null);

  const visibleRows = useMemo(
    () => rows.filter((r) => r.kind === "project" || !collapsed.has(r.project.id)),
    [rows, collapsed],
  );

  const columns = useMemo(
    () => buildColumns(onApplied, onChangelog),
    [onApplied, onChangelog],
  );

  const table = useReactTable({
    data: visibleRows,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (row) => (row.kind === "project" ? `project-${row.project.id}` : `service-${row.service.id}`),
  });

  function toggle(projectId: number) {
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

            if (original.kind === "project") {
              const expanded = !collapsed.has(original.project.id);
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
                      <div className="flex items-center gap-1">
                        <ProjectAutoToggle project={original.project} />
                        <ProjectBulkActions
                          project={original.project}
                          updatesByService={updatesByService}
                          onApplied={onApplied}
                        />
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
