import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { SidebarLeftIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { Button, type ButtonProps } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useIsMobile } from "@/hooks/use-mobile";
import { cn } from "@/lib/utils";

const SIDEBAR_STORAGE_KEY = "asterism-sidebar";
const SIDEBAR_WIDTH = "240px";
const SIDEBAR_WIDTH_MOBILE = "18rem";
/** Icon rail: one nav item (32px, 40px on touch) plus the 8px group gutter each side. */
const SIDEBAR_WIDTH_ICON = "calc(var(--sidebar-item-h) + 1rem)";
const SIDEBAR_KEYBOARD_SHORTCUT = "b";

export interface SidebarContextValue {
  state: "expanded" | "collapsed";
  open: boolean;
  setOpen: (open: boolean | ((open: boolean) => boolean)) => void;
  openMobile: boolean;
  setOpenMobile: (open: boolean) => void;
  isMobile: boolean;
  toggleSidebar: () => void;
}

const SidebarContext = React.createContext<SidebarContextValue | null>(null);

function useSidebar(): SidebarContextValue {
  const context = React.useContext(SidebarContext);
  if (!context) {
    throw new Error("useSidebar must be used within a SidebarProvider.");
  }
  return context;
}

function readStoredOpen(): boolean | undefined {
  try {
    const stored = window.localStorage.getItem(SIDEBAR_STORAGE_KEY);
    return stored === null ? undefined : stored === "true";
  } catch {
    return undefined;
  }
}

function writeStoredOpen(open: boolean): void {
  try {
    window.localStorage.setItem(SIDEBAR_STORAGE_KEY, String(open));
  } catch {
    // Storage can be unavailable; the state still applies to this page.
  }
}

export interface SidebarProviderProps extends React.ComponentProps<"div"> {
  /** Initial state. Without it the last stored state is restored, falling back to open. */
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

/**
 * Holds the sidebar state: desktop open/closed (persisted, Cmd/Ctrl+B) and
 * the mobile sheet (below 768px).
 */
function SidebarProvider({
  defaultOpen,
  open: openProp,
  onOpenChange: setOpenProp,
  className,
  style,
  children,
  ...props
}: SidebarProviderProps) {
  const isMobile = useIsMobile();
  const [openMobile, setOpenMobile] = React.useState(false);
  const wrapperRef = React.useRef<HTMLDivElement>(null);

  const [internalOpen, setInternalOpen] = React.useState(
    () => defaultOpen ?? readStoredOpen() ?? true,
  );
  const open = openProp ?? internalOpen;
  const setOpen = React.useCallback(
    (value: boolean | ((value: boolean) => boolean)) => {
      const next = typeof value === "function" ? value(open) : value;
      if (setOpenProp) setOpenProp(next);
      else setInternalOpen(next);
      writeStoredOpen(next);
    },
    [setOpenProp, open],
  );

  const toggleSidebar = React.useCallback(() => {
    if (isMobile) setOpenMobile((value) => !value);
    else setOpen((value) => !value);
  }, [isMobile, setOpen]);

  React.useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (
        event.key.toLowerCase() === SIDEBAR_KEYBOARD_SHORTCUT &&
        (event.metaKey || event.ctrlKey) &&
        !event.altKey &&
        !event.shiftKey
      ) {
        event.preventDefault();
        // Keyboard toggles are frequent: the sidebar jumps instead of animating.
        if (!isMobile && wrapperRef.current) wrapperRef.current.dataset.instant = "";
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isMobile, toggleSidebar]);

  // After a keyboard toggle commits, flush the new layout with transitions off.
  React.useLayoutEffect(() => {
    const wrapper = wrapperRef.current;
    if (!wrapper || !("instant" in wrapper.dataset)) return;
    void wrapper.offsetWidth;
    delete wrapper.dataset.instant;
  }, [open]);

  const state = open ? "expanded" : "collapsed";

  const contextValue = React.useMemo<SidebarContextValue>(
    () => ({ state, open, setOpen, isMobile, openMobile, setOpenMobile, toggleSidebar }),
    [state, open, setOpen, isMobile, openMobile, toggleSidebar],
  );

  return (
    <SidebarContext.Provider value={contextValue}>
      <div
        ref={wrapperRef}
        data-slot="sidebar-wrapper"
        style={
          {
            "--sidebar-width": SIDEBAR_WIDTH,
            "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
            ...style,
          } as React.CSSProperties
        }
        className={cn("group/sidebar-wrapper flex min-h-svh w-full", className)}
        {...props}
      >
        {children}
      </div>
    </SidebarContext.Provider>
  );
}

