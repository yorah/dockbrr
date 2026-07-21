import { describe, expect, test } from "vitest";
import { joinRows } from "./useDashboardRows";
import type { Project, Update } from "@/api/types";

const projects: Project[] = [{
  id: 1, name: "app", kind: "compose", working_dir: "/srv", auto_update_enabled: false, unmanaged: false, auto_named: false,
  services: [
    { id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "running", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
    { id: 11, name: "db", image_ref: "postgres:16", current_digest: "sha256:b", state: "running", pinned: true, drifted: false, healthcheck: true, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
  ],
}];
const updates: Update[] = [
  { id: 99, service_id: 10, from_digest: "sha256:a", to_digest: "sha256:c", from_version: "", to_version: "", tag: "1.28", severity: "minor", changelog_url: "https://x/rel", changelog_text: "", status: "available", detected_at: "2026-07-04T00:00:00Z", is_self: false },
];

describe("joinRows", () => {
  test("emits a project header then its services, joining updates by service_id", () => {
    const rows = joinRows(projects, updates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    expect(rows[0]).toMatchObject({ kind: "project", project: { id: 1 } });
    const web = rows.find((r) => r.kind === "service" && r.service.id === 10);
    expect(web && web.kind === "service" && web.update?.id).toBe(99);
    const db = rows.find((r) => r.kind === "service" && r.service.id === 11);
    expect(db && db.kind === "service" && db.update).toBeUndefined();
  });

  test("only-updates filter drops services with no open update (and empty project headers)", () => {
    const rows = joinRows(projects, updates, { onlyUpdates: true, project: "", status: "", search: "", showRemoved: false });
    expect(rows.some((r) => r.kind === "service" && r.service.id === 11)).toBe(false);
    expect(rows.some((r) => r.kind === "service" && r.service.id === 10)).toBe(true);
  });

  test("search matches image_ref", () => {
    const rows = joinRows(projects, updates, { onlyUpdates: false, project: "", status: "", search: "postgres", showRemoved: false });
    expect(rows.some((r) => r.kind === "service" && r.service.id === 11)).toBe(true);
    expect(rows.some((r) => r.kind === "service" && r.service.id === 10)).toBe(false);
  });

  test("gone services are hidden by default and shown when showRemoved is true", () => {
    const withGone: Project[] = [{
      ...projects[0],
      services: [
        ...projects[0].services,
        { id: 12, name: "cache", image_ref: "redis:7", current_digest: "sha256:d", state: "gone", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
      ],
    }];

    const hidden = joinRows(withGone, updates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    expect(hidden.some((r) => r.kind === "service" && r.service.id === 12)).toBe(false);

    const shown = joinRows(withGone, updates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: true });
    expect(shown.some((r) => r.kind === "service" && r.service.id === 12)).toBe(true);
  });

  test("a project whose only service is gone produces no header when showRemoved is off, and one when on", () => {
    const goneOnly: Project[] = [{
      id: 2, name: "torn-down", kind: "compose", working_dir: "/srv2", auto_update_enabled: false, unmanaged: false, auto_named: false,
      services: [
        { id: 20, name: "old", image_ref: "redis:6", current_digest: "sha256:x", state: "gone", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
      ],
    }];

    const hidden = joinRows(goneOnly, [], { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    expect(hidden.some((r) => r.kind === "project" && r.project.id === 2)).toBe(false);
    expect(hidden).toHaveLength(0);

    const shown = joinRows(goneOnly, [], { onlyUpdates: false, project: "", status: "", search: "", showRemoved: true });
    expect(shown.some((r) => r.kind === "project" && r.project.id === 2)).toBe(true);
    expect(shown.some((r) => r.kind === "service" && r.service.id === 20)).toBe(true);
  });

  test("status=gone shows only gone services even with showRemoved off", () => {
    const withGone: Project[] = [{
      ...projects[0],
      services: [
        ...projects[0].services,
        { id: 12, name: "cache", image_ref: "redis:7", current_digest: "sha256:d", state: "gone", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
      ],
    }];
    const rows = joinRows(withGone, updates, { onlyUpdates: false, project: "", status: "gone", search: "", showRemoved: false });
    const svcs = rows.filter((r) => r.kind === "service");
    expect(svcs).toHaveLength(1);
    expect(svcs[0].kind === "service" && svcs[0].service.id).toBe(12);
  });

  test("status=restarting keeps only restarting services", () => {
    const withRestart: Project[] = [{
      ...projects[0],
      services: [
        ...projects[0].services,
        { id: 13, name: "worker", image_ref: "busybox:1", current_digest: "sha256:e", state: "restarting", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "" },
      ],
    }];
    const rows = joinRows(withRestart, updates, { onlyUpdates: false, project: "", status: "restarting", search: "", showRemoved: false });
    const svcs = rows.filter((r) => r.kind === "service");
    expect(svcs).toHaveLength(1);
    expect(svcs[0].kind === "service" && svcs[0].service.id).toBe(13);
  });

  test("a dismissed update joins to its row so the row is reviewable", () => {
    const dismissed: Update[] = [
      { ...updates[0], id: 77, service_id: 11, status: "dismissed" },
    ];
    const rows = joinRows(projects, dismissed, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    const db = rows.find((r) => r.kind === "service" && r.service.id === 11);
    expect(db && db.kind === "service" && db.update?.id).toBe(77);
  });

  test("onlyUpdates excludes a dismissed-only row (not actionable)", () => {
    const dismissed: Update[] = [
      { ...updates[0], id: 77, service_id: 11, status: "dismissed" },
    ];
    const rows = joinRows(projects, dismissed, { onlyUpdates: true, project: "", status: "", search: "", showRemoved: false });
    expect(rows.some((r) => r.kind === "service" && r.service.id === 11)).toBe(false);
  });

  test("an available update wins over a dismissed one for the same service", () => {
    const mixed: Update[] = [
      updates[0], // available, service 10
      { ...updates[0], id: 78, service_id: 10, status: "dismissed" },
    ];
    const rows = joinRows(projects, mixed, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    const web = rows.find((r) => r.kind === "service" && r.service.id === 10);
    expect(web && web.kind === "service" && web.update?.status).toBe("available");
  });

  test("attaches the last applied update to the service row, without affecting filters", () => {
    const lastApplied: Update[] = [
      { id: 42, service_id: 11, from_digest: "sha256:x", to_digest: "sha256:b", from_version: "", to_version: "", tag: "16.1", severity: "minor", changelog_url: "https://x/16.1", changelog_text: "# 16.1", status: "applied", detected_at: "2026-07-01T00:00:00Z", is_self: false },
    ];
    const rows = joinRows(projects, updates, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false }, lastApplied);
    const db = rows.find((r) => r.kind === "service" && r.service.id === 11);
    expect(db && db.kind === "service" && db.lastApplied?.id).toBe(42);
    // It must not count as a pending update: `db` has no open update, so the
    // onlyUpdates filter still drops it.
    const filtered = joinRows(projects, updates, { onlyUpdates: true, project: "", status: "", search: "", showRemoved: false }, lastApplied);
    expect(filtered.some((r) => r.kind === "service" && r.service.id === 11)).toBe(false);
  });
});
