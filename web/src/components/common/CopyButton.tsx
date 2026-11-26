import { useEffect, useRef, useState } from "react";
import { Copy01Icon, Tick02Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

export interface CopyButtonProps {
  value: string;
  /** Accessible name and tooltip, e.g. "Copy install command". */
  label: string;
  iconOnly?: boolean;
  className?: string;
}

/**
 * Small ghost button that copies `value` and shows a tick for 1.5 s. The
 * clipboard can be blocked (insecure origin); the text stays selectable.
 */
export function CopyButton({ value, label, iconOnly = false, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = () => {
    void navigator.clipboard
      ?.writeText(value)
      .then(() => {
        setCopied(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {
        // Clipboard blocked: the value stays selectable.
      });
  };

  return (
    <Button
      variant="ghost"
      size={iconOnly ? "icon-sm" : "sm"}
      onClick={copy}
      aria-label={iconOnly ? label : undefined}
      title={label}
      className={className}
    >
      <Icon icon={copied ? Tick02Icon : Copy01Icon} />
      {!iconOnly && (copied ? "Copied" : "Copy")}
      <span className="sr-only" aria-live="polite">
        {copied ? "Copied" : ""}
      </span>
    </Button>
  );
}

/** An inline code value with an icon copy button, for PropertyRow values. */
export function CopyValue({ value, label }: { value: string; label?: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <code className="min-w-0 truncate rounded-chip bg-surface-subtle px-2 py-0.5 font-mono text-mono text-foreground ring-1 ring-inset ring-border">
        {value}
      </code>
      <CopyButton value={value} label={label ?? `Copy ${value}`} iconOnly />
    </span>
  );
}

export interface CodeBlockProps {
  value: string;
  /** Names the scroll region and the copy button ("Copy <label>"). */
  label: string;
  className?: string;
}

/**
 * A pre block (rounded-tile, mono 12.5px) that scrolls sideways in a
 * ScrollArea, with a copy button pinned to its top-right corner.
 */
export function CodeBlock({ value, label, className }: CodeBlockProps) {
  return (
    <div className={cn("relative min-w-0", className)}>
      <ScrollArea
        orientation="horizontal"
        className="rounded-tile bg-surface-subtle ring-1 ring-inset ring-border"
        viewportProps={{ tabIndex: 0, "aria-label": label }}
      >
        <pre className="py-3 pr-12 pl-4 font-mono text-mono leading-6 text-foreground">{value}</pre>
      </ScrollArea>
      <CopyButton value={value} label={`Copy ${label.toLowerCase()}`} iconOnly className="absolute top-1.5 right-1.5 bg-surface-subtle" />
    </div>
  );
}
