import { useEffect } from "react";
import { metricsClient } from "@/lib/rpc";
import { liveStore } from "@/live/LiveStore";

/** Host series the overview and host pages chart. */
export const HOST_METRICS = ["cpu.util", "mem.used_bytes", "net.rx_bps", "net.tx_bps"] as const;

const HISTORY_MS = 15 * 60_000;

/**
 * Seeds the LiveStore with the last 15 minutes of host metrics so charts and
 * sparklines start full; the live stream then appends to the same buffers.
 */
export function useHostHistory(): void {
  useEffect(() => {
    let active = true;
    const now = Date.now();
    metricsClient
      .queryRange({
        entityId: "host",
        metrics: [...HOST_METRICS],
        fromMs: BigInt(now - HISTORY_MS),
        toMs: BigInt(now),
      })
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
        // History is a nicety; the live stream still fills the charts.
      });
    return () => {
      active = false;
    };
  }, []);
}
