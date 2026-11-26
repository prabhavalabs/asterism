import type * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/**
 * Restrained buttons (lyra), a touch rounder than cards (rounded-button 10px, sm 9px). Heights come from the control tokens so
 * a button always matches the inputs, tabs and segmented controls in its row:
 * `default` / `icon` = --control-h (32px), `sm` / `icon-sm` = --control-h-sm (28px).
 * Focus is a 1px ring-coloured border plus a faint 1px halo.
 */
const buttonVariants = cva(
  [
    "group/button inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-button border border-transparent bg-clip-padding font-medium text-control outline-none select-none",
    "transition-[color,background-color,border-color,box-shadow,transform] duration-press ease-out",
    "active:not-aria-[haspopup]:translate-y-px motion-reduce:active:not-aria-[haspopup]:translate-y-0",
    "focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50",
    "disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50",
    "aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/20",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ],
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/85",
        outline:
          "border-border bg-card text-foreground hover:bg-muted aria-expanded:bg-muted data-[state=open]:bg-muted dark:border-input dark:bg-input/30 dark:hover:bg-input/50 dark:aria-expanded:bg-input/50",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-accent aria-expanded:bg-accent data-[state=open]:bg-accent",
        ghost:
          "text-text-2 hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground dark:hover:bg-muted/50 dark:aria-expanded:bg-muted/50",
        destructive:
          "bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30",
        link: "h-auto! border-0 px-0! text-primary underline-offset-4 hover:underline",
      },
      size: {
        default:
          "h-control px-2.5 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2",
        sm: "h-control-sm gap-1 rounded-button-sm px-2.5 text-small has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3.5",
        icon: "size-control",
        "icon-sm": "size-control-sm rounded-button-sm [&_svg:not([class*='size-'])]:size-3.5",
      },
    },
    compoundVariants: [
      {
        variant: "ghost",
        size: ["icon", "icon-sm"],
        className: "text-muted-foreground",
      },
    ],
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export type ButtonVariant = NonNullable<VariantProps<typeof buttonVariants>["variant"]>;
export type ButtonSize = NonNullable<VariantProps<typeof buttonVariants>["size"]>;

export interface ButtonProps
  extends React.ComponentProps<"button">,
    VariantProps<typeof buttonVariants> {
  /** Render the child element (for example a router Link) with button styles. */
  asChild?: boolean;
}

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  type,
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      type={asChild ? type : (type ?? "button")}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export { Button, buttonVariants };
