import type * as React from "react";
import { Search01Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export interface SearchInputProps extends Omit<React.ComponentProps<"input">, "type" | "size"> {
  /** Called with the new string on every change (in addition to `onChange`). */
  onValueChange?: (value: string) => void;
  /** `sm` matches --control-h-sm (28px). */
  size?: "default" | "sm";
  /** Classes for the wrapper (width). Default `w-full sm:w-72`. */
  containerClassName?: string;
}

/** Search field: full width on phones, 288px from `sm`, 16px icon. */
export function SearchInput({
  onValueChange,
  onChange,
  size = "default",
  containerClassName,
  className,
  placeholder = "Search…",
  "aria-label": ariaLabel,
  ...props
}: SearchInputProps) {
  return (
    <div data-slot="search-input" className={cn("relative w-full sm:w-72", containerClassName)}>
      <Icon icon={Search01Icon}
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-text-4"
      />
      <Input
        type="search"
        placeholder={placeholder}
        aria-label={ariaLabel ?? placeholder}
        className={cn("pl-8", size === "sm" && "h-control-sm text-small", className)}
        onChange={(event) => {
          onChange?.(event);
          onValueChange?.(event.target.value);
        }}
        {...props}
      />
    </div>
  );
}
