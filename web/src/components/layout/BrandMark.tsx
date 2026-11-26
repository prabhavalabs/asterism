import { useId } from "react";
import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Quadrant geometry from scripts/brand/generate.py: four tiles on a 64 grid
 * whose rounded inner corners open the gutters into a four-point star.
 */
const QUADRANT =
  "M15 5H27A3 3 0 0 1 30 8V14A16 16 0 0 1 14 30H8A3 3 0 0 1 5 27V15A10 10 0 0 1 15 5Z" +
  "M49 5H37A3 3 0 0 0 34 8V14A16 16 0 0 0 50 30H56A3 3 0 0 0 59 27V15A10 10 0 0 0 49 5Z" +
  "M15 59H27A3 3 0 0 0 30 56V50A16 16 0 0 0 14 34H8A3 3 0 0 0 5 37V49A10 10 0 0 0 15 59Z" +
  "M49 59H37A3 3 0 0 1 34 56V50A16 16 0 0 1 50 34H56A3 3 0 0 1 59 37V49A10 10 0 0 1 49 59Z";

const STOPS = [
  ["0", "var(--brand-1)"],
  [".34", "var(--brand-2)"],
  [".7", "var(--brand-3)"],
  ["1", "var(--brand-4)"],
] as const;

type BrandMarkProps = React.ComponentProps<"svg"> & {
  /** "color" uses the brand gradient; "mono" paints in the current text colour. */
  variant?: "color" | "mono";
};

/** The Asterism Quadrant mark. */
export function BrandMark({ className, variant = "color", ...props }: BrandMarkProps) {
  const gradientId = `brand-${useId().replace(/:/g, "")}`;
  return (
    <svg viewBox="0 0 64 64" fill="none" aria-hidden="true" className={cn("size-4", className)} {...props}>
      {variant === "color" && (
        <defs>
          <linearGradient id={gradientId} gradientUnits="userSpaceOnUse" x1="5" y1="5" x2="59" y2="59">
            {STOPS.map(([offset, color]) => (
              <stop key={offset} offset={offset} style={{ stopColor: color }} />
            ))}
          </linearGradient>
        </defs>
      )}
      <path d={QUADRANT} fill={variant === "color" ? `url(#${gradientId})` : "currentColor"} />
    </svg>
  );
}
