import type * as React from "react";
import { Icon, type IconData } from "@/components/ui/icon";
import { cn } from "@/lib/utils";
import { TONE_TEXT_CLASSES, type StatusTone } from "./tones";

export interface StatCardProps extends Omit<React.ComponentProps<"div">, "title"> {
  /** 12.5px muted label. */
  label: React.ReactNode;
  /** 28px semibold tabular value. */
  value: React.ReactNode;
  /** Unit or secondary value shown next to the value (muted). */
  unit?: React.ReactNode;
  /** 12.5px line under the value (for example "of 8 GiB · 41%"). */
  sub?: React.ReactNode;
  icon?: IconData;
  /** Colours the icon. */
  tone?: StatusTone;
  /** Element at the top right (a StatusPill, a sm Button). */
  action?: React.ReactNode;
  /** Metric numerals: the heading face with fixed-width digits (default true). */
  mono?: boolean;
  /** Content under the value: a MeterBar, a sparkline. */
  children?: React.ReactNode;
}

/** KPI tile: square, px-4 py-3.5, label, big tabular value, sub line, optional meter/sparkline. */
export function StatCard({
  label,
  value,
  unit,
  sub,
  icon,
  tone,
  action,
  mono = true,
  className,
  children,
  ...props
}: StatCardProps) {
  return (
    <div
      data-slot="stat-card"
      className={cn("flex min-w-0 flex-col gap-1.5 rounded-card bg-card px-4 py-3.5 shadow-card", className)}
      {...props}
    >
      <div className="flex min-h-control-sm items-center gap-2">
        {icon && (
          <Icon
            icon={icon}
            className={cn("size-4 shrink-0", tone ? TONE_TEXT_CLASSES[tone] : "text-text-4")}
          />
        )}
        <span className="min-w-0 flex-1 truncate text-small font-medium text-muted-foreground">
          {label}
        </span>
        {action && <div className="flex shrink-0 items-center gap-1.5">{action}</div>}
      </div>
      <div className="flex min-w-0 items-baseline gap-1.5">
        <span
          className={cn("truncate text-kpi text-foreground tabular-nums", mono && "font-heading tracking-[-0.02em]")}
        >
          {value}
        </span>
        {unit && <span className="shrink-0 text-control text-muted-foreground">{unit}</span>}
      </div>
      {sub && <div className="min-w-0 truncate text-small text-muted-foreground tabular-nums">{sub}</div>}
      {children && <div className="mt-1 min-w-0">{children}</div>}
    </div>
  );
}
