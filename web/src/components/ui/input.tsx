import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Square text input at --control-h (32px), 13px text, 1px input border. On touch screens the text
 * grows to 16px so iOS does not zoom the page on focus.
 */
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "h-control w-full min-w-0 rounded-control border border-input bg-card px-2.5 text-control text-foreground outline-none transition-[color,border-color,box-shadow] dark:bg-input/30",
        "[@media(pointer:coarse)]:text-[16px]",
        "placeholder:text-text-4 selection:bg-primary selection:text-primary-foreground",
        "file:inline-flex file:h-6 file:border-0 file:bg-transparent file:font-medium file:text-control file:text-foreground",
        "focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50",
        "aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/20",
        "disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 dark:disabled:bg-input/80",
        "[&::-webkit-search-cancel-button]:cursor-pointer",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
