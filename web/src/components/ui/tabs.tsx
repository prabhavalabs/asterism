import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cva, type VariantProps } from "class-variance-authority";
import { useActiveIndicator } from "@/hooks/use-active-indicator";
import { cn } from "@/lib/utils";

function Tabs({
  className,
  orientation = "horizontal",
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return (
    <TabsPrimitive.Root
      data-slot="tabs"
      data-orientation={orientation}
      orientation={orientation}
      className={cn("group/tabs flex gap-3 data-horizontal:flex-col", className)}
      {...props}
    />
  );
}

/**
 * The list matches the control height (32px, or 28px with `size="sm"`) so it
 * lines up with buttons and inputs. `line` draws an underline instead of the
 * sliding chip.
 */
const tabsListVariants = cva(
  "group/tabs-list relative inline-flex w-fit max-w-full items-center justify-start text-muted-foreground group-data-vertical/tabs:h-fit group-data-vertical/tabs:flex-col",
  {
    variants: {
      variant: {
        default: "rounded-control bg-muted p-[3px]",
        line: "gap-1",
      },
      size: {
        default: "group-data-horizontal/tabs:h-control",
        sm: "group-data-horizontal/tabs:h-control-sm",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface TabsListProps
  extends React.ComponentProps<typeof TabsPrimitive.List>,
    VariantProps<typeof tabsListVariants> {}

function TabsList({ className, variant = "default", size = "default", children, ref, ...props }: TabsListProps) {
  const listRef = React.useRef<HTMLDivElement>(null);
  const indicatorRef = React.useRef<HTMLSpanElement>(null);
  const withIndicator = variant !== "line";
  useActiveIndicator(
    listRef,
    indicatorRef,
    '[role="tab"][data-state="active"]',
    "data-state",
    withIndicator,
  );

  const setRefs = React.useCallback(
    (node: HTMLDivElement | null) => {
      listRef.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  return (
    <TabsPrimitive.List
      ref={setRefs}
      data-slot="tabs-list"
      data-variant={variant}
      data-size={size}
      className={cn(tabsListVariants({ variant, size }), className)}
      {...props}
    >
      {withIndicator && (
        <span
          ref={indicatorRef}
          aria-hidden="true"
          data-slot="tabs-indicator"
          className="pointer-events-none absolute top-0 left-0 rounded-control-item bg-card opacity-0 ring-1 ring-border transition-[transform,width,height] duration-tabs ease-out data-ready:opacity-100 motion-reduce:transition-none dark:bg-input/30 dark:ring-input"
        />
      )}
      {children}
    </TabsPrimitive.List>
  );
}

function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        "relative inline-flex h-full shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control-item px-2.5 font-medium text-control text-muted-foreground outline-none transition-colors duration-tabs ease-out",
        "group-data-[size=sm]/tabs-list:px-2 group-data-[size=sm]/tabs-list:text-small",
        "hover:text-foreground data-active:text-foreground focus-visible:ring-1 focus-visible:ring-ring",
        "disabled:pointer-events-none disabled:opacity-50 group-data-vertical/tabs:w-full group-data-vertical/tabs:justify-start group-data-vertical/tabs:py-1.5",
        "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        "after:absolute after:bg-foreground after:opacity-0 after:transition-opacity after:duration-tabs group-data-horizontal/tabs:after:inset-x-1 group-data-horizontal/tabs:after:-bottom-1 group-data-horizontal/tabs:after:h-0.5 group-data-vertical/tabs:after:inset-y-0 group-data-vertical/tabs:after:-right-1 group-data-vertical/tabs:after:w-0.5 group-data-[variant=line]/tabs-list:data-active:after:opacity-100",
        className,
      )}
      {...props}
    />
  );
}

function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("flex-1 text-body outline-none", className)}
      {...props}
    />
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export { Tabs, TabsContent, TabsList, TabsTrigger, tabsListVariants };
