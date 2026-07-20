import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import type { ScanRun } from "@/api/types";
import { clearJobBusy } from "@/hooks/useBusyServices";
import { setScanRun, useScanRun } from "@/hooks/useScanRun";

// Authoritative resync poll interval while a scan-run is in progress. The SSE
// bus is best-effort (drops frames on buffer overflow), so a dropped
// scan_finished can otherwise strand every check button disabled until a full
// page reload. Polling GET /api/scan is server-authoritative, so it can never
// falsely clear an in-progress scan, only confirm what the server already
// knows.
const SCAN_RUN_POLL_MS = 5000;

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
  const { running } = useScanRun();

  // Self-heal: while running, periodically confirm against the authoritative
  // snapshot in case a scan_finished frame never arrived. Stops as soon as
  // running flips false, whether from that resync or a real scan_finished.
  useEffect(() => {
    if (!enabled || !running) return;
    const id = setInterval(() => {
      void apiFetch<ScanRun>("/api/scan").then(setScanRun).catch(() => {});
    }, SCAN_RUN_POLL_MS);
    return () => clearInterval(id);
  }, [enabled, running]);

  useEffect(() => {
    if (!enabled) return;
    let es: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let attempts = 0;
    let stopped = false;

    const handleMessage = (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data as string) as {
          type: string;
          service_id?: number;
          job_id?: number;
          done?: number;
          total?: number;
        };
        switch (ev.type) {
          case "detected":
            void qc.invalidateQueries({ queryKey: keys.updates });
            if (ev.service_id) void qc.invalidateQueries({ queryKey: keys.serviceEvents(ev.service_id) });
            break;
          case "job_finished": {
            // Busy state clears only AFTER the refetches land: clearing on the
            // raw event re-enables the row while it still shows pre-job state
            // (a Stop button for a now-stopped service), inviting a second
            // click at exactly the wrong moment. invalidateQueries resolves
            // when the active refetches complete, so by then the row has its
            // real state and the right buttons.
            const jobId = ev.job_id;
            const refetches = [
              qc.invalidateQueries({ queryKey: keys.updates }),
              qc.invalidateQueries({ queryKey: keys.projects }),
              qc.invalidateQueries({ queryKey: keys.jobs }),
            ];
            if (jobId) {
              refetches.push(qc.invalidateQueries({ queryKey: keys.job(jobId) }));
              void Promise.all(refetches).finally(() => clearJobBusy(jobId));
            }
            break;
          }
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
          case "scan_progress":
            setScanRun({ running: true, done: ev.done ?? 0, total: ev.total ?? 0 });
            break;
          case "scan_finished":
            setScanRun({ running: false, done: 0, total: 0 });
            void qc.invalidateQueries({ queryKey: keys.updates });
            void qc.invalidateQueries({ queryKey: keys.projects });
            void qc.invalidateQueries({ queryKey: keys.status });
            break;
        }
      } catch { /* ignore malformed frames */ }
    };

    const connect = () => {
      if (stopped) return;
      es = makeES("/api/events/stream");
      es.onopen = () => {
        attempts = 0; // healthy connection → reset backoff
        // Authoritative resync: a page mounted mid-scan, or one whose stream
        // blipped, learns the true running state (dropped progress events
        // self-heal here).
        void apiFetch<ScanRun>("/api/scan").then(setScanRun).catch(() => {});
      };
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
