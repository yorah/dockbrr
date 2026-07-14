import { useState } from "react";
import { useServiceEvents } from "@/hooks/queries";
import { EventItem } from "@/components/EventItem";
import { ApplyPanel } from "@/components/ApplyPanel";

export interface HistoryTimelineProps {
  serviceId: number;
}


export function HistoryTimeline({ serviceId }: HistoryTimelineProps) {
  const { data, isLoading, isError } = useServiceEvents(serviceId);
  const [viewJobId, setViewJobId] = useState<number | null>(null);

  if (isLoading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }
  if (isError) {
    return <p className="text-sm text-danger">Failed to load history.</p>;
  }

  const events = data ?? [];

  return (
    <>
      <section aria-label="Service history">
        {events.length === 0 ? (
          <p className="text-sm text-muted-foreground">No history yet.</p>
        ) : (
          <ol className="mt-2">
            {events.map((event) => (
              <EventItem
                key={event.id}
                event={event}
                onViewJob={setViewJobId}
                changelog_text={event.changelog_text}
                changelog_url={event.changelog_url}
              />
            ))}
          </ol>
        )}
      </section>

      {/* Read-only past-job log view, reusing ApplyPanel (Task 13) as a viewer. */}
      {viewJobId !== null && (
        <ApplyPanel key={viewJobId} jobId={viewJobId} readOnly onClose={() => setViewJobId(null)} />
      )}
    </>
  );
}
