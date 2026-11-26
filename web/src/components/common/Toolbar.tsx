import type * as React from "react";
import { cn } from "@/lib/utils";

export interface ToolbarProps extends React.ComponentProps<"div"> {
  /**
   * `default` locks direct Button/Input children to --control-h (32px); `sm`
   * to --control-h-sm (28px). SegmentedControl, SearchInput and TabsList take
   * the matching `size` prop.
   */
  size?: "default" | "sm";
}

/** A row of controls that all share one height. Wraps on narrow screens. */
export function Toolbar({ size = "default", className, ...props }: ToolbarProps) {
  return (
    <div
      data-slot="toolbar"
      data-size={size}
      className={cn(
        "flex min-w-0 flex-wrap items-center",
        size === "default"
          ? "min-h-control gap-2 [&>[data-slot=button]]:h-control [&>[data-slot=button][data-size^=icon]]:w-control [&>[data-slot=input]]:h-control"
          : "min-h-control-sm gap-1.5 [&>[data-slot=button]]:h-control-sm [&>[data-slot=button][data-size^=icon]]:w-control-sm [&>[data-slot=input]]:h-control-sm",
        className,
      )}
      {...props}
    />
  );
}

/** Groups controls inside a Toolbar so they wrap together. */
export function ToolbarGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="toolbar-group"
      className={cn("flex min-w-0 flex-wrap items-center gap-[inherit]", className)}
      {...props}
    />
  );
}

/** Pushes the following controls to the right edge. */
export function ToolbarSpacer({ className, ...props }: React.ComponentProps<"div">) {
  return <div aria-hidden="true" className={cn("flex-1", className)} {...props} />;
}
