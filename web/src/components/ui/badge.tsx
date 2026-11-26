import type * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/**
 * Fixed-height pill (rounded-chip 6px) (--pill-h, 22px) so pills on one row always align.
 * Text 12px, icons 12px. Use `mono` for ports, IDs and counts.
 */
const badgeVariants = cva(
  [
    "group/badge inline-flex h-pill w-fit shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap rounded-chip border border-transparent px-2 font-medium text-pill transition-colors",
    "focus-visible:border-ring focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring/50",
    "has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5",
    "[&>svg]:pointer-events-none [&>svg]:size-3 [&>svg]:shrink-0",
  ],
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground",
        secondary: "border-border bg-secondary text-text-2",
        outline: "border-border text-text-2",
        healthy: "bg-status-healthy-surface text-status-healthy",
        warning: "bg-status-warning-surface text-status-warning",
        critical: "bg-status-critical-surface text-status-critical",
        info: "bg-status-info-surface text-status-info",
        neutral: "bg-status-neutral-surface text-status-neutral",
      },
      mono: {
        true: "font-mono tracking-normal tabular-nums",
        false: "",
      },
    },
    defaultVariants: {
      variant: "secondary",
      mono: false,
    },
  },
);

export type BadgeVariant = NonNullable<VariantProps<typeof badgeVariants>["variant"]>;

export interface BadgeProps
  extends React.ComponentProps<"span">,
    VariantProps<typeof badgeVariants> {
  asChild?: boolean;
}

function Badge({ className, variant = "secondary", mono = false, asChild = false, ...props }: BadgeProps) {
  const Comp = asChild ? Slot : "span";
  return (
    <Comp
      data-slot="badge"
      data-variant={variant}
      className={cn(badgeVariants({ variant, mono }), className)}
      {...props}
    />
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export { Badge, badgeVariants };
