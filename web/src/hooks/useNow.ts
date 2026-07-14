import { useEffect, useState } from "react";

// Re-renders on an interval so clock-derived text (a "5m ago" stamp, a
// next-scan countdown) ages on screen without refetching anything. The status
// query only polls every 60s; this keeps the reading between polls truthful.
export function useNow(intervalMs = 10_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}
