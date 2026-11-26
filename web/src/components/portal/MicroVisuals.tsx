import { useEffect, useRef } from "react";
import { liveStore } from "@/live/LiveStore";
import { SPARK_VIEW_H, SPARK_VIEW_W, sparklinePaths } from "./sparkline";
import { cn } from "@/lib/utils";

export type SparklineTone = "chart-1" | "chart-2" | "chart-3" | "chart-4" | "chart-5";

const TONE_CLASSES: Record<SparklineTone, { line: string; area: string }> = {
  "chart-1": { line: "stroke-chart-1", area: "fill-chart-1/12" },
  "chart-2": { line: "stroke-chart-2", area: "fill-chart-2/12" },
  "chart-3": { line: "stroke-chart-3", area: "fill-chart-3/12" },
  "chart-4": { line: "stroke-chart-4", area: "fill-chart-4/12" },
  "chart-5": { line: "stroke-chart-5", area: "fill-chart-5/12" },
};

export interface LiveSparklineProps {
  entityId: string;
  metric: string;
  tone?: SparklineTone;
  /** Seconds of history to draw. Default 5 minutes. */
  windowSec?: number;
  /** Fixed y range (for ratios use [0, 1]); otherwise fitted to the data. Pass a module-level constant. */
  range?: readonly [number, number];
  className?: string;
}

/**
 * Small live trend line drawn straight from the LiveStore ring buffer. Path
 * data is written to the DOM on each rAF-batched series update, so per-second
 * points never pass through React state.
 */
export function LiveSparkline({
  entityId,
  metric,
  tone = "chart-1",
  windowSec = 300,
  range,
  className,
}: LiveSparklineProps) {
  const lineRef = useRef<SVGPathElement>(null);
  const areaRef = useRef<SVGPathElement>(null);

  useEffect(() => {
    const draw = () => {
      const series = liveStore.getSeries(entityId, metric);
      const paths = sparklinePaths(series.timestamps, series.values, windowSec, range);
      lineRef.current?.setAttribute("d", paths?.line ?? "");
      areaRef.current?.setAttribute("d", paths?.area ?? "");
    };
    draw();
    return liveStore.subscribeSeries(entityId, metric, draw);
  }, [entityId, metric, windowSec, range]);

  const classes = TONE_CLASSES[tone];
  return (
    <svg
      aria-hidden="true"
      data-slot="live-sparkline"
      viewBox={`0 0 ${SPARK_VIEW_W} ${SPARK_VIEW_H}`}
      preserveAspectRatio="none"
      className={cn("block h-8 w-full overflow-visible", className)}
    >
      <path ref={areaRef} className={cn("stroke-none", classes.area)} />
      <path
        ref={lineRef}
        className={cn("fill-none", classes.line)}
        strokeWidth={1.5}
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