export interface SidebarProps extends React.ComponentProps<"div"> {
  side?: "left" | "right";
  /**
   * `icon` collapses to a rail of icons (with tooltips); `offcanvas` hides the
   * sidebar fully when collapsed; `none` is always shown.
   */
  collapsible?: "icon" | "offcanvas" | "none";
}

function Sidebar({
  side = "left",
  collapsible = "offcanvas",
  className,
  children,
  ...props
}: SidebarProps) {
  const { isMobile, state, openMobile, setOpenMobile } = useSidebar();

  if (collapsible === "none") {
    return (
      <div
        data-slot="sidebar"
        className={cn(
          "flex h-full w-(--sidebar-width) flex-col bg-sidebar text-sidebar-foreground",
          className,
        )}
        {...props}
      >
        {children}
      </div>
    );
  }

  if (isMobile) {
    return (
      <Sheet open={openMobile} onOpenChange={setOpenMobile}>
        <SheetContent
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          side={side}
          showCloseButton={false}
          className="bg-sidebar p-0 text-sidebar-foreground data-[side=left]:w-(--sidebar-width) data-[side=left]:max-w-[85vw] data-[side=left]:sm:max-w-[85vw] data-[side=right]:w-(--sidebar-width) data-[side=right]:max-w-[85vw] data-[side=right]:sm:max-w-[85vw]"
          style={{ "--sidebar-width": SIDEBAR_WIDTH_MOBILE } as React.CSSProperties}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Navigation</SheetTitle>
            <SheetDescription>Primary navigation</SheetDescription>
          </SheetHeader>
          <div className="flex h-full w-full flex-col">{children}</div>
        </SheetContent>
      </Sheet>
    );
  }

  return (
    <div
      className="group peer hidden text-sidebar-foreground md:block"
      data-state={state}
      data-collapsible={state === "collapsed" ? collapsible : ""}
      data-side={side}
      data-slot="sidebar"
    >
      {/* Reserves the sidebar's width in the flow on desktop. */}
      <div
        data-slot="sidebar-gap"
        className={cn(
          "relative w-(--sidebar-width) bg-transparent transition-[width] duration-tabs ease-in-out motion-reduce:transition-none group-data-instant/sidebar-wrapper:transition-none",
          "group-data-[collapsible=offcanvas]:w-0 group-data-[collapsible=icon]:w-(--sidebar-width-icon)",
          "group-data-[side=right]:rotate-180",
        )}
      />
      <div
        data-slot="sidebar-container"
        data-side={side}
        className={cn(
          "fixed inset-y-0 z-10 hidden h-svh w-(--sidebar-width) transition-[left,right,width] duration-tabs ease-in-out motion-reduce:transition-none group-data-instant/sidebar-wrapper:transition-none md:flex",
          "data-[side=left]:left-0 data-[side=left]:group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)]",
          "data-[side=right]:right-0 data-[side=right]:group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)]",
          "group-data-[collapsible=icon]:w-(--sidebar-width-icon)",
          "group-data-[side=left]:border-r group-data-[side=left]:border-sidebar-border group-data-[side=right]:border-l group-data-[side=right]:border-sidebar-border",
          className,
        )}
        {...props}
      >
        <div
          data-sidebar="sidebar"
          data-slot="sidebar-inner"
          className="flex size-full flex-col bg-sidebar"
        >
          {children}
        </div>
      </div>
    </div>
  );
}

function SidebarTrigger({ className, onClick, ...props }: ButtonProps) {
  const { toggleSidebar, open, isMobile, openMobile } = useSidebar();
  const expanded = isMobile ? openMobile : open;

  return (
    <Button
      data-sidebar="trigger"
      data-slot="sidebar-trigger"
      variant="ghost"
      size="icon"
      aria-label={expanded ? "Collapse sidebar" : "Expand sidebar"}
      aria-expanded={expanded}
      title="Toggle sidebar (⌘B)"
      className={className}
      onClick={(event) => {
        onClick?.(event);
        toggleSidebar();
      }}
      {...props}
    >
      <Icon icon={SidebarLeftIcon} />
    </Button>
  );
}

/** Thin hit area on the sidebar edge that toggles it on click. */
function SidebarRail({ className, ...props }: React.ComponentProps<"button">) {
  const { toggleSidebar } = useSidebar();

  return (
    <button
      type="button"
      data-sidebar="rail"
      data-slot="sidebar-rail"
      aria-label="Toggle sidebar"
      tabIndex={-1}
      onClick={toggleSidebar}
      title="Toggle sidebar"
      className={cn(
        "absolute inset-y-0 z-20 hidden w-4 -translate-x-1/2 transition-colors group-data-[side=left]:-right-4 group-data-[side=right]:left-0 after:absolute after:inset-y-0 after:left-1/2 after:w-[2px] hover:after:bg-sidebar-border sm:flex",
        "in-data-[side=left]:cursor-w-resize in-data-[side=right]:cursor-e-resize",
        "[[data-side=left][data-state=collapsed]_&]:cursor-e-resize [[data-side=right][data-state=collapsed]_&]:cursor-w-resize",
        "group-data-[collapsible=offcanvas]:translate-x-0 group-data-[collapsible=offcanvas]:after:left-full hover:group-data-[collapsible=offcanvas]:bg-sidebar",
        className,
      )}
      {...props}
    />
  );
}

