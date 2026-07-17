import { useEffect, useRef, useState } from "react";
import type { LogLine } from "@/api/types";

type Factory = (url: string) => EventSource;
let factory: Factory | null = null;
// Test seam: override how the EventSource is constructed.
export function __setEventSourceFactory(f: Factory | null) { factory = f; }
function makeES(url: string): EventSource {
  return factory ? factory(url) : new EventSource(url, { withCredentials: true });
}

/**
 * Streams a job's log lines over SSE (GET /api/jobs/{id}/logs, same-origin cookie
 * auth, GET so no CSRF). Each `message` frame is JSON.parse'd into a LogLine and
 * appended. The source is closed on unmount, when jobId changes, and on error
 * (which also flips `closed`). A null jobId opens nothing.
 */
export function useJobLog(jobId: number | null) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [closed, setClosed] = useState(false);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    // Intentional reset-then-resubscribe when jobId changes; a key-based
    // remount would change this hook's public shape (it's a hook, not a
    // component to key), out of scope for a lint fix.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLines([]);
    setClosed(false);
    if (jobId == null) return;
    const es = makeES(`/api/jobs/${jobId}/logs`);
    esRef.current = es;
    es.onmessage = (e: MessageEvent) => {
      try {
        const parsed = JSON.parse(e.data as string) as LogLine;
        setLines((prev) => [...prev, parsed]);
      } catch { /* ignore malformed frames */ }
    };
    es.onerror = () => { es.close(); setClosed(true); };
    return () => { es.close(); esRef.current = null; };
  }, [jobId]);

  return { lines, closed };
}
