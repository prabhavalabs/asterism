import type * as React from "react";
import { Link } from "@tanstack/react-router";
import { ArrowLeft01Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { cn } from "@/lib/utils";

export interface PageProps extends React.ComponentProps<"div"> {
  /** `default` caps the column at 1440px; `full` is for canvas/topology pages. */
  width?: "default" | "full";
}

/** The content column every page sits in: px-4 md:px-6, pt-5 pb-10, sections gap-5. */
export function Page({ width = "default", className, ...props }: PageProps) {
  return (
    <div
      data-slot="page"
      className={cn(
        "flex w-full min-w-0 flex-col gap-5 px-4 pt-5 pb-10 md:px-6",
        width === "default" && "mx-auto max-w-[1440px]",
        className,
      )}
      {...props}
    />
  );
}

export interface PageHeaderBackLink {
  /** Concrete path, for example "/projects". */
  to: string;
  label: string;
}

export interface PageHeaderProps extends Omit<React.ComponentProps<"div">, "title"> {
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Small back link above the title. */
  back?: PageHeaderBackLink;
  /** Pills next to the title (StatusPill, Badge). */
  meta?: React.ReactNode;
  /** Page actions on the right; wrap below the title on phones. Use default-size controls. */
  actions?: React.ReactNode;
}

/** Page title (22px), one line of context, optional back link, actions on the right. */
export function PageHeader({
  title,
  description,
  back,
  meta,
  actions,
  className,
  ...props
}: PageHeaderProps) {
  return (
    <div
      data-slot="page-header"
      className={cn("flex flex-col gap-3 sm:flex-row sm:items-end", className)}
      {...props}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        {back && (
          <Link
            to={back.to}
            className="-ml-1 mb-0.5 inline-flex h-control-sm w-fit items-center gap-1 rounded-button-sm pr-2 pl-1 text-small font-medium text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
          >
            <Icon icon={ArrowLeft01Icon} className="size-4" />
            {back.label}
          </Link>
        )}
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5">
          <h1 className="min-w-0 font-heading text-title break-words text-foreground">{title}</h1>
          {meta && <div className="flex flex-wrap items-center gap-1.5">{meta}</div>}
        </div>
        {description && (
          <div className="max-w-3xl text-control text-muted-foreground text-pretty">{description}</div>
        )}
      </div>
      {actions && (
        <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>
      )}
    </div>
  );
}
