export const SPARK_VIEW_W = 100;
export const SPARK_VIEW_H = 32;

/** Builds line and area paths for the last `windowSec` seconds of a series. */
export function sparklinePaths(
  timestamps: readonly number[],
  values: readonly number[],
  windowSec: number,
  range?: readonly [number, number],
): { line: string; area: string } | null {
  if (timestamps.length < 2) return null;
  const end = timestamps[timestamps.length - 1];
  const start = end - windowSec;
  let first = timestamps.length - 1;
  while (first > 0 && timestamps[first - 1] >= start) first--;
  if (timestamps.length - first < 2) first = Math.max(0, timestamps.length - 2);

  let min = range ? range[0] : Infinity;
  let max = range ? range[1] : -Infinity;
  if (!range) {
    for (let i = first; i < values.length; i++) {
      if (values[i] < min) min = values[i];
      if (values[i] > max) max = values[i];
    }
    const pad = (max - min) * 0.15 || Math.abs(max) * 0.1 || 1;
    min -= pad;
    max += pad;
  }
  const span = max - min || 1;
  const t0 = timestamps[first];
  const tSpan = end - t0 || 1;

  let line = "";
  for (let i = first; i < timestamps.length; i++) {
    const x = ((timestamps[i] - t0) / tSpan) * SPARK_VIEW_W;
    const ratio = Math.min(1, Math.max(0, (values[i] - min) / span));
    const y = SPARK_VIEW_H - 1 - ratio * (SPARK_VIEW_H - 2);
    line += `${i === first ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
  }
  return { line, area: `${line}L${SPARK_VIEW_W},${SPARK_VIEW_H}L0,${SPARK_VIEW_H}Z` };
}
