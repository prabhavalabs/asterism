import * as React from "react";
import { Icon, type IconData } from "@/components/ui/icon";
import { Card, CardAction, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export interface SectionCardProps extends Omit<React.ComponentProps<"section">, "title"> {
  title: React.ReactNode;
  description?: React.ReactNode;
  icon?: IconData;
  /** Header actions on the right. Use sm controls (Button size="sm", TabsList size="sm"). */
  actions?: React.ReactNode;
  footer?: React.ReactNode;
  /** Remove body padding (lists, tables, charts that bleed to the edges). */
  flush?: boolean;
  bodyClassName?: string;
  headerClassName?: string;
}

/**
 * Titled card: 44px header (title + description left, sm actions right) with
 * a divider, and a 16px-padded body. Labelled by its title for screen readers.
 */
export function SectionCard({
  title,
  description,
  icon,
  actions,
  footer,
  flush = false,
  className,
  bodyClassName,
  headerClassName,
  children,
  ...props
}: SectionCardProps) {
  const titleId = React.useId();

  return (
    <Card asChild className={className}>
      <section aria-labelledby={titleId} {...props}>
        <CardHeader className={headerClassName}>
          <div className="flex min-w-0 items-center gap-2">
            {icon && <Icon icon={icon} className="size-4 shrink-0 text-text-4" />}
            <CardTitle id={titleId}>{title}</CardTitle>
          </div>
          {description && <CardDescription className="truncate">{description}</CardDescription>}
          {actions && <CardAction>{actions}</CardAction>}
        </CardHeader>
        <div
          data-slot="section-card-body"
          className={cn("min-w-0 flex-1", !flush && "p-4", bodyClassName)}
        >
          {children}
        </div>
        {footer && <CardFooter>{footer}</CardFooter>}
      </section>
    </Card>
  );
}
