import { useSyncExternalStore } from "react";
import type { Event } from "@/gen/asterism/v1/asterism_pb";
import { liveClient } from "@/lib/rpc";

export interface SeriesData {
  timestamps: number[]; // unix seconds (uPlot native)
  values: number[];
}

type SeriesListener = () => void;
type EventListener = (event: Event) => void;

const DEFAULT_CAPACITY = 900; // 15 minutes at 1 Hz

/**
 * Plain TypeScript ring-buffer store outside React state.
 * Routes 1 Hz ConnectRPC stream frames directly into uPlot instances via rAF
 * and exposes a 4 Hz throttled snapshot hook for numeric tiles.
 */
export class LiveStore {
  private readonly capacity: number;
  private readonly series = new Map<string, { ts: number[]; vals: number[] }>();
  private readonly latest = new Map<string, number>();
  private readonly seriesListeners = new Map<string, Set<SeriesListener>>();
  private readonly valueListeners = new Set<SeriesListener>();
  private readonly eventListeners = new Set<EventListener>();

  private valueFlushTimer: ReturnType<typeof setTimeout> | null = null;
  private rafHandle: number | null = null;
  private dirtySeries = new Set<string>();

  private abortController: AbortController | null = null;
  private running = false;
  private connected = false;
  private visibilityHandler: (() => void) | null = null;

  constructor(capacity = DEFAULT_CAPACITY) {
    this.capacity = capacity;
  }

  static key(entityId: string, metric: string): string {
    return `${entityId}|${metric}`;
  }

  seed(entityId: string, metric: string, points: Array<{ tsMs: number; value: number }>): void {
    const k = LiveStore.key(entityId, metric);
    const existing = this.series.get(k);
    const map = new Map<number, number>();
    if (existing) {
      for (let i = 0; i < existing.ts.length; i++) {
        map.set(existing.ts[i], existing.vals[i]);
      }
    }
    for (const pt of points) {
      const sec = Math.floor(pt.tsMs / 1000);
      map.set(sec, pt.value);
    }
    const sortedTs = Array.from(map.keys()).sort((a, b) => a - b);
    const trimmedTs = sortedTs.slice(-this.capacity);
    const trimmedVals = trimmedTs.map((t) => map.get(t) ?? 0);
    this.series.set(k, { ts: trimmedTs, vals: trimmedVals });
    if (trimmedVals.length > 0) {
      this.latest.set(k, trimmedVals[trimmedVals.length - 1]);
    }
    this.markDirty(k);
  }

  push(entityId: string, metric: string, tsMs: number, value: number): void {
    const k = LiveStore.key(entityId, metric);
    const sec = Math.floor(tsMs / 1000);
    let buf = this.series.get(k);
    if (!buf) {
      buf = { ts: [], vals: [] };
      this.series.set(k, buf);
    }
    const lastIdx = buf.ts.length - 1;
    if (lastIdx >= 0 && buf.ts[lastIdx] === sec) {
      buf.vals[lastIdx] = value;
    } else {
      buf.ts.push(sec);
      buf.vals.push(value);
      if (buf.ts.length > this.capacity) {
        buf.ts.shift();
        buf.vals.shift();
      }
    }
    this.latest.set(k, value);
    this.markDirty(k);
  }

  getSeries(entityId: string, metric: string): SeriesData {
    const buf = this.series.get(LiveStore.key(entityId, metric));
    if (!buf) {
      return { timestamps: [], values: [] };
    }
    return { timestamps: buf.ts, values: buf.vals };
  }

  getLatest(entityId: string, metric: string): number | undefined {
    return this.latest.get(LiveStore.key(entityId, metric));
  }

  isConnected(): boolean {
    return this.connected;
  }

  subscribeSeries(entityId: string, metric: string, listener: SeriesListener): () => void {
    const k = LiveStore.key(entityId, metric);
    let set = this.seriesListeners.get(k);
    if (!set) {
      set = new Set();
      this.seriesListeners.set(k, set);
    }
    set.add(listener);
    return () => {
      set?.delete(listener);
    };
  }

  subscribeValues(listener: SeriesListener): () => void {
    this.valueListeners.add(listener);
    return () => {
      this.valueListeners.delete(listener);
    };
  }

