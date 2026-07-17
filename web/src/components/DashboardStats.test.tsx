import { act, render, screen, fireEvent, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { DashboardStats } from "./DashboardStats";
import { joinRows } from "@/hooks/useDashboardRows";
import type { Project, Update } from "@/api/types";

const svc = (id: number, over: Partial<Project["services"][number]> = {}) => ({
  id, name: `s${id}`, image_ref: "nginx:1", current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", last_checked: "", current_version: "",
  ...over,
});
const projects: Project[] = [
  { id: 1, name: "p", kind: "compose", working_dir: "/x", auto_update_enabled: false, unmanaged: false, auto_named: false,
    services: [svc(1), svc(2, { pinned: true }), svc(3, { state: "exited" })] },
];
const updates = [
  { id: 9, service_id: 1, from_digest: "a", to_digest: "b", from_version: "", to_version: "",
    tag: "1", severity: "minor", changelog_url: "", changelog_text: "", status: "available",
    detected_at: "2026-07-04T00:00:00Z" },
] as Update[];

test("renders counts and forwards tile clicks as filters", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates}
    status={{ last_check_all: "2026-07-04T09:00:00Z", poll_interval_seconds: 900, docker_reachable: true, version: "x" }}
    onFilter={onFilter} />);
  expect(screen.getByText("3")).toBeInTheDocument(); // services
  const updatesTile = screen.getByRole("button", { name: /updates available/i });
  expect(within(updatesTile).getByText("1")).toBeInTheDocument();
  const pinnedTile = screen.getByRole("button", { name: /pinned/i });
  expect(within(pinnedTile).getByText("1")).toBeInTheDocument();
  const stoppedTile = screen.getByText(/stopped/i).closest("button");
  expect(stoppedTile).not.toBeNull();
  expect(within(stoppedTile!).getByText("1")).toBeInTheDocument();

  fireEvent.click(updatesTile);
  expect(onFilter).toHaveBeenCalledWith({ status: "update-available" });
});

test("active status tile is marked selected; a second click reverts to Any status", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates} activeStatus="pinned" onFilter={onFilter} />);
  const pinnedTile = screen.getByRole("button", { name: /pinned/i });
  expect(pinnedTile).toHaveAttribute("aria-pressed", "true");
  // Services tile is not selected while a status filter is active.
  expect(screen.getByRole("button", { name: /services/i })).toHaveAttribute("aria-pressed", "false");
  // Clicking the already-active tile toggles the filter back off.
  fireEvent.click(pinnedTile);
  expect(onFilter).toHaveBeenCalledWith({ status: "" });
});

test("Services tile is selected when no status filter is active", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates} activeStatus="" onFilter={onFilter} />);
  expect(screen.getByRole("button", { name: /services/i })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: /pinned/i })).toHaveAttribute("aria-pressed", "false");
});

test("stopped tile filters by stopped status", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates} onFilter={onFilter} />);
  const stoppedTile = screen.getByText(/stopped/i).closest("button") as HTMLButtonElement;
  fireEvent.click(stoppedTile);
  expect(onFilter).toHaveBeenCalledWith({ status: "stopped" });
});

test("services tile resets all filters", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates} onFilter={onFilter} />);
  const servicesTile = screen.getByText(/services/i).closest("button") as HTMLButtonElement;
  fireEvent.click(servicesTile);
  expect(onFilter).toHaveBeenCalledWith({ status: "" });
});

test("gone service is not counted in the Stopped tile", () => {
  const onFilter = vi.fn();
  const goneProjects: Project[] = [
    { id: 1, name: "p", kind: "compose", working_dir: "/x", auto_update_enabled: false, unmanaged: false, auto_named: false,
      services: [svc(1), svc(2, { state: "exited" }), svc(3, { state: "gone" })] },
  ];
  render(<DashboardStats projects={goneProjects} updates={[]} onFilter={onFilter} />);
  const stoppedTile = screen.getByText(/stopped/i).closest("button") as HTMLButtonElement;
  // Only the "exited" service counts; "gone" is excluded (STOPPED_STATES = {exited, dead, created}).
  expect(within(stoppedTile).getByText("1")).toBeInTheDocument();
});

