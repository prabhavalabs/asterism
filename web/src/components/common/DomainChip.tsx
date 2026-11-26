import type * as React from "react";
import { Globe02Icon, SquareLock02Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { cn } from "@/lib/utils";

export interface DomainChipProps extends Omit<React.ComponentProps<"span">, "children"> {
  domain: string;
  /** Shows a lock when the route terminates TLS. */
  tls?: boolean;
  /** Makes the chip an external link (opens in a new tab). */
  href?: string;
}

/** Domain chip (22px, 12px mono), with a TLS lock. Truncates long names. */
export function DomainChip({ domain, tls = false, href, className, ...props }: DomainChipProps) {
  const classes = cn(
    "inline-flex h-pill min-w-0 max-w-full shrink items-center gap-1 rounded-chip bg-secondary px-1.5 font-mono text-pill text-text-2 ring-1 ring-inset ring-border",
    href &&
      "relative outline-none transition-colors after:absolute after:-inset-y-[5px] after:inset-x-0 hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring",
    className,
  );
  const content = (
    <>
      {tls ? (
        <Icon icon={SquareLock02Icon} className="size-3 shrink-0 text-status-healthy" />
      ) : (
        <Icon icon={Globe02Icon} className="size-3 shrink-0 text-text-4" />
      )}
      <span className="sr-only">{tls ? "TLS, " : "No TLS, "}</span>
      <span className="truncate">{domain}</span>
    </>
  );

  if (href) {
    return (
      <a
        data-slot="domain-chip"
        href={href}
        target="_blank"
        rel="noreferrer noopener"
        title={domain}
        className={classes}
      >
        {content}
      </a>
    );
  }

  return (
    <span data-slot="domain-chip" title={domain} className={classes} {...props}>
      {content}
    </span>
  );
}
