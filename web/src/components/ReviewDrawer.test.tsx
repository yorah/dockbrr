import { beforeEach, expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { __resetBusyServices } from "@/hooks/useBusyServices";
import { ReviewDrawer } from "./ReviewDrawer";
import type { Project, Service, Update } from "@/api/types";

beforeEach(() => __resetBusyServices());

const update: Update = {
  id: 7,
  service_id: 10,
  from_digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  to_digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  from_version: "",
  to_version: "",
  tag: "1.28",
  severity: "minor",
  changelog_url: "https://example.com/changelog",
  changelog_text: "",
  status: "available",
  detected_at: "2026-07-04T00:00:00Z",
  is_self: false,
};
const service: Service = {
  id: 10,
  name: "web",
  image_ref: "nginx",
  current_digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  state: "running",
  pinned: false,
  drifted: false,
  healthcheck: true,
  auto_update_enabled: null,
  check_status: "ok",
  image_local: false,
  last_checked: "",
};
const project: Project = {
  id: 1,
  name: "app",
  kind: "compose",
  working_dir: "/srv",
  auto_update_enabled: false,
  unmanaged: false,
  auto_named: false,
  services: [service],
};

function makeUpdate(overrides: Partial<Update> = {}): Update {
  return { ...update, ...overrides };
}

function renderDrawer(
  { update: u, svc }: { update?: Update; svc?: Service } = {},
  callbacks?: { onClose?: () => void; onApplied?: (jobId: number) => void },
) {
  const onClose = callbacks?.onClose ?? vi.fn();
  const onApplied = callbacks?.onApplied ?? vi.fn();
  renderWithClient(
    <ReviewDrawer update={u ?? update} service={svc ?? service} project={project} onClose={onClose} onApplied={onApplied} />,
  );
  return { onClose, onApplied };
}

function setup(svc: Service = service) {
  const onClose = vi.fn();
  const onApplied = vi.fn();
  renderWithClient(
    <ReviewDrawer update={update} service={svc} project={project} onClose={onClose} onApplied={onApplied} />,
  );
  return { onClose, onApplied };
}

test("shows from->to digests, severity, changelog fallback, and the command preview", async () => {
  server.use(
    http.get("/api/updates/7/preview", () =>
      HttpResponse.json({ pull: "docker compose pull web", up: "docker compose up -d web" }),
    ),
  );
  setup();

  expect(screen.getByText("1.28")).toBeInTheDocument();
  expect(screen.getByText("minor")).toBeInTheDocument();
  expect(screen.getByTitle(update.from_digest)).toBeInTheDocument();
  expect(screen.getByTitle(update.to_digest)).toBeInTheDocument();
  expect(screen.getByText(/no changelog available/i)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /view full changelog/i })).toHaveAttribute(
    "href",
    "https://example.com/changelog",
  );

  await waitFor(() => expect(screen.getByText("docker compose pull web")).toBeInTheDocument());
  expect(screen.getByText("docker compose up -d web")).toBeInTheDocument();
});

test("Apply calls useApply with the current scope and fires onApplied(job_id)", async () => {
  let appliedScope: string | undefined;
  server.use(
    http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })),
    http.post("/api/updates/7/apply", async ({ request }) => {
      const body = (await request.json()) as { scope: string };
      appliedScope = body.scope;
      return HttpResponse.json({ job_id: 99 });
    }),
  );
  const { onApplied } = setup();

  await userEvent.click(screen.getByRole("button", { name: /apply/i }));

  await waitFor(() => expect(onApplied).toHaveBeenCalledWith(99));
  expect(appliedScope).toBe("service");
});

test("offers no project scope, since the drawer reviews one service, so it applies one service", async () => {
  server.use(http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })));
  setup();

  expect(screen.queryByRole("tab", { name: /project/i })).toBeNull();
  expect(screen.queryByRole("tab", { name: /service/i })).toBeNull();
});

test("Dismiss calls the dismiss endpoint and closes the drawer", async () => {
  let dismissed = false;
  server.use(
    http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })),
    http.post("/api/updates/7/dismiss", () => {
      dismissed = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  const { onClose } = setup();

  await userEvent.click(screen.getByRole("button", { name: /dismiss/i }));

  await waitFor(() => expect(dismissed).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});

test("renders cached changelog markdown when the update carries text", async () => {
  server.use(http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })));
  renderDrawer({
    update: makeUpdate({
      changelog_text: "## Release 1.3.0\n- fixed the thing",
      changelog_url: "https://example.com/rel",
    }),
  });
  expect(await screen.findByRole("heading", { name: /release 1\.3\.0/i })).toBeInTheDocument();
  expect(screen.getByText(/fixed the thing/)).toBeInTheDocument();
});

test("shows version delta when versions are known", () => {
  server.use(http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })));
  renderDrawer({ update: makeUpdate({ from_version: "1.2.3", to_version: "1.3.0" }) });
  expect(screen.getByText("1.2.3")).toBeInTheDocument();
  expect(screen.getByText("1.3.0")).toBeInTheDocument();
});

test("shows a pinned warning banner when the service is pinned", () => {
  server.use(http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })));
  setup({ ...service, pinned: true });

  expect(screen.getByRole("alert")).toHaveTextContent(/pinned/i);
});

test("disables Apply and shows a warning when the service is gone", () => {
  server.use(http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })));
  setup({ ...service, state: "gone" });

  expect(screen.getByRole("alert")).toHaveTextContent(/gone/i);
  expect(screen.getByRole("button", { name: /^apply$/i })).toBeDisabled();
});

test("a dismissed update shows Restore and calls the restore endpoint", async () => {
  let restored = false;
  server.use(
    http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })),
    http.post("/api/updates/7/restore", () => {
      restored = true;
      return HttpResponse.json({ status: "available" });
    }),
  );
  const onClose = vi.fn();
  renderWithClient(
    <ReviewDrawer
      update={{ ...update, status: "dismissed" }}
      service={service}
      project={project}
      onClose={onClose}
      onApplied={vi.fn()}
    />,
  );

  // Dismiss button is replaced by Restore.
  expect(screen.queryByRole("button", { name: /^dismiss$/i })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: /^restore$/i }));

  await waitFor(() => expect(restored).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});

test("a rolled-back update shows Restore and calls the restore endpoint", async () => {
  let restored = false;
  server.use(
    http.get("/api/updates/7/preview", () => HttpResponse.json({ pull: "p", up: "u" })),
    http.post("/api/updates/7/restore", () => {
      restored = true;
      return HttpResponse.json({ status: "available" });
    }),
  );
  const onClose = vi.fn();
  renderWithClient(
    <ReviewDrawer
      update={{ ...update, status: "rolled_back" }}
      service={service}
      project={project}
      onClose={onClose}
      onApplied={vi.fn()}
    />,
  );

  // Dismiss button is replaced by Restore.
  expect(screen.queryByRole("button", { name: /^dismiss$/i })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: /^restore$/i }));

  await waitFor(() => expect(restored).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});
