import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { CheckCircle2, Circle, Info } from "lucide-react";
import { EventItem, kindMeta } from "./EventItem";
import type { ServiceEvent } from "@/api/types";

function makeEvent(over: Partial<ServiceEvent> = {}): ServiceEvent {
  return {
    id: 1,
    kind: "detected",
    ref_job_id: null,
    from_digest: "",
    to_digest: "",
    message: "",
    created_at: new Date().toISOString(),
    ...over,
  };
}

describe("kindMeta", () => {
  test("maps known kinds to their icon + label", () => {
    expect(kindMeta("detected").icon).toBe(Info);
    expect(kindMeta("detected").label).toBe("Update detected");
    expect(kindMeta("succeeded").icon).toBe(CheckCircle2);
  });

  test("falls back to a generic meta for an unknown kind", () => {
    const meta = kindMeta("something_new");
    expect(meta.icon).toBe(Circle);
    expect(meta.label).toBe("Event");
  });
});

describe("EventItem", () => {
  test("renders the label for the event kind", () => {
    render(
      <ul>
        <EventItem event={makeEvent({ kind: "succeeded" })} />
      </ul>,
    );
    expect(screen.getByText("Succeeded")).toBeInTheDocument();
  });

  test("shows the View job button and calls onViewJob when ref_job_id is set", async () => {
    const onViewJob = vi.fn();
    render(
      <ul>
        <EventItem event={makeEvent({ ref_job_id: 42 })} onViewJob={onViewJob} />
      </ul>,
    );
    const btn = screen.getByRole("button", { name: /view job #42 log/i });
    await userEvent.click(btn);
    expect(onViewJob).toHaveBeenCalledWith(42);
  });

  test("omits the View job button when ref_job_id is null", () => {
    render(
      <ul>
        <EventItem event={makeEvent({ ref_job_id: null })} onViewJob={() => {}} />
      </ul>,
    );
    expect(screen.queryByRole("button", { name: /view job/i })).toBeNull();
  });
});
