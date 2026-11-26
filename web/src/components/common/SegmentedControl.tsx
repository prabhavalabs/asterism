import * as React from "react";
import { Icon, type IconData } from "@/components/ui/icon";
import { useActiveIndicator } from "@/hooks/use-active-indicator";
import { cn } from "@/lib/utils";

export interface SegmentedOption<T extends string> {
  value: T;
  label: React.ReactNode;
  /** Optional count chip after the label (tabular). */
  count?: number;
  icon?: IconData;
  disabled?: boolean;
  /** Accessible name when `label` is not plain text (icon-only options). */
  ariaLabel?: string;
}

export interface SegmentedControlProps<T extends string>
  extends Omit<React.ComponentProps<"div">, "onChange" | "defaultValue" | "children"> {
  value: T;
  onValueChange: (value: T) => void;
  options: readonly SegmentedOption<T>[];
  /** `default` = --control-h (32px, 13px text); `sm` = --control-h-sm (28px, 12.5px). */
  size?: "default" | "sm";
  /** Stretch to the container width with equal segments. */
  fullWidth?: boolean;
  /** Required: names the radio group. */
  "aria-label": string;
}

/**
 * Single-choice segmented control (radiogroup). Arrow keys, Home and End move
 * and select; only the checked segment is in the tab order. The selection
 * chip slides between segments (jumps for keyboard and reduced motion).
 */
export function SegmentedControl<T extends string>({
  value,
  onValueChange,
  options,
  size = "default",
  fullWidth = false,
  className,
  ...props
}: SegmentedControlProps<T>) {
  const listRef = React.useRef<HTMLDivElement>(null);
  const indicatorRef = React.useRef<HTMLSpanElement>(null);
  useActiveIndicator(listRef, indicatorRef, '[role="radio"][aria-checked="true"]', "aria-checked");

  const enabled = options.filter((option) => !option.disabled);

  const move = (from: T, delta: number | "first" | "last") => {
    if (enabled.length === 0) return;
    let next: SegmentedOption<T>;
    if (delta === "first") next = enabled[0];
    else if (delta === "last") next = enabled[enabled.length - 1];
    else {
      const index = enabled.findIndex((option) => option.value === from);
      next = enabled[(index + delta + enabled.length) % enabled.length];
    }
    onValueChange(next.value);
    listRef.current
      ?.querySelector<HTMLButtonElement>(`[role="radio"][data-value="${CSS.escape(next.value)}"]`)
      ?.focus();
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, current: T) => {
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        event.preventDefault();
        move(current, 1);
        break;
      case "ArrowLeft":
      case "ArrowUp":
        event.preventDefault();
        move(current, -1);
        break;
      case "Home":
        event.preventDefault();
        move(current, "first");
        break;
      case "End":
        event.preventDefault();
        move(current, "last");
        break;
    }
  };

  const hasChecked = options.some((option) => option.value === value && !option.disabled);

  return (
    <div
      ref={listRef}
      role="radiogroup"
      data-slot="segmented-control"
      data-size={size}
      className={cn(
        "relative inline-flex max-w-full shrink-0 items-center rounded-control bg-muted p-[3px]",
        size === "default" ? "h-control" : "h-control-sm",
        fullWidth ? "flex w-full" : "w-fit",
        className,
      )}
      {...props}
    >
      <span
        ref={indicatorRef}
        aria-hidden="true"
        className="pointer-events-none absolute top-0 left-0 rounded-control-item bg-card opacity-0 ring-1 ring-border transition-[transform,width,height] duration-tabs ease-out data-ready:opacity-100 motion-reduce:transition-none dark:bg-input/30 dark:ring-input"
      />
      {options.map((option, index) => {
        const checked = option.value === value;
        const icon = option.icon;
        const focusable = checked || (!hasChecked && index === 0);
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={option.ariaLabel}
            data-value={option.value}
            disabled={option.disabled}
            tabIndex={focusable ? 0 : -1}
            onClick={() => onValueChange(option.value)}
            onKeyDown={(event) => onKeyDown(event, option.value)}
            className={cn(
              "relative inline-flex h-full min-w-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control-item font-medium text-muted-foreground outline-none transition-colors duration-tabs ease-out",
              size === "default" ? "px-2.5 text-control" : "px-2 text-small",
              "hover:text-foreground aria-checked:text-foreground focus-visible:ring-1 focus-visible:ring-ring",
              "disabled:pointer-events-none disabled:opacity-50",
              fullWidth && "flex-1",
              "[&_svg]:pointer-events-none [&_svg]:shrink-0",
              size === "default" ? "[&_svg]:size-4" : "[&_svg]:size-3.5",
            )}
          >
            {icon && <Icon icon={icon} />}
            <span className="truncate">{option.label}</span>
            {option.count !== undefined && (
              <span className="inline-flex h-5 min-w-5 items-center justify-center rounded-chip bg-foreground/6 px-1.5 font-mono text-pill tabular-nums text-text-2">
                {option.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