function SidebarInset({ className, ...props }: React.ComponentProps<"main">) {
  return (
    <main
      data-slot="sidebar-inset"
      className={cn("relative flex w-full min-w-0 flex-1 flex-col bg-background", className)}
      {...props}
    />
  );
}

function SidebarHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-header"
      data-sidebar="header"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  );
}

function SidebarFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-footer"
      data-sidebar="footer"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  );
}

function SidebarSeparator({ className, ...props }: React.ComponentProps<typeof Separator>) {
  return (
    <Separator
      data-slot="sidebar-separator"
      data-sidebar="separator"
      className={cn("mx-3 w-auto shrink-0 bg-sidebar-border group-data-[collapsible=icon]:mx-2", className)}
      {...props}
    />
  );
}

/**
 * Scrolling middle section. Only ever scrolls vertically (when the window is
 * too short for every group) through the shared ScrollArea, whose thin
 * overlay scrollbar takes no width; horizontal overflow is clipped.
 */
function SidebarContent({ className, children, ...props }: React.ComponentProps<typeof ScrollArea>) {
  return (
    <ScrollArea
      data-sidebar="content"
      orientation="vertical"
      className={cn("min-h-0 flex-1", className)}
      viewportClassName="overscroll-contain"
      {...props}
    >
      <div data-slot="sidebar-content" className="flex w-full min-w-0 flex-col">
        {children}
      </div>
    </ScrollArea>
  );
}

/** A nav group: 8px side gutter, 6px above and below (12px plus a hairline between groups). */
function SidebarGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group"
      data-sidebar="group"
      className={cn("relative flex w-full min-w-0 flex-col px-2 py-1.5", className)}
      {...props}
    />
  );
}

/** Group heading: 24px row, 12px muted text; slides away on the icon rail. */
function SidebarGroupLabel({
  className,
  asChild = false,
  ...props
}: React.ComponentProps<"div"> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "div";

  return (
    <Comp
      data-slot="sidebar-group-label"
      data-sidebar="group-label"
      className={cn(
        "flex h-6 shrink-0 items-center overflow-hidden rounded-menu-item px-2 font-medium whitespace-nowrap text-pill text-text-4 outline-hidden ring-sidebar-ring transition-[margin,opacity] duration-tabs ease-in-out focus-visible:ring-1 motion-reduce:transition-none [&>svg]:size-3.5 [&>svg]:shrink-0",
        "group-data-[collapsible=icon]:-mt-6 group-data-[collapsible=icon]:opacity-0",
        className,
      )}
      {...props}
    />
  );
}

function SidebarGroupAction({
  className,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "button";

  return (
    <Comp
      data-slot="sidebar-group-action"
      data-sidebar="group-action"
      className={cn(
        "absolute top-1.5 right-3 flex size-control-sm items-center justify-center rounded-button-sm p-0 text-text-4 outline-hidden ring-sidebar-ring transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-1 group-data-[collapsible=icon]:hidden [&>svg]:size-4 [&>svg]:shrink-0",
        className,
      )}
      {...props}
    />
  );
}

function SidebarGroupContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group-content"
      data-sidebar="group-content"
      className={cn("w-full text-[13.5px]", className)}
      {...props}
    />
  );
}

function SidebarMenu({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu"
      data-sidebar="menu"
      className={cn("flex w-full min-w-0 flex-col gap-px", className)}
      {...props}
    />
  );
}

function SidebarMenuItem({ className, ...props }: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-item"
      data-sidebar="menu-item"
      className={cn("group/menu-item relative", className)}
      {...props}
    />
  );
}

