import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { InfoRow } from "@/components/settings/InfoRow";

describe("SettingsCard", () => {
  it("renders title, description, action slot and body", () => {
    render(
      <SettingsCard title="Build" description="Version and build details." action={<button>Refresh</button>}>
        <p>body</p>
      </SettingsCard>,
    );
    expect(screen.getByRole("heading", { name: "Build" })).toBeInTheDocument();
    expect(screen.getByText("Version and build details.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument();
  });

  it("renders without a description or action", () => {
    render(
      <SettingsCard title="Runtime">
        <p>body</p>
      </SettingsCard>,
    );
    expect(screen.getByRole("heading", { name: "Runtime" })).toBeInTheDocument();
  });

  it("guards the header row from overflow: text block shrinks, title wraps", () => {
    render(
      <SettingsCard
        title="Averylongunbrokentitlethatwouldotherwisepushthecolumnwideandforceabodyscrollbar"
        action={<button>A wide action slot</button>}
      >
        <p>body</p>
      </SettingsCard>,
    );
    const heading = screen.getByRole("heading", { name: /Averylongunbroken/ });
    expect(heading).toHaveClass("break-words");
    expect(heading.parentElement).toHaveClass("min-w-0");
  });
});

describe("DefaultHint", () => {
  it("renders the default badge", () => {
    render(<DefaultHint />);
    expect(screen.getByText("default")).toBeInTheDocument();
  });
});

describe("InfoRow", () => {
  it("renders label, value and optional sub-line", () => {
    render(
      <dl>
        <InfoRow label="Build date" value="29/06/2026 21:48:15" sub="13d 14h ago" />
      </dl>,
    );
    expect(screen.getByText("Build date")).toBeInTheDocument();
    expect(screen.getByText("29/06/2026 21:48:15")).toBeInTheDocument();
    expect(screen.getByText("13d 14h ago")).toBeInTheDocument();
  });
});
