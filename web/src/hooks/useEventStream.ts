import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { keys } from "@/api/keys";

type Factory = (url: string) => EventSource;
let factory: Factory | null = null;
// Test seam: override how the EventSource is constructed.
export function __setEventSourceFactory(f: Factory | null) { factory = f; }
function makeES(url: string): EventSource {
  return factory ? factory(url) : new EventSource(url, { withCredentials: true });
}

// Reconnect backoff: doubles from 1s, capped at 30s. A successful open resets it.
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;

/**
 * Subscribes to the global refresh stream (GET /api/events/stream, same-origin
 * cookie auth, GET so no CSRF) and maps event types to query invalidations.
 * Events are hints only; the queries stay the source of truth. On a stream error
 * we close and reconnect with capped exponential backoff, so a transient blip
 * (proxy hiccup, brief network drop) doesn't permanently kill live updates.
 * The explicit close() disables EventSource's native retry, so we drive it here.
 */
export function useEventStream(enabled = true) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled) return;
    let es: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let attempts = 0;
    let stopped = false;

    const handleMessage = (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data as string) as { type: string; service_id?: number; job_id?: number };
        switch (ev.type) {
          case "detected":
            void qc.invalidateQueries({ queryKey: keys.updates });
            if (ev.service_id) void qc.invalidateQueries({ queryKey: keys.serviceEvents(ev.service_id) });
            break;
          case "job_finished":
            void qc.invalidateQueries({ queryKey: keys.updates });
            void qc.invalidateQueries({ queryKey: keys.projects });
            void qc.invalidateQueries({ queryKey: keys.jobs });
            if (ev.job_id) void qc.invalidateQueries({ queryKey: keys.job(ev.job_id) });
            break;
          case "jobs_cleared":
            void qc.invalidateQueries({ queryKey: keys.jobs });
            break;
          case "reconciled":
            void qc.invalidateQueries({ queryKey: keys.projects });
            break;
          case "scanned":
            void qc.invalidateQueries({ queryKey: keys.status });
            void qc.invalidateQueries({ queryKey: keys.updates });
            void qc.invalidateQueries({ queryKey: keys.projects });
            break;
        }
      } catch { /* ignore malformed frames */ }
    };

    const connect = () => {
      if (stopped) return;
      es = makeES("/api/events/stream");
      es.onopen = () => { attempts = 0; }; // healthy connection → reset backoff
      es.onmessage = handleMessage;
      es.onerror = () => {
        es?.close();
        if (stopped) return;
        const delay = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** attempts);
        attempts += 1;
        retryTimer = setTimeout(connect, delay);
      };
    };

    connect();
    return () => {
      stopped = true;
      if (retryTimer) clearTimeout(retryTimer);
      es?.close();
    };
  }, [enabled, qc]);
}
