import { useSyncExternalStore } from "react";
import type { ScanRun } from "@/api/types";

// Server-authoritative scan-run state, external to any component so every
// check button (dashboard, project, per-row) and the progress bar share one
// source of truth that survives navigation. Mirrors useBusyServices.
let state: ScanRun = { running: false, done: 0, total: 0 };
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function setScanRun(next: ScanRun) {
  if (next.running === state.running && next.done === state.done && next.total === state.total) return;
  state = next;
  emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

function getSnapshot(): ScanRun {
  return state;
}

export function useScanRun(): ScanRun {
  return useSyncExternalStore(subscribe, getSnapshot);
}

// Test seam: reset module state between cases.
export function __resetScanRun() {
  state = { running: false, done: 0, total: 0 };
  emit();
}
