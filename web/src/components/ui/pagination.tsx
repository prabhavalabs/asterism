import type * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import {
  ArrowLeft01Icon,
  ArrowLeftDoubleIcon,
  ArrowRight01Icon,
  ArrowRightDoubleIcon,
  MoreHorizontalIcon,
} from "@hugeicons/core-free-icons";
import { buttonVariants } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { cn } from "@/lib/utils";

/**
 * Pagination primitives after shadcn's, in the lyra look: every control is
 * --control-h-sm (28px) with rounded-button-sm corners, ghost by default and
 * outlined for the current page. Items render buttons (the page lives in the
 * URL through `usePagedSearch`, so a click updates the search params); pass
 * `asChild` to render a router Link instead. Compose with
 * `DataPagination` (components/common) for the full table footer.
 */
function Pagination({ className, ...props }: React.ComponentProps<"nav">) {
  return (
    <nav
      aria-label="Pagination"
      data-slot="pagination"
      className={cn("flex min-w-0", className)}
      {...props}
    />
  );
}

function PaginationContent({ className, ...props }: React.ComponentProps<"ul">) {
  return <ul data-slot="pagination-content" className={cn("flex items-center gap-0.5", className)} {...props} />;
}

function PaginationItem(props: React.ComponentProps<"li">) {
  return <li data-slot="pagination-item" {...props} />;
}

export interface PaginationLinkProps extends React.ComponentProps<"button"> {
  isActive?: boolean;
  asChild?: boolean;
}

/** A numbered page: 28px tall, at least 28px wide, tabular digits. */
function PaginationLink({ className, isActive = false, asChild = false, type, ...props }: PaginationLinkProps) {
  const Comp = asChild ? Slot : "button";
  return (
    <Comp
      data-slot="pagination-link"
      data-active={isActive}
      aria-current={isActive ? "page" : undefined}
      type={asChild ? undefined : (type ?? "button")}
      className={cn(
        buttonVariants({ variant: isActive ? "outline" : "ghost", size: "sm" }),
        "min-w-control-sm justify-center px-1.5 tabular-nums",
        isActive && "pointer-events-none text-foreground",
        className,
      )}
      {...props}
    />
  );
}

/** Square 28px icon step control (first / previous / next / last). */
function PaginationStep({
  className,
  icon,
  label,
  asChild = false,
  type,
  ...props
}: React.ComponentProps<"button"> & { icon: Parameters<typeof Icon>[0]["icon"]; label: string; asChild?: boolean }) {
  const Comp = asChild ? Slot : "button";
  return (
    <Comp
      data-slot="pagination-step"
      aria-label={label}
      title={label}
      type={asChild ? undefined : (type ?? "button")}
      className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }), className)}
      {...props}
    >
      <Icon icon={icon} />
    </Comp>
  );
}

type StepProps = Omit<React.ComponentProps<typeof PaginationStep>, "icon" | "label"> & { label?: string };

function PaginationFirst({ label = "First page", ...props }: StepProps) {
  return <PaginationStep data-slot="pagination-first" icon={ArrowLeftDoubleIcon} label={label} {...props} />;
}

function PaginationPrevious({ label = "Previous page", ...props }: StepProps) {
  return <PaginationStep data-slot="pagination-previous" icon={ArrowLeft01Icon} label={label} {...props} />;
}

function PaginationNext({ label = "Next page", ...props }: StepProps) {
  return <PaginationStep data-slot="pagination-next" icon={ArrowRight01Icon} label={label} {...props} />;
}

function PaginationLast({ label = "Last page", ...props }: StepProps) {
  return <PaginationStep data-slot="pagination-last" icon={ArrowRightDoubleIcon} label={label} {...props} />;
}

/** Gap marker between page numbers; same 28px box so the row never shifts. */
function PaginationEllipsis({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      aria-hidden="true"
      data-slot="pagination-ellipsis"
      className={cn("flex size-control-sm items-center justify-center text-text-4 [&_svg]:size-3.5", className)}
      {...props}
    >
      <Icon icon={MoreHorizontalIcon} />
    </span>
  );
}

export {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationFirst,
  PaginationItem,
  PaginationLast,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
};
