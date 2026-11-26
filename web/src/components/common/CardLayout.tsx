import type * as React from "react";
import { cn } from "@/lib/utils";

/*
 * Multi-column card layouts that always end level: never an empty gap under
 * a shorter column.
 *
 * - CardGrid pairs cards row by row. Every cell stretches to the height of
 *   its row (Card is a flex column and the SectionCard body is flex-1, so the
 *   extra height lands inside the card), and a lone trailing card spans the
 *   whole row. Adding or removing a card therefore never leaves a hole.
 * - CardStack is a column of cards for one CardGrid cell. The cell stretches
 *   to the row and the stack's last card grows to fill it, so two stacks of
 *   different heights still end level.
 *
 * Put cards with similar heights side by side; stretching is for absorbing
 * the small difference, not for hiding a half-empty column.
 */

type Breakpoint = "md" | "lg" | "xl";
type Split = "1:1" | "7:5" | "5:7" | "2:1";
type Gap = 3 | 5;

const GRID_COLUMNS: Record<Breakpoint, Record<Split, string>> = {
  md: {
    "1:1": "md:grid-cols-2",
    "7:5": "md:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]",
    "5:7": "md:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]",
    "2:1": "md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]",
  },
  lg: {
    "1:1": "lg:grid-cols-2",
    "7:5": "lg:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]",
    "5:7": "lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]",
    "2:1": "lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]",
  },
  xl: {
    "1:1": "xl:grid-cols-2",
    "7:5": "xl:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]",
    "5:7": "xl:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]",
    "2:1": "xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]",
  },
};

const GAPS: Record<Gap, string> = { 3: "gap-3", 5: "gap-5" };

export interface CardGridProps extends React.ComponentProps<"div"> {
  /** Column widths from `breakpoint` up; one column below it. */
  split?: Split;
  breakpoint?: Breakpoint;
  /** 3 inside a page section (12px), 5 between page sections (20px). */
  gap?: Gap;
}

/** Two columns of cards whose rows always end level. */
export function CardGrid({ split = "1:1", breakpoint = "lg", gap = 3, className, ...props }: CardGridProps) {
  return (
    <div
      data-slot="card-grid"
      className={cn(
        "grid min-w-0 grid-cols-1 items-stretch",
        GRID_COLUMNS[breakpoint][split],
        GAPS[gap],
        // A lone trailing card spans the row instead of leaving an empty cell.
        "*:min-w-0 [&>:last-child:nth-child(odd)]:col-span-full",
        className,
      )}
      {...props}
    />
  );
}

export interface CardStackProps extends React.ComponentProps<"div"> {
  gap?: Gap;
}

/** A column of cards inside a CardGrid cell; its last card grows to the row height. */
export function CardStack({ gap = 3, className, ...props }: CardStackProps) {
  return (
    <div
      data-slot="card-stack"
      className={cn("flex min-w-0 flex-col [&>:last-child]:flex-1", GAPS[gap], className)}
      {...props}
    />
  );
}
