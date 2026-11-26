import type * as React from "react";
import * as ScrollAreaPrimitive from "@radix-ui/react-scroll-area";
import { cn } from "@/lib/utils";

export interface ScrollAreaProps extends React.ComponentProps<typeof ScrollAreaPrimitive.Root> {
  /**
   * Which scrollbars to draw. Content only scrolls along the axes listed
   * here; the other axis is clipped (`overflow: hidden`), so a vertical area
   * can never grow a horizontal scrollbar.
   */
  orientation?: "vertical" | "horizontal" | "both";
  /** Classes for the scrolling viewport. Put max-height and padding here. */
  viewportClassName?: string;
  viewportRef?: React.Ref<HTMLDivElement>;
  /** Extra props for the viewport element (for example an aria-label or tabIndex). */
  viewportProps?: Omit<React.ComponentProps<typeof ScrollAreaPrimitive.Viewport>, "ref" | "className" | "children">;
}

/**
 * The only way to make content scroll (owner rule): a Radix scroll area with
 * thin overlay scrollbars that appear while the pointer is over the area or
 * while it scrolls. Native scrolling, wheel, touch and keyboard all still
 * work; only the scrollbar is drawn by us.
 *
 * - Bound the size on the viewport (`viewportClassName="max-h-96"`) or give
 *   the root a definite height (`className="min-h-0 flex-1"` in a flex column).
 * - Sticky table headers work inside: the viewport is the scroll container.
 * - `orientation="horizontal"` for chip rows and code blocks, `"both"` for
 *   wide tables.
 */
function ScrollArea({
  className,
  children,
  orientation = "vertical",
  type = "hover",
  viewportClassName,
  viewportRef,
  viewportProps,
  style,
  ...props
}: ScrollAreaProps) {
  return (
    <ScrollAreaPrimitive.Root
      data-slot="scroll-area"
      data-orientation={orientation}
      type={type}
      className={cn("relative flex flex-col overflow-hidden", className)}
      style={{ position: undefined, ...style }}
      {...props}
    >
      <ScrollAreaPrimitive.Viewport
        {...viewportProps}
        ref={viewportRef}
        data-slot="scroll-area-viewport"
        className={cn(
          "min-h-0 w-full flex-1 rounded-[inherit] outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-inset",
          // The primitive wraps content in a `display: table` div so wide
          // content can scroll sideways. Vertical-only areas lay it out as a
          // block instead, which keeps the content at the viewport width and
          // lets text truncate.
          orientation === "vertical" && "[&>div]:block!",
          viewportClassName,
        )}
      >
        {children}
      </ScrollAreaPrimitive.Viewport>
      {orientation !== "horizontal" && <ScrollBar orientation="vertical" />}
      {orientation !== "vertical" && <ScrollBar orientation="horizontal" />}
      <ScrollAreaPrimitive.Corner />
    </ScrollAreaPrimitive.Root>
  );
}

/**
 * An 8px overlay track with a 4px thumb (rounded-bar), drawn over the edge of
 * the content so it never takes layout space. It fades in on hover or
 * scroll; reduced motion drops the fade.
 */
function ScrollBar({
  className,
  orientation = "vertical",
  ...props
}: React.ComponentProps<typeof ScrollAreaPrimitive.ScrollAreaScrollbar>) {
  return (
    <ScrollAreaPrimitive.ScrollAreaScrollbar
      data-slot="scroll-area-scrollbar"
      orientation={orientation}
      className={cn(
        "z-20 flex touch-none p-0.5 select-none",
        "data-horizontal:h-2 data-horizontal:flex-col data-vertical:h-full data-vertical:w-2",
        "data-[state=visible]:animate-in data-[state=visible]:fade-in-0 data-[state=hidden]:animate-out data-[state=hidden]:fade-out-0 motion-reduce:animate-none!",
        className,
      )}
      {...props}
    >
      <ScrollAreaPrimitive.ScrollAreaThumb
        data-slot="scroll-area-thumb"
        className="relative flex-1 rounded-bar bg-border-strong transition-colors hover:bg-text-4 active:bg-text-4"
      />
    </ScrollAreaPrimitive.ScrollAreaScrollbar>
  );
}

export { ScrollArea, ScrollBar };
