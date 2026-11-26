/** Tick and range helpers for the live uPlot charts. */

const pad2 = (n: number) => String(n).padStart(2, "0");

/** Single-line time ticks: `14:05` or `14:05:30` when ticks are under a minute apart. */
export function formatTimeTicks(splits: readonly number[], incr: number): string[] {
  const withSeconds = incr < 60;
  return splits.map((ts) => {
    const d = new Date(ts * 1000);
    const hm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
    return withSeconds ? `${hm}:${pad2(d.getSeconds())}` : hm;
  });
}

/** Zero-based range with headroom for non-negative data; symmetric padding otherwise. */
export function autoRange(min: number | null, max: number | null): [number, number] {
  const lo = min ?? 0;
  const hi = max ?? 0;
  if (lo >= 0) return [0, hi > 0 ? hi * 1.15 : 1];
  const span = hi - lo || Math.abs(lo) || 1;
  return [lo - span * 0.1, hi + span * 0.1];
}
