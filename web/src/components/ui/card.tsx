import type * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cn } from "@/lib/utils";

/**
 * Card: 8px corners (rounded-card) with a 1px foreground/10 ring (shadow-card). The
 * header is 44px minimum (title left, sm actions right) with a divider; the
 * body pads 16px.
 */
function Card({
  className,
  asChild = false,
  ...props
}: React.ComponentProps<"div"> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "div";
  return (
    <Comp
      data-slot="card"
      className={cn(
        "group/card flex flex-col overflow-hidden rounded-card bg-card text-body text-card-foreground shadow-card",
        className,
      )}
      {...props}
    />
  );
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn(
        "grid min-h-11 auto-rows-min content-center items-center gap-x-3 gap-y-0.5 border-b border-divider px-4 py-2",
        "has-data-[slot=card-action]:grid-cols-[minmax(0,1fr)_auto]",
        className,
      )}
      {...props}
    />
  );
}

function CardTitle({ className, ...props }: React.ComponentProps<"h3">) {
  return (
    <h3
      data-slot="card-title"
      className={cn("min-w-0 truncate font-heading text-heading text-foreground", className)}
      {...props}
    />
  );
}

function CardDescription({ className, ...props }: React.ComponentProps<"p">) {
  return (
    <p
      data-slot="card-description"
      className={cn("min-w-0 text-small text-muted-foreground", className)}
      {...props}
    />
  );
}

/** Right-aligned header actions. Use sm controls here (h-control-sm). */
function CardAction({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-action"
      className={cn(
        "col-start-2 row-span-2 row-start-1 flex items-center gap-1.5 self-center justify-self-end",
        className,
      )}
      {...props}
    />
  );
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-content" className={cn("p-4", className)} {...props} />;
}

function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-footer"
      className={cn(
        "flex min-h-11 items-center gap-2 border-t border-divider bg-surface-subtle px-4 py-2 text-small text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}

export { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle };
