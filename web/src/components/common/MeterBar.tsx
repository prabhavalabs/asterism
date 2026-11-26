import type * as React from "react";
import { cn } from "@/lib/utils";
import {
  DEFAULT_METER_THRESHOLDS,
  resolveMeterTone,
  type MeterThresholds,
  type MeterTone,
} from "./tones";

const FILL: Record<Exclude<MeterTone, "auto">, string> = {
  healthy: "bg-status-healthy",
  warning: "bg-status-warning",
  critical: "bg-status-critical",
  info: "bg-status-info",
  neutral: "bg-status-neutral",
  primary: "bg-foreground",
  "chart-1": "bg-chart-1",
  "chart-2": "bg-chart-2",
  "chart-3": "bg-chart-3",
  "chart-4": "bg-chart-4",
  "chart-5": "bg-chart-5",
  "role-proxy": "bg-role-proxy",
  "role-app": "bg-role-app",
  "role-db": "bg-role-db",
  "role-cache": "bg-role-cache",
  "role-worker": "bg-role-worker",
};

const HEIGHT = { sm: "h-1", default: "h-1.5", lg: "h-2.5" } as const;

export interface MeterSegment {
  /** Ratio 0..1 of the whole track. */
  value: number;
  tone: Exclude<MeterTone, "auto">;
  label?: string;
}

export interface MeterBarProps extends Omit<React.ComponentProps<"div">, "children"> {
  /** Ratio 0..1 (clamped). Ignored when `segments` is given. */
  value?: number;
  /** Default `auto`: chart-1 below `thresholds.warning`, then warning, then critical. */
  tone?: MeterTone;
  thresholds?: MeterThresholds;
  /** Diagonal hatch texture over the fill. */
  hatch?: boolean;
  /** Track height: sm 4px, default 6px, lg 10px. */
  size?: keyof typeof HEIGHT;
  /** Stacked segments (for example per-project memory). */
  segments?: readonly MeterSegment[];
  /** Accessible name, for example "Memory used". */
  label?: string;
  /** Accessible value text, for example "3.1 GiB of 8 GiB". */
  valueText?: string;
}

function clamp(n: number): number {
  if (!Number.isFinite(n)) return 0;
  return Math.min(1, Math.max(0, n));
}

/**
 * Thin capacity meter with 4px rounded ends (rounded-bar). A single fill is
 * rounded at both ends; stacked segments are clipped by the track, so only
 * the outer ends round. Width changes ease in 220ms (off under reduced motion). */
export function MeterBar({
  value = 0,
  tone = "auto",
  thresholds = DEFAULT_METER_THRESHOLDS,
  hatch = false,
  size = "default",
  segments,
  label,
  valueText,
  className,
  ...props
}: MeterBarProps) {
  const ratio = clamp(value);
  const fillBase = cn(
    "h-full transition-[width] duration-tabs ease-out motion-reduce:transition-none",
    hatch && "hatch",
  );

  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round((segments ? segments.reduce((s, x) => s + clamp(x.value), 0) : ratio) * 100)}
      aria-valuetext={valueText}
      data-slot="meter-bar"
      className={cn("flex w-full overflow-hidden rounded-bar bg-meter-track", HEIGHT[size], className)}
      {...props}
    >
      {segments ? (
        segments.map((segment, index) => (
          <div
            key={segment.label ?? index}
            title={segment.label}
            className={cn(fillBase, FILL[segment.tone], index > 0 && "border-l border-card")}
            style={{ width: `${clamp(segment.value) * 100}%` }}
          />
        ))
      ) : (
        <div
          className={cn(fillBase, "rounded-bar", FILL[resolveMeterTone(tone, ratio, thresholds)])}
          style={{ width: `${ratio * 100}%` }}
        />
      )}
    </div>
  );
}
