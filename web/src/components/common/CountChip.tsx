import type * as React from "react";
import { cn } from "@/lib/utils";

/** 20px tabular count beside a tab label (rounded-chip, 12px mono). */
export function CountChip({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      data-slot="count-chip"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-chip bg-foreground/6 px-1.5 font-mono text-pill text-text-2 tabular-nums",
        className,
      )}
      {...props}
    />
  );
}
