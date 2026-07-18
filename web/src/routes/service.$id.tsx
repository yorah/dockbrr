import { useMemo } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useProjects, useServiceEvents } from "@/hooks/queries";
import { HistoryTimeline } from "@/components/HistoryTimeline";
import { DigestShort } from "@/components/DigestShort";
import { StatusBadge, computeStatus } from "@/components/StatusBadge";
import { Badge } from "@/components/ui/badge";
import type { Project, Service } from "@/api/types";

function findService(
  projects: Project[] | undefined,
  id: number,
): { service: Service; project: Project } | undefined {
  for (const project of projects ?? []) {
    const service = project.services.find((s) => s.id === id);
    if (service) return { service, project };
  }
  return undefined;
}

export interface ServiceDetailProps {
  serviceId: number;
}

/** Plain, prop-driven component so it's directly testable without the router. */
export function ServiceDetail({ serviceId }: ServiceDetailProps) {
  const { data: projects } = useProjects();
  const { data: events } = useServiceEvents(serviceId);
  const found = useMemo(() => findService(projects, serviceId), [projects, serviceId]);

  // Past digests: the sequence of distinct to_digest values from the event
  // history, newest-first (events already arrive newest-first from the store).
  // The service's current digest is shown separately in the header, so exclude
  // it here to avoid listing it twice.
  const currentDigest = found?.service.current_digest;
  const pastDigests = useMemo(() => {
    const seen = new Set<string>();
    const digests: string[] = [];
    for (const event of events ?? []) {
      if (event.to_digest && event.to_digest !== currentDigest && !seen.has(event.to_digest)) {
        seen.add(event.to_digest);
        digests.push(event.to_digest);
      }
    }
    return digests;
  }, [events, currentDigest]);

  return (
    <div className="mx-auto max-w-3xl">
      <Link to="/" className="mb-4 inline-block text-xs text-muted-foreground hover:underline">
        ← Dashboard
      </Link>
      <header className="mb-6">
        {found ? (
          <>
            <div className="flex items-center gap-2">
              <h1 className="text-lg font-semibold">{found.service.name}</h1>
              {found.service.image_local ? (
                <Badge variant="default">Local</Badge>
              ) : (
                <StatusBadge status={computeStatus(found.service, undefined)} />
              )}
            </div>
            <p className="mt-1 text-sm text-muted-foreground">{found.service.image_ref}</p>
            <div className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
              <span>Current:</span>
              <DigestShort digest={found.service.current_digest} />
            </div>
          </>
        ) : (
          <h1 className="text-lg font-semibold">Service #{serviceId}</h1>
        )}
      </header>

      {pastDigests.length > 0 && (
        <section className="mb-6">
          <h2 className="mb-2 text-sm font-medium text-foreground">Past digests</h2>
          <ul className="flex flex-wrap gap-2">
            {pastDigests.map((digest) => (
              <li key={digest} className="rounded-md bg-muted px-2 py-1">
                <DigestShort digest={digest} />
              </li>
            ))}
          </ul>
        </section>
      )}

      <section>
        <h2 className="mb-2 text-sm font-medium text-foreground">History</h2>
        <HistoryTimeline serviceId={serviceId} />
      </section>
    </div>
  );
}

/** Route component: reads the $id param and delegates to ServiceDetail. */
export function ServiceScreen() {
  const { id } = useParams({ from: "/service/$id" });
  return <ServiceDetail serviceId={Number(id)} />;
}
