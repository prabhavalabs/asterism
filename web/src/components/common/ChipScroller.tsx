import { ScrollArea, type ScrollAreaProps } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

export type ChipScrollerProps = Omit<ScrollAreaProps, "orientation">;

/**
 * A single-line row of chips or a segmented control that scrolls sideways
 * when it does not fit (mostly on phones), through the shared ScrollArea.
 *
 * The viewport pads 4px above (room for focus rings) and 10px below, where
 * the 8px horizontal scrollbar sits clear of the chips; matching negative
 * margins keep the row's layout height equal to its content, so a toolbar
 * row is still exactly one control tall.
 */
export function ChipScroller({ className, viewportClassName, children, ...props }: ChipScrollerProps) {
  return (
    <ScrollArea
      data-slot="chip-scroller"
      orientation="horizontal"
      className={cn("-mt-1 -mb-2.5 max-w-full min-w-0", className)}
      viewportClassName={cn("pt-1 pb-2.5", viewportClassName)}
      {...props}
    >
      {children}
    </ScrollArea>
  );
}