// Navigation highlights change colour only (150ms ease); nothing moves. The
// active item gets the subtle accent tint and a blue icon. On the icon rail
// every button shrinks to a square the size of one nav item and shows only
// its first child (the icon, avatar or logo tile) plus any [data-rail-keep].
const sidebarMenuButtonVariants = cva(
  [
    "peer/menu-button group/menu-button flex w-full items-center gap-2.5 overflow-hidden rounded-nav px-2 text-left text-sidebar-foreground outline-hidden ring-sidebar-ring transition-[color,background-color,box-shadow,width,height,padding] duration-tabs motion-reduce:transition-none",
    "group-has-data-[sidebar=menu-badge]/menu-item:pr-10 group-has-data-[sidebar=menu-action]/menu-item:pr-10",
    "focus-visible:ring-1 disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50",
    "data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground",
    "data-[active=true]:bg-sidebar-accent data-[active=true]:font-medium data-[active=true]:text-sidebar-accent-foreground data-[active=true]:[&_svg]:text-sidebar-primary",
    "group-data-[collapsible=icon]:size-sidebar-item! group-data-[collapsible=icon]:px-0! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:[&>*:not(:first-child,[data-rail-keep])]:hidden",
    "[&>span:last-child]:truncate [&_svg]:size-4 [&_svg]:shrink-0 [&_svg]:text-muted-foreground",
  ],
  {
    variants: {
      variant: {
        default: "hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
        outline:
          "bg-card shadow-[0_0_0_1px_var(--sidebar-border)] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
      },
      size: {
        default: "h-sidebar-item text-[13.5px]",
        sm: "h-control-sm text-small",
        lg: "h-12 text-[13.5px]",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface SidebarMenuButtonProps
  extends React.ComponentProps<"button">,
    VariantProps<typeof sidebarMenuButtonVariants> {
  asChild?: boolean;
  isActive?: boolean;
  /** Label shown to the right of the button while the sidebar is an icon rail. */
  tooltip?: React.ReactNode;
}

/**
 * A 32px nav item (40px on touch), 13.5px text, 16px icon. The active item
 * gets the subtle accent and a blue icon. With `tooltip`, the label appears
 * beside the icon rail when the sidebar is collapsed.
 */
function SidebarMenuButton({
  asChild = false,
  isActive = false,
  variant = "default",
  size = "default",
  tooltip,
  className,
  ...props
}: SidebarMenuButtonProps) {
  const Comp = asChild ? Slot : "button";
  const { isMobile, state } = useSidebar();

  const button = (
    <Comp
      data-slot="sidebar-menu-button"
      data-sidebar="menu-button"
      data-size={size}
      data-active={isActive}
      aria-current={isActive && asChild ? "page" : undefined}
      className={cn(sidebarMenuButtonVariants({ variant, size }), className)}
      {...props}
    />
  );

  if (!tooltip) return button;

  return (
    <Tooltip>
      <TooltipTrigger asChild>{button}</TooltipTrigger>
      <TooltipContent side="right" align="center" hidden={state !== "collapsed" || isMobile}>
        {tooltip}
      </TooltipContent>
    </Tooltip>
  );
}

function SidebarMenuAction({
  className,
  asChild = false,
  showOnHover = false,
  ...props
}: React.ComponentProps<"button"> & { asChild?: boolean; showOnHover?: boolean }) {
  const Comp = asChild ? Slot : "button";

  return (
    <Comp
      data-slot="sidebar-menu-action"
      data-sidebar="menu-action"
      className={cn(
        "absolute top-1/2 right-1 flex size-control-sm -translate-y-1/2 items-center justify-center rounded-button-sm p-0 text-text-4 outline-hidden ring-sidebar-ring transition-[color,background-color,opacity] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-1 group-data-[collapsible=icon]:hidden [&>svg]:size-4 [&>svg]:shrink-0",
        showOnHover &&
          "group-focus-within/menu-item:opacity-100 group-hover/menu-item:opacity-100 aria-expanded:opacity-100 md:opacity-0",
        className,
      )}
      {...props}
    />
  );
}

/** Count chip on the right of a menu item (22px, tabular). */
function SidebarMenuBadge({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-menu-badge"
      data-sidebar="menu-badge"
      className={cn(
        "pointer-events-none absolute top-1/2 right-1.5 flex h-pill min-w-pill -translate-y-1/2 select-none items-center justify-center rounded-chip px-1.5 font-mono text-pill text-text-4 tabular-nums group-data-[collapsible=icon]:hidden",
        "peer-data-[active=true]/menu-button:text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}

function SidebarMenuSkeleton({
  className,
  showIcon = false,
  ...props
}: React.ComponentProps<"div"> & { showIcon?: boolean }) {
  return (
    <div
      data-slot="sidebar-menu-skeleton"
      data-sidebar="menu-skeleton"
      className={cn("flex h-sidebar-item items-center gap-2.5 rounded-nav px-2", className)}
      {...props}
    >
      {showIcon && <Skeleton className="size-4" />}
      <Skeleton className="h-3 max-w-[70%] flex-1" />
    </div>
  );
}

export {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
  SidebarTrigger,
  // eslint-disable-next-line react-refresh/only-export-components
  useSidebar,
};
