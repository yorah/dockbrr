import { expect, test } from "vitest";
import userEvent from "@testing-library/user-event";
import { screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { HistoryTimeline } from "./HistoryTimeline";

test("renders events with kind labels", async () => {
  server.use(http.get("/api/services/10/events", () => HttpResponse.json([
    { id: 1, kind: "detected", ref_job_id: null, from_digest: "sha256:a", to_digest: "sha256:b", message: "update available", created_at: "2026-07-01T00:00:00Z" },
    { id: 2, kind: "succeeded", ref_job_id: 5, from_digest: "sha256:a", to_digest: "sha256:b", message: "applied", created_at: "2026-07-01T00:05:00Z" },
  ])));
  renderWithClient(<HistoryTimeline serviceId={10} />);
  await waitFor(() => expect(screen.getByText(/update available/i)).toBeInTheDocument());
  expect(screen.getByText(/applied/i)).toBeInTheDocument();
  // Assert one <li> per event (no separate changelog rows)
  const listItems = screen.getAllByRole("listitem");
  expect(listItems).toHaveLength(2);
});

test("empty history shows an empty state", async () => {
  server.use(http.get("/api/services/11/events", () => HttpResponse.json([])));
  renderWithClient(<HistoryTimeline serviceId={11} />);
  await waitFor(() => expect(screen.getByText(/no history/i)).toBeInTheDocument());
});

test("a history entry with a changelog shows an affordance that renders the sanitized changelog", async () => {
  server.use(http.get("/api/services/12/events", () => HttpResponse.json([
    {
      id: 1,
      kind: "succeeded",
      ref_job_id: 5,
      from_digest: "sha256:a",
      to_digest: "sha256:b",
      message: "applied",
      created_at: "2026-07-01T00:05:00Z",
      changelog_text: "## Release 1.3.0\n- fixed the thing",
      changelog_url: "https://example.com/rel",
    },
  ])));
  renderWithClient(<HistoryTimeline serviceId={12} />);
  await waitFor(() => expect(screen.getByText(/applied/i)).toBeInTheDocument());

  // Assert one <li> per event (changelog within the same row, not a separate row)
  const listItems = screen.getAllByRole("listitem");
  expect(listItems).toHaveLength(1);

  const toggle = screen.getByRole("button", { name: /changelog/i });
  await userEvent.click(toggle);

  expect(await screen.findByRole("heading", { name: /release 1\.3\.0/i })).toBeInTheDocument();
  expect(screen.getByText(/fixed the thing/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /view full changelog/i })).toHaveAttribute(
    "href",
    "https://example.com/rel",
  );
});

test("a history entry with neither changelog field shows no affordance", async () => {
  server.use(http.get("/api/services/13/events", () => HttpResponse.json([
    {
      id: 1,
      kind: "detected",
      ref_job_id: null,
      from_digest: "sha256:a",
      to_digest: "sha256:b",
      message: "update available",
      created_at: "2026-07-01T00:00:00Z",
    },
  ])));
  renderWithClient(<HistoryTimeline serviceId={13} />);
  await waitFor(() => expect(screen.getByText(/update available/i)).toBeInTheDocument());

  expect(screen.queryByRole("button", { name: /changelog/i })).not.toBeInTheDocument();
});

test("an XSS-shaped changelog_text is sanitized and does not execute", async () => {
  const evil = 'Hello <script>window.__pwned=1</script> <img src=x onerror="window.__pwned=1"> world';
  server.use(http.get("/api/services/14/events", () => HttpResponse.json([
    {
      id: 1,
      kind: "succeeded",
      ref_job_id: 5,
      from_digest: "sha256:a",
      to_digest: "sha256:b",
      message: "applied",
      created_at: "2026-07-01T00:05:00Z",
      changelog_text: evil,
    },
  ])));
  renderWithClient(<HistoryTimeline serviceId={14} />);
  await waitFor(() => expect(screen.getByText(/applied/i)).toBeInTheDocument());

  // Assert one <li> per event
  const listItems = screen.getAllByRole("listitem");
  expect(listItems).toHaveLength(1);

  await userEvent.click(screen.getByRole("button", { name: /changelog/i }));

  await waitFor(() => expect(screen.getByText(/hello/i)).toBeInTheDocument());
  expect(document.querySelector("script")).toBeNull();
  expect(document.body.innerHTML).not.toContain("onerror");
  expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
});
