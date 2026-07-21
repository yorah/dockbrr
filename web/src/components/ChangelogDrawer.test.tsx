import { expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChangelogDrawer } from "./ChangelogDrawer";
import type { Service, Update } from "@/api/types";

const service: Service = {
  id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:c",
  state: "running", pinned: false, drifted: false, healthcheck: false,
  auto_update_enabled: null, check_status: "ok", image_local: false, last_checked: "", current_version: "",
};
const update: Update = {
  id: 42, service_id: 10, from_digest: "sha256:b", to_digest: "sha256:c",
  from_version: "1.27", to_version: "1.28", tag: "1.28", severity: "minor",
  changelog_url: "https://example.test/rel/1.28", changelog_text: "## What's new\n\n- faster",
  status: "applied", detected_at: "2026-07-01T00:00:00Z", is_self: false,
};

test("renders the cached changelog markdown and the external link", () => {
  render(<ChangelogDrawer update={update} service={service} onClose={() => {}} />);
  expect(screen.getByText("What's new")).toBeInTheDocument();
  expect(screen.getByText("faster")).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /view full changelog/i });
  expect(link).toHaveAttribute("href", "https://example.test/rel/1.28");
});

test("exposes no apply or dismiss control", () => {
  render(<ChangelogDrawer update={update} service={service} onClose={() => {}} />);
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^dismiss$/i })).not.toBeInTheDocument();
});

test("says so when the update has no changelog text", () => {
  render(
    <ChangelogDrawer
      update={{ ...update, changelog_text: "", changelog_url: "" }}
      service={service}
      onClose={() => {}}
    />,
  );
  expect(screen.getByText(/no changelog available/i)).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /view full changelog/i })).not.toBeInTheDocument();
});

test("closes on Escape", async () => {
  const onClose = vi.fn();
  render(<ChangelogDrawer update={update} service={service} onClose={onClose} />);
  await userEvent.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalled();
});

test("labels an applied update as Last applied update", () => {
  render(<ChangelogDrawer update={{ ...update, status: "applied" }} service={service} onClose={() => {}} />);
  expect(screen.getByText(/^Last applied update/)).toBeInTheDocument();
});

test("labels a dismissed update as Dismissed update, not Pending", () => {
  render(<ChangelogDrawer update={{ ...update, status: "dismissed" }} service={service} onClose={() => {}} />);
  expect(screen.getByText(/^Dismissed update/)).toBeInTheDocument();
  expect(screen.queryByText(/^Pending update/)).not.toBeInTheDocument();
});

test("labels an available update as Pending update", () => {
  render(<ChangelogDrawer update={{ ...update, status: "available" }} service={service} onClose={() => {}} />);
  expect(screen.getByText(/^Pending update/)).toBeInTheDocument();
});

test("labels a current-version baseline as Current version, not Pending", () => {
  render(<ChangelogDrawer update={{ ...update, status: "current" }} service={service} onClose={() => {}} />);
  expect(screen.getByText(/^Current version/)).toBeInTheDocument();
  expect(screen.queryByText(/^Pending update/)).not.toBeInTheDocument();
});

test("shows the rate-limit hint for a rate_limited update with no changelog", () => {
  render(
    <ChangelogDrawer
      update={{ ...update, changelog_text: "", changelog_url: "", changelog_status: "rate_limited" }}
      service={service}
      onClose={() => {}}
    />,
  );
  expect(screen.getByRole("link", { name: /token in settings/i })).toHaveAttribute("href", "/settings/registries");
});
