import type * as React from "react";
import { cn } from "@/lib/utils";

export interface PropertyRowProps extends Omit<React.ComponentProps<"div">, "children"> {
  /** 13px muted label on the left. */
  label: React.ReactNode;
  /** Optional 12.5px line under the label. */
  hint?: React.ReactNode;
  /** Value or control on the right (text, a pill, a sm Button, a sm SegmentedControl). */
  children?: React.ReactNode;
  /** Monospaced, tabular value text. */
  mono?: boolean;
}

/**
 * Label/value row, min 40px, px-4 so it lines up with SectionCard content.
 * Stack rows in a `divide-y divide-divider` container (SectionCard `flush`).
 * The value wraps under the label on narrow screens.
 */
export function PropertyRow({ label, hint, mono = false, className, children, ...props }: PropertyRowProps) {
  return (
    <div
      data-slot="property-row"
      className={cn(
        "flex min-h-10 flex-wrap items-center justify-between gap-x-6 gap-y-1 px-4 py-2",
        className,
      )}
      {...props}
    >
      <div className="flex min-w-0 flex-col">
        <span className="text-control text-muted-foreground">{label}</span>
        {hint && <span className="text-small text-text-4">{hint}</span>}
      </div>
      <div
        className={cn(
          "flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2 text-right text-body text-foreground",
          mono && "font-mono text-mono tabular-nums break-all",
        )}
      >
        {children}
      </div>
    </div>
  );
}