  /**
   * Called for every event the live stream pushes (lifecycle changes,
   * alerts, audited actions). Listeners get the event itself; lists that
   * page events should refetch rather than splice it in.
   */
  subscribeEvents(listener: EventListener): () => void {
    this.eventListeners.add(listener);
    return () => {
      this.eventListeners.delete(listener);
    };
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.visibilityHandler = () => {
      if (this.running) {
        this.restartStream();
      }
    };
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", this.visibilityHandler);
    }
    void this.connectLoop();
  }

  stop(): void {
    this.running = false;
    this.connected = false;
    this.abortController?.abort();
    this.abortController = null;
    if (this.visibilityHandler && typeof document !== "undefined") {
      document.removeEventListener("visibilitychange", this.visibilityHandler);
      this.visibilityHandler = null;
    }
    this.scheduleValueNotify();
  }

  private restartStream(): void {
    this.abortController?.abort();
  }

  private async connectLoop(): Promise<void> {
    let backoffMs = 500;
    while (this.running) {
      const ac = new AbortController();
      this.abortController = ac;
      const hidden = typeof document !== "undefined" && document.visibilityState === "hidden";
      const intervalMs = hidden ? 5000 : 1000;

      const dict = new Map<number, { entityId: string; metric: string }>();

      try {
        const stream = liveClient.subscribe(
          { topics: ["metrics:*"], intervalMs },
          { signal: ac.signal },
        );
        this.connected = true;
        backoffMs = 500;
        this.scheduleValueNotify();

        for await (const frame of stream) {
          if (frame.payload.case === "seriesDict") {
            for (const entry of frame.payload.value.entries) {
              dict.set(entry.seriesId, {
                entityId: entry.entityId,
                metric: entry.metric,
              });
            }
          } else if (frame.payload.case === "event") {
            const event = frame.payload.value;
            for (const cb of this.eventListeners) cb(event);
          } else if (frame.payload.case === "metricBatch") {
            const batch = frame.payload.value;
            const tsMs = Number(batch.tsMs);
            for (let i = 0; i < batch.seriesIds.length; i++) {
              const info = dict.get(batch.seriesIds[i]);
              if (info) {
                this.push(info.entityId, info.metric, tsMs, batch.values[i]);
              }
            }
          }
        }
      } catch {
        // Reconnect cleanly on stream close or visibility-triggered restart.
      } finally {
        this.connected = false;
        this.scheduleValueNotify();
      }

      if (!this.running) break;
      const jitter = Math.floor(Math.random() * 250);
      await new Promise((resolve) => setTimeout(resolve, backoffMs + jitter));
      backoffMs = Math.min(backoffMs * 2, 10000);
    }
  }

  private markDirty(key: string): void {
    this.dirtySeries.add(key);
    this.scheduleSeriesFlush();
    this.scheduleValueNotify();
  }

  private scheduleSeriesFlush(): void {
    if (this.rafHandle !== null) return;
    const flush = () => {
      this.rafHandle = null;
      const dirty = Array.from(this.dirtySeries);
      this.dirtySeries.clear();
      for (const k of dirty) {
        const listeners = this.seriesListeners.get(k);
        if (listeners) {
          for (const cb of listeners) cb();
        }
      }
    };
    if (typeof requestAnimationFrame !== "undefined") {
      this.rafHandle = requestAnimationFrame(flush);
    } else {
      flush();
    }
  }

  private scheduleValueNotify(): void {
    if (this.valueFlushTimer !== null) return;
    // Throttle React numeric tile updates to at most 4 Hz (250 ms).
    this.valueFlushTimer = setTimeout(() => {
      this.valueFlushTimer = null;
      for (const cb of this.valueListeners) cb();
    }, 250);
  }
}

export const liveStore = new LiveStore();

export function useLiveValue(
  entityId: string,
  metric: string,
  fallback: number,
): number {
  return useSyncExternalStore(
    (cb) => liveStore.subscribeValues(cb),
    () => liveStore.getLatest(entityId, metric) ?? fallback,
    () => fallback,
  );
}

export function useLiveConnected(): boolean {
  return useSyncExternalStore(
    (cb) => liveStore.subscribeValues(cb),
    () => liveStore.isConnected(),
    () => false,
  );
}
