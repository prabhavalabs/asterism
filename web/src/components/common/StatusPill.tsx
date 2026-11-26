import type * as React from "react";
import type { HealthStatus } from "@/gen/asterism/v1/asterism_pb";
import { cn } from "@/lib/utils";
import {
  TONE_DOT_CLASSES,
  TONE_PILL_CLASSES,
  healthStyle,
  severityStyle,
  type StatusTone,
} from "./tones";

type StatusPillBase = Omit<React.ComponentProps<"span">, "children"> & {
  /** Overrides the default label, for example to add a count. */
  children?: React.ReactNode;
  /** Adds a soft ping around the dot (live states). Off under reduced motion. */
  pulse?: boolean;
};

export type StatusPillProps = StatusPillBase &
  (
    | { health: HealthStatus; severity?: never; tone?: never }
    | { severity: string; health?: never; tone?: never }
    | { tone: StatusTone; health?: never; severity?: never }
  );

function resolve(props: { health?: HealthStatus; severity?: string; tone?: StatusTone }): {
  tone: StatusTone;
  label: string;
} {
  if (props.health !== undefined) return healthStyle(props.health);
  if (props.severity !== undefined) return severityStyle(props.severity);
  return { tone: props.tone ?? "neutral", label: "" };
}

/**
 * Status pill (22px, 12px text, 6px dot). Pass exactly one of `health`
 * (HealthStatus), `severity` (event severity string) or `tone` + children.
 */
export function StatusPill({
  health,
  severity,
  tone: toneProp,
  pulse = false,
  className,
  children,
  ...props
}: StatusPillProps) {
  const { tone, label } = resolve({ health, severity, tone: toneProp });

  return (
    <span
      data-slot="status-pill"
      data-tone={tone}
      className={cn(
        "inline-flex h-pill shrink-0 items-center gap-1.5 whitespace-nowrap rounded-chip px-2 font-medium text-pill",
        TONE_PILL_CLASSES[tone],
        className,
      )}
      {...props}
    >
      <StatusDot tone={tone} pulse={pulse} />
      {children ?? label}
    </span>
  );
}

export interface StatusDotProps extends Omit<React.ComponentProps<"span">, "children"> {
  tone: StatusTone;
  pulse?: boolean;
}

/** The pill's 6px dot on its own. Decorative: say the status in the row's text. */
export function StatusDot({ tone, pulse = false, className, ...props }: StatusDotProps) {
  return (
    <span
      aria-hidden="true"
      data-slot="status-dot"
      data-tone={tone}
      className={cn("relative inline-flex size-1.5 shrink-0", className)}
      {...props}
    >
      {pulse && (
        <span
          className={cn(
            "absolute inset-0 animate-ping rounded-full opacity-60 motion-reduce:hidden",
            TONE_DOT_CLASSES[tone],
          )}
        />
      )}
      <span className={cn("relative inline-flex size-1.5 rounded-full", TONE_DOT_CLASSES[tone])} />
    </span>
  );
}
