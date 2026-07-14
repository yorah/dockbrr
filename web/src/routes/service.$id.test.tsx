import { describe, expect, test, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import type { Project, ServiceEvent } from "@/api/types";

// Stub the router: ServiceDetail is prop-driven but renders <Link>, which needs
// a router context we don't want to stand up here.
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useParams: () => ({ id: "1" }),
}));

import { ServiceDetail } from "./service.$id";

function project(): Project {
  return {
    id: 10,
    name: "web-stack",
    kind: "compose",
    working_dir: "/srv/web",
    auto_update_enabled: true,
    unmanaged: false,
    services: [
      {
        id: 1,
        name: "app",
        image_ref: "nginx:1.27",
        current_digest: "sha256:current",
        state: "running",
        pinned: false,
        drifted: false,
        healthcheck: true,
        auto_update_enabled: null,
        check_status: "ok",
        last_checked: new Date().toISOString(),
      },
    ],
  };
}

function events(): ServiceEvent[] {
  return [
    { id: 3, kind: "succeeded", ref_job_id: null, from_digest: "sha256:old", to_digest: "sha256:current", message: "", created_at: new Date().toISOString() },
    { id: 2, kind: "succeeded", ref_job_id: null, from_digest: "sha256:older", to_digest: "sha256:old", message: "", created_at: new Date().toISOString() },
    { id: 1, kind: "succeeded", ref_job_id: null, from_digest: "", to_digest: "sha256:older", message: "", created_at: new Date().toISOString() },
  ];
}

describe("ServiceDetail past digests", () => {
  test("excludes the current digest so it isn't listed twice", async () => {
    server.use(
      http.get("/api/projects", () => HttpResponse.json([project()])),
      http.get("/api/services/1/events", () => HttpResponse.json(events())),
    );

    renderWithClient(<ServiceDetail serviceId={1} />);

    // The Past digests section renders once the events load.
    const heading = await screen.findByRole("heading", { name: /past digests/i });
    const section = heading.closest("section")!;

    // current (sha256:current) is shown in the header only; the list holds the
    // two prior distinct digests, and never the current one.
    const items = section.querySelectorAll("li");
    expect(items).toHaveLength(2);
    expect(section.textContent).not.toContain("current");
  });
});
