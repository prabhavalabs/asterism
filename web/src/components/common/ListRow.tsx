import type * as React from "react";
import { Slot, Slottable } from "@radix-ui/react-slot";
import { cn } from "@/lib/utils";

export interface ListRowProps extends Omit<React.ComponentProps<"div">, "title"> {
  /** Icon, avatar or StatusDot on the left. */
  leading?: React.ReactNode;
  /** 14px medium, truncates. */
  title?: React.ReactNode;
  /** 12.5px muted line under the title, truncates. */
  description?: React.ReactNode;
  /** Right-aligned muted text (values, timestamps), tabular. */
  meta?: React.ReactNode;
  /** Right-most slot: pills, chips, a chevron, sm buttons. */
  trailing?: React.ReactNode;
  /** Hover background and pointer. Implied by `asChild`. */
  interactive?: boolean;
  /** Selected/current row. */
  active?: boolean;
  /**
   * Render the single child element (an empty Link or button) as the row;
   * the row content is placed inside it.
   */
  asChild?: boolean;
}

/**
 * List row, min 40px, px-4 so it lines up with SectionCard content. Stack
 * rows in a `divide-y divide-divider` container (SectionCard `flush`).
 */
export function ListRow({
  leading,
  title,
  description,
  meta,
  trailing,
  interactive = false,
  active = false,
  asChild = false,
  className,
  children,
  ...props
}: ListRowProps) {
  const Comp = asChild ? Slot : "div";
  const isInteractive = interactive || asChild;

  const body =
    title !== undefined || description !== undefined ? (
      <div className="flex min-w-0 flex-1 flex-col">
        {title !== undefined && (
          <div className="truncate text-body font-medium text-foreground">{title}</div>
        )}
        {description !== undefined && (
          <div className="truncate text-small text-muted-foreground">{description}</div>
        )}
      </div>
    ) : asChild ? null : (
      <div className="flex min-w-0 flex-1 items-center gap-3">{children}</div>
    );

  return (
    <Comp
      data-slot="list-row"
      data-active={active || undefined}
      className={cn(
        "flex min-h-10 w-full min-w-0 items-center gap-3 px-4 py-1.5 text-left",
        isInteractive &&
          "cursor-pointer outline-none transition-colors hover:bg-muted/70 focus-visible:bg-muted/70 focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-inset",
        active && "bg-muted",
        className,
      )}
      {...props}
    >
      {asChild && <Slottable>{children}</Slottable>}
      {leading !== undefined && <div className="flex shrink-0 items-center">{leading}</div>}
      {body}
      {meta !== undefined && (
        <div className="shrink-0 text-small text-muted-foreground tabular-nums">{meta}</div>
      )}
      {trailing !== undefined && (
        <div className="flex shrink-0 items-center gap-1.5">{trailing}</div>
      )}
    </Comp>
  );
}
