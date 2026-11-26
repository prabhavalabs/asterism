import type * as React from "react";
import { Alert02Icon, ArrowReloadHorizontalIcon, InboxIcon, Loading03Icon } from "@hugeicons/core-free-icons";
import { Icon, type IconData } from "@/components/ui/icon";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { errorMessage } from "./errors";

type StateSize = "default" | "compact";

const frame = (size: StateSize) =>
  cn(
    "flex flex-col items-center justify-center text-center",
    size === "default" ? "gap-3 px-6 py-14" : "gap-2.5 px-4 py-8",
  );

export interface EmptyStateProps extends Omit<React.ComponentProps<"div">, "title"> {
  icon?: IconData;
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Next step: an outline Button, a Link. */
  action?: React.ReactNode;
  /** `compact` fits inside a card or a filtered list. */
  size?: StateSize;
}

/** A centred explanation for a place with nothing in it yet, with the next step. */
export function EmptyState({
  icon = InboxIcon,
  title,
  description,
  action,
  size = "default",
  className,
  ...props
}: EmptyStateProps) {
  return (
    <div data-slot="empty-state" className={cn(frame(size), className)} {...props}>
      <span className="flex size-9 items-center justify-center rounded-avatar bg-card text-muted-foreground shadow-card">
        <Icon icon={icon} className="size-4" />
      </span>
      <div className="flex max-w-sm flex-col gap-1">
        <p className="font-heading text-heading text-foreground">{title}</p>
        {description && (
          <p className="text-control text-muted-foreground text-pretty">{description}</p>
        )}
      </div>
      {action && <div className="mt-1 flex flex-wrap justify-center gap-2">{action}</div>}
    </div>
  );
}

export interface ErrorStateProps extends Omit<React.ComponentProps<"div">, "title"> {
  title?: React.ReactNode;
  /** The error; its message is shown under the title. */
  error?: unknown;
  /** Overrides the message derived from `error`. */
  message?: React.ReactNode;
  onRetry?: () => void;
  retrying?: boolean;
  action?: React.ReactNode;
  size?: StateSize;
}

/** What failed, in words, and a way to try again. Announced as an alert. */
export function ErrorState({
  title = "Couldn't load this",
  error,
  message,
  onRetry,
  retrying = false,
  action,
  size = "default",
  className,
  ...props
}: ErrorStateProps) {
  return (
    <div role="alert" data-slot="error-state" className={cn(frame(size), className)} {...props}>
      <span className="flex size-9 items-center justify-center rounded-avatar bg-status-critical-surface text-status-critical">
        <Icon icon={Alert02Icon} className="size-4" />
      </span>
      <div className="flex max-w-sm flex-col gap-1">
        <p className="font-heading text-heading text-foreground">{title}</p>
        <p className="text-control text-muted-foreground text-pretty break-words">
          {message ?? errorMessage(error)}
        </p>
      </div>
      {(onRetry || action) && (
        <div className="mt-1 flex flex-wrap justify-center gap-2">
          {onRetry && (
            <Button variant="outline" onClick={onRetry} disabled={retrying}>
              <Icon icon={ArrowReloadHorizontalIcon} className={cn(retrying && "animate-spin")} />
              {retrying ? "Retrying…" : "Try again"}
            </Button>
          )}
          {action}
        </div>
      )}
    </div>
  );
}

export interface LoadingStateProps extends React.ComponentProps<"div"> {
  /** Announced to screen readers; shown for `block` and `inline`. */
  label?: string;
  /**
   * `block`: centred spinner + label (default). `inline`: spinner + label in a row.
   * `rows`: skeleton list rows (40px). `cards`: skeleton StatCard grid.
   */
  variant?: "block" | "inline" | "rows" | "cards";
  /** Number of skeleton rows or cards. */
  count?: number;
  size?: StateSize;
}

export function LoadingState({
  label = "Loading…",
  variant = "block",
  count,
  size = "default",
  className,
  ...props
}: LoadingStateProps) {
  if (variant === "rows") {
    const rows = count ?? 4;
    return (
      <div role="status" aria-live="polite" className={cn("divide-y divide-divider", className)} {...props}>
        <span className="sr-only">{label}</span>
        {Array.from({ length: rows }, (_, index) => (
          <div key={index} className="flex min-h-10 items-center gap-3 px-4 py-1.5">
            <Skeleton className="size-4" />
            <Skeleton className="h-3.5 flex-1" style={{ maxWidth: `${70 - ((index * 13) % 30)}%` }} />
            <Skeleton className="h-pill w-14" />
          </div>
        ))}
      </div>
    );
  }

  if (variant === "cards") {
    const cards = count ?? 4;
    return (
      <div
        role="status"
        aria-live="polite"
        className={cn("grid grid-cols-2 gap-3 lg:grid-cols-4", className)}
        {...props}
      >
        <span className="sr-only">{label}</span>
        {Array.from({ length: cards }, (_, index) => (
          <div key={index} className="flex flex-col gap-3 rounded-card bg-card px-4 py-3.5 shadow-card">
            <Skeleton className="h-3.5 w-24" />
            <Skeleton className="h-7 w-28" />
            <Skeleton className="h-3 w-32" />
          </div>
        ))}
      </div>
    );
  }

  if (variant === "inline") {
    return (
      <div
        role="status"
        aria-live="polite"
        className={cn("inline-flex items-center gap-2 text-small text-muted-foreground", className)}
        {...props}
      >
        <Icon icon={Loading03Icon} className="size-4 animate-spin" />
        {label}
      </div>
    );
  }

  return (
    <div role="status" aria-live="polite" className={cn(frame(size), className)} {...props}>
      <Icon icon={Loading03Icon} className="size-5 animate-spin text-muted-foreground" />
      <p className="text-control text-muted-foreground">{label}</p>
    </div>
  );
}
