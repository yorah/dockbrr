export function relative(iso: string, now = new Date()): string {
  if (!iso) return "never";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "never";
  const s = Math.round((now.getTime() - t) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

// Countdown to a future instant, rendered at minute resolution so a 10s render
// tick is enough to keep it honest. A deadline that has passed reads "due":
// the scheduler tick and our clock never agree to the second.
export function until(iso: string, now = new Date()): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "";
  const s = Math.round((t - now.getTime()) / 1000);
  if (s <= 0) return "due";
  if (s < 60) return "<1m";
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

export function RelativeTime({ iso, now }: { iso: string; now?: Date }) {
  return <span title={iso}>{relative(iso, now)}</span>;
}
