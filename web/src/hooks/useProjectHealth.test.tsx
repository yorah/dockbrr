import { describe, expect, test } from "vitest";
import { projectHealth } from "@/hooks/useProjectHealth";
import type { JobRow, Project, Service, Update } from "@/api/types";

const svc = (id: number, name: string): Service => ({
  id, name, image_ref: "nginx:1", current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null,
  check_status: "ok", last_checked: "2026-07-12T10:00:00Z",
});
const proj = (id: number, name: string, services: Service[]): Project => ({
  id, name, kind: "compose", working_dir: `/srv/${name}`,
  auto_update_enabled: false, unmanaged: false, services,
});
const upd = (id: number, service_id: number, status: string): Update => ({
  id, service_id, from_digest: "sha256:a", to_digest: "sha256:b",
  from_version: "1.0.0", to_version: "1.1.0", tag: "1.1.0", severity: "minor",
  changelog_url: "", changelog_text: "", status, detected_at: "2026-07-12T10:00:00Z",
});
const job = (id: number, project_id: number | null, status: string, created_at: string): JobRow => ({
  id, type: "apply", status, scope: "project", exit_code: null, error: "",
  project_id, service_id: null, requested_by: "admin", created_at, finished_at: created_at,
});

describe("projectHealth", () => {
  test("green when there are no updates and no failed job", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const h = projectHealth([p], [], [])!.get(1)!;
    expect(h).toEqual({ updates: 0, dot: "green" });
  });

  test("amber with a pending update; count excludes non-available updates", () => {
    const p = proj(1, "media", [svc(10, "plex"), svc(11, "sonarr")]);
    const updates = [upd(1, 10, "available"), upd(2, 11, "dismissed"), upd(3, 11, "applied")];
    const h = projectHealth([p], updates, [])!.get(1)!;
    expect(h).toEqual({ updates: 1, dot: "amber" });
  });

  test("red when the project's most recent job failed, beats a pending update", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [
      job(1, 1, "success", "2026-07-12T09:00:00Z"),
      job(2, 1, "failed", "2026-07-12T11:00:00Z"),
    ];
    const h = projectHealth([p], [upd(1, 10, "available")], jobs)!.get(1)!;
    expect(h).toEqual({ updates: 1, dot: "red" });
  });

  test("an older failed job is superseded by a newer success", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [
      job(1, 1, "failed", "2026-07-12T09:00:00Z"),
      job(2, 1, "success", "2026-07-12T11:00:00Z"),
    ];
    expect(projectHealth([p], [], jobs)!.get(1)!.dot).toBe("green");
  });

  test("jobs belonging to another project (or to none) do not colour this one", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [job(1, 2, "failed", "2026-07-12T11:00:00Z"), job(2, null, "failed", "2026-07-12T12:00:00Z")];
    expect(projectHealth([p], [], jobs)!.get(1)!.dot).toBe("green");
  });

  test("an update on a service of another project does not colour this one", () => {
    const a = proj(1, "media", [svc(10, "plex")]);
    const b = proj(2, "arr", [svc(20, "radarr")]);
    const map = projectHealth([a, b], [upd(1, 20, "available")], []);
    expect(map.get(1)).toEqual({ updates: 0, dot: "green" });
    expect(map.get(2)).toEqual({ updates: 1, dot: "amber" });
  });
});
