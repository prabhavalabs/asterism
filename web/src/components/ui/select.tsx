import type * as React from "react";
import * as SelectPrimitive from "@radix-ui/react-select";
import { ArrowDown01Icon, ArrowUp01Icon, Tick02Icon, UnfoldMoreIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { surfaceMotion } from "@/lib/motion";
import { cn } from "@/lib/utils";

/**
 * Select (Radix) in the lyra look. The trigger matches Input: --control-h
 * (32px), 13px text, rounded-control, 1px input border; `size="sm"` is
 * --control-h-sm (28px) with 12.5px text for rows where every control is sm.
 * The menu matches DropdownMenu: rounded-card popover, 6px items at
 * control height, tick on the selected item.
 *
 * Long option lists scroll inside Radix's own select viewport (it keeps the
 * selected item aligned and adds hover scroll buttons), so it is the one
 * list that does not use ScrollArea.
 */
function Select(props: React.ComponentProps<typeof SelectPrimitive.Root>) {
  return <SelectPrimitive.Root data-slot="select" {...props} />;
}

function SelectGroup(props: React.ComponentProps<typeof SelectPrimitive.Group>) {
  return <SelectPrimitive.Group data-slot="select-group" {...props} />;
}

function SelectValue(props: React.ComponentProps<typeof SelectPrimitive.Value>) {
  return <SelectPrimitive.Value data-slot="select-value" {...props} />;
}

function SelectTrigger({
  className,
  size = "default",
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Trigger> & { size?: "default" | "sm" }) {
  return (
    <SelectPrimitive.Trigger
      data-slot="select-trigger"
      data-size={size}
      className={cn(
        "flex w-fit shrink-0 items-center justify-between gap-1.5 rounded-control border border-input bg-card whitespace-nowrap text-foreground outline-none transition-[color,border-color,box-shadow] select-none dark:bg-input/30",
        "hover:bg-muted dark:hover:bg-input/50 data-[state=open]:bg-muted dark:data-[state=open]:bg-input/50",
        "focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50",
        "aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/20",
        "disabled:pointer-events-none disabled:opacity-50 data-placeholder:text-text-4",
        "data-[size=default]:h-control data-[size=default]:pr-2 data-[size=default]:pl-2.5 data-[size=default]:text-control",
        "data-[size=sm]:h-control-sm data-[size=sm]:pr-1.5 data-[size=sm]:pl-2 data-[size=sm]:text-small",
        "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:text-text-4 data-[size=default]:[&_svg]:size-4 data-[size=sm]:[&_svg]:size-3.5",
        "*:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-1.5 *:data-[slot=select-value]:tabular-nums",
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <Icon icon={UnfoldMoreIcon} />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  );
}

function SelectContent({
  className,
  children,
  position = "popper",
  align = "start",
  sideOffset = 4,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        data-slot="select-content"
        position={position}
        align={align}
        sideOffset={position === "popper" ? sideOffset : undefined}
        className={cn(
          "relative z-50 max-h-(--radix-select-content-available-height) min-w-32 overflow-hidden rounded-card bg-popover text-popover-foreground shadow-pop outline-none origin-(--radix-select-content-transform-origin)",
          position === "popper" && "w-full min-w-(--radix-select-trigger-width)",
          surfaceMotion,
          className,
        )}
        {...props}
      >
        <SelectScrollUpButton />
        <SelectPrimitive.Viewport data-slot="select-viewport" className="p-1">
          {children}
        </SelectPrimitive.Viewport>
        <SelectScrollDownButton />
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  );
}

function SelectLabel({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.Label>) {
  return (
    <SelectPrimitive.Label
      data-slot="select-label"
      className={cn("flex h-control-sm items-center px-2.5 text-small text-muted-foreground", className)}
      {...props}
    />
  );
}

function SelectItem({ className, children, ...props }: React.ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        "relative flex h-control w-full cursor-default items-center gap-2 rounded-menu-item pr-8 pl-2.5 text-control text-text-2 outline-hidden select-none",
        "data-highlighted:bg-accent data-highlighted:text-accent-foreground data-[state=checked]:text-foreground",
        "data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <span className="absolute right-2 flex size-4 items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Icon icon={Tick02Icon} className="text-primary" />
        </SelectPrimitive.ItemIndicator>
      </span>
    </SelectPrimitive.Item>
  );
}

function SelectSeparator({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.Separator>) {
  return (
    <SelectPrimitive.Separator
      data-slot="select-separator"
      className={cn("pointer-events-none -mx-1 my-1 h-px bg-divider", className)}
      {...props}
    />
  );
}

function SelectScrollUpButton({ className, ...props }: React.ComponentProps<typeof SelectPrimitive.ScrollUpButton>) {
  return (
    <SelectPrimitive.ScrollUpButton
      data-slot="select-scroll-up-button"
      className={cn("flex h-6 cursor-default items-center justify-center text-text-4 [&_svg]:size-3.5", className)}
      {...props}
    >
      <Icon icon={ArrowUp01Icon} />
    </SelectPrimitive.ScrollUpButton>
  );
}

function SelectScrollDownButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollDownButton>) {
  return (
    <SelectPrimitive.ScrollDownButton
      data-slot="select-scroll-down-button"
      className={cn("flex h-6 cursor-default items-center justify-center text-text-4 [&_svg]:size-3.5", className)}
      {...props}
    >
      <Icon icon={ArrowDown01Icon} />
    </SelectPrimitive.ScrollDownButton>
  );
}

export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
};
