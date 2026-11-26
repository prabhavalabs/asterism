import { metricsClient } from "@/lib/rpc";
import { liveStore } from "@/live/LiveStore";

/** Live container metric names streamed for `ctr:<name>` entities. */
export const CONTAINER_METRICS = [
  "cpu.util",
  "mem.used_bytes",
  "net.rx_bps",
  "net.tx_bps",
  "blk.read_bps",
  "blk.write_bps",
] as const;

const seeded = new Set<string>();

/**
 * Backfills recent history into the LiveStore so sparklines and charts start
 * filled. Each entity/metric pair is fetched at most once per session; later
 * points arrive on the live stream.
 */
export function seedContainerHistory(
  entityIds: readonly string[],
  metrics: readonly string[],
  windowMs: number,
): () => void {
  let active = true;
  const now = Date.now();
  for (const entityId of entityIds) {
    const todo = metrics.filter((m) => !seeded.has(`${entityId}|${m}|${windowMs}`));
    if (todo.length === 0) continue;
    for (const m of todo) seeded.add(`${entityId}|${m}|${windowMs}`);
    void metricsClient
      .queryRange({ entityId, metrics: todo, fromMs: BigInt(now - windowMs), toMs: BigInt(now) })
      .then((res) => {
        if (!active) return;
        for (const s of res.series) {
          liveStore.seed(
            s.entityId,
            s.metric,
            s.points.map((pt) => ({ tsMs: Number(pt.tsMs), value: pt.value })),
          );
        }
      })
      .catch(() => {
        for (const m of todo) seeded.delete(`${entityId}|${m}|${windowMs}`);
      });
  }
  return () => {
    active = false;
  };
}
