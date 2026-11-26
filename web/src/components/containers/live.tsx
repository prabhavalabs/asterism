import { useEffect, useRef } from "react";
import { liveStore } from "@/live/LiveStore";
import { cn } from "@/lib/utils";

export interface LiveMetricTextProps {
  entityId: string;
  metric: string;
  /** Shown until the first live point (the last polled value). */
  fallback: number;
  /** Module-level formatter: a new identity re-subscribes. */
  format: (value: number) => string;
  /** Show the fallback text instead of live values (stopped containers). */
  live?: boolean;
  className?: string;
}

/**
 * A number that follows the LiveStore without React re-renders: the text node
 * is rewritten on each rAF-batched series update.
 */
export function LiveMetricText({ entityId, metric, fallback, format, live = true, className }: LiveMetricTextProps) {
  const ref = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const draw = () => {
      const v = live ? (liveStore.getLatest(entityId, metric) ?? fallback) : fallback;
      const text = format(v);
      if (el.textContent !== text) el.textContent = text;
    };
    draw();
    if (!live) return;
    return liveStore.subscribeSeries(entityId, metric, draw);
  }, [entityId, metric, fallback, format, live]);

  return <span ref={ref} className={cn("tabular-nums", className)} />;
}
