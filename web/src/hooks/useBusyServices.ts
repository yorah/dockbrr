import { useSyncExternalStore } from "react";

export type BusyAction = "apply" | "start" | "stop" | "restart";

// Apply/Start/Stop/Restart all share the same shape of bug: the mutation's
// `isPending` only covers the (sub-second) POST that ENQUEUES a job, not the
// async job itself, which runs for real seconds to minutes. Without this
// store the row's button re-enables the instant the enqueue POST resolves,
// letting a user queue a second apply (or start/stop) before the first has
// even reached Docker. We track "busy" per service here, external to any one
// component, so every button watching a row (the table cell, the review
// drawer, a project-level Apply-all) sees the same state and re-enables in
// lockstep once the job actually finishes.
interface BusyEntry {
  jobId: number;
  action: BusyAction;
  timer: ReturnType<typeof setTimeout>;
}

// A dropped SSE connection (proxy hiccup, tab backgrounded, reconnect still
// backing off) must not strand a row's spinner forever: each entry carries a
// fallback timeout that self-clears well past any realistic job duration.
const BUSY_TIMEOUT_MS = 10 * 60 * 1000;

const entries = new Map<number, BusyEntry>();
const listeners = new Set<() => void>();

// Cached immutable snapshot for useSyncExternalStore: rebuilt lazily only
// when the underlying entries actually change, so repeated getSnapshot calls
// between renders return the same reference and never force a re-render.
let snapshot: ReadonlyMap<number, BusyAction> = new Map();
let dirty = false;

function notify() {
  dirty = true;
  for (const listener of listeners) listener();
}

export function markServiceBusy(serviceId: number, jobId: number, action: BusyAction) {
  const existing = entries.get(serviceId);
  if (existing) clearTimeout(existing.timer);
  const timer = setTimeout(() => {
    entries.delete(serviceId);
    notify();
  }, BUSY_TIMEOUT_MS);
  entries.set(serviceId, { jobId, action, timer });
  notify();
}

export function clearJobBusy(jobId: number) {
  let changed = false;
  for (const [serviceId, entry] of entries) {
    if (entry.jobId !== jobId) continue;
    clearTimeout(entry.timer);
    entries.delete(serviceId);
    changed = true;
  }
  if (changed) notify();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(): ReadonlyMap<number, BusyAction> {
  if (dirty) {
    const next = new Map<number, BusyAction>();
    for (const [serviceId, entry] of entries) next.set(serviceId, entry.action);
    snapshot = next;
    dirty = false;
  }
  return snapshot;
}

export function useBusyServices(): ReadonlyMap<number, BusyAction> {
  return useSyncExternalStore(subscribe, getSnapshot);
}

// Test seam: drop every tracked entry and its pending timeout. Module-level
// state otherwise leaks between test files/cases.
export function __resetBusyServices() {
  for (const entry of entries.values()) clearTimeout(entry.timer);
  entries.clear();
  dirty = true;
}
