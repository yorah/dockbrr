export function DigestShort({ digest }: { digest: string }) {
  if (!digest) return <span className="opacity-50">-</span>;
  const [algo, hex = ""] = digest.split(":");
  const short = hex ? `${algo}:${hex.slice(0, 12)}` : digest.slice(0, 12);
  return <span className="font-mono text-xs" title={digest}>{short}</span>;
}