test("Services tile shows a removed sub-count when gone services are hidden from the table", () => {
  const onFilter = vi.fn();
  const goneProjects: Project[] = [
    { id: 1, name: "p", kind: "compose", working_dir: "/x", auto_update_enabled: false, unmanaged: false, auto_named: false,
      services: [svc(1), svc(2, { state: "gone" })] },
  ];
  render(<DashboardStats projects={goneProjects} updates={[]} onFilter={onFilter} />);
  const servicesTile = screen.getByRole("button", { name: /services/i });
  expect(within(servicesTile).getByText("2")).toBeInTheDocument();
  expect(within(servicesTile).getByText("1 removed")).toBeInTheDocument();
});

test("Services tile omits the removed sub-count when no service is gone", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates} onFilter={onFilter} />);
  const servicesTile = screen.getByRole("button", { name: /services/i });
  expect(within(servicesTile).queryByText(/removed/)).not.toBeInTheDocument();
});

test("shows docker unreachable tile when status reports it", () => {
  const onFilter = vi.fn();
  render(<DashboardStats projects={projects} updates={updates}
    status={{ last_check_all: "", poll_interval_seconds: 900, docker_reachable: false, version: "x" }}
    onFilter={onFilter} />);
  expect(screen.getByText(/unreachable/i)).toBeInTheDocument();
});

test("updates-available tile counts only currently-visible services, not services hidden by the filter", () => {
  const onFilter = vi.fn();
  const goneWithUpdate: Project[] = [
    { id: 1, name: "p", kind: "compose", working_dir: "/x", auto_update_enabled: false, unmanaged: false, auto_named: false,
      services: [svc(1, { state: "gone" }), svc(2)] },
  ];
  const goneUpdates: Update[] = [
    { id: 9, service_id: 1, from_digest: "a", to_digest: "b", from_version: "", to_version: "",
      tag: "1", severity: "minor", changelog_url: "", changelog_text: "", status: "available",
      detected_at: "2026-07-04T00:00:00Z" },
  ];
  // showRemoved: false hides the gone service (and its update) from the table;
  // the tile must reflect that, not the raw updates list (which has 1 open update).
  const rows = joinRows(goneWithUpdate, goneUpdates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
  render(<DashboardStats projects={goneWithUpdate} updates={goneUpdates} rows={rows} onFilter={onFilter} />);
  const updatesTile = screen.getByRole("button", { name: /updates available/i });
  expect(within(updatesTile).getByText("0")).toBeInTheDocument();
});

test("pinned tile drops a removed (gone) service once the table hides it", () => {
  const onFilter = vi.fn();
  const goneProjects: Project[] = [
    { id: 1, name: "p", kind: "compose", working_dir: "/x", auto_update_enabled: false, unmanaged: false, auto_named: false,
      services: [svc(1, { pinned: true }), svc(2, { pinned: true, state: "gone" })] },
  ];
  // showRemoved: false hides the gone service; its pin must not linger in the
  // count. Two services are pinned, but only the visible one should tally.
  const rows = joinRows(goneProjects, [], { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
  render(<DashboardStats projects={goneProjects} updates={[]} rows={rows} onFilter={onFilter} />);
  const pinnedTile = screen.getByRole("button", { name: /pinned/i });
  expect(within(pinnedTile).getByText("1")).toBeInTheDocument();
});

test("last-scan tile counts down to the scheduler's next check", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-07-04T12:00:00Z"));
  try {
    render(<DashboardStats projects={projects} updates={updates}
      status={{ last_check_all: "2026-07-04T11:55:00Z", next_check_all: "2026-07-04T12:04:00Z",
        poll_interval_seconds: 900, docker_reachable: true, version: "x" }}
      onFilter={vi.fn()} />);
    const tile = screen.getByText("Last scan").closest("button")!;
    expect(within(tile).getByText("5m ago")).toBeInTheDocument();
    expect(within(tile).getByText("next in 4m")).toBeInTheDocument();
    // The clock ages on its own tick, without a status refetch.
    act(() => { vi.advanceTimersByTime(120_000); });
    expect(within(tile).getByText("7m ago")).toBeInTheDocument();
    expect(within(tile).getByText("next in 2m")).toBeInTheDocument();
  } finally {
    vi.useRealTimers();
  }
});

test("last-scan tile omits the countdown when the scheduler reports no next check", () => {
  render(<DashboardStats projects={projects} updates={updates}
    status={{ last_check_all: "2026-07-04T11:55:00Z", poll_interval_seconds: 900, docker_reachable: true, version: "x" }}
    onFilter={vi.fn()} />);
  const tile = screen.getByText("Last scan").closest("button")!;
  expect(within(tile).queryByText(/next in/)).not.toBeInTheDocument();
});
