import type * as React from "react";
import { cn } from "@/lib/utils";

export interface SectionHeaderProps extends Omit<React.ComponentProps<"div">, "title"> {
  title: React.ReactNode;
  /** Count shown next to the title (tabular). */
  count?: number;
  description?: React.ReactNode;
  /** Controls on the right (default-size Buttons, SearchInput, SegmentedControl). */
  actions?: React.ReactNode;
  /** Heading level; defaults to h2. */
  as?: "h2" | "h3";
}

/**
 * Heading for a page section that is not itself a card (a grid of cards
 * underneath). 15px title, 12.5px description, controls on the right that
 * wrap under the title on phones.
 */
export function SectionHeader({
  title,
  count,
  description,
  actions,
  as: Heading = "h2",
  className,
  ...props
}: SectionHeaderProps) {
  return (
    <div
      data-slot="section-header"
      className={cn("flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between", className)}
      {...props}
    >
      <div className="flex min-w-0 flex-col gap-0.5">
        <Heading className="flex items-baseline gap-2 font-heading text-heading text-foreground">
          {title}
          {count !== undefined && (
            <span className="font-mono text-small font-normal text-muted-foreground tabular-nums">
              {count}
            </span>
          )}
        </Heading>
        {description && <p className="text-small text-muted-foreground">{description}</p>}
      </div>
      {actions && (
        <div className="flex min-h-control min-w-0 flex-wrap items-center gap-2">{actions}</div>
      )}
    </div>
  );
}
