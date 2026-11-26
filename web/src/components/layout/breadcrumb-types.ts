import type { ReactNode } from "react";

/** A crumb is a label, or a render function for labels that need route params. */
export type Breadcrumb = string | ((params: Record<string, string>) => ReactNode);

declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    /** Label of this route in the topbar breadcrumbs. */
    breadcrumb?: Breadcrumb;
    /** A parent crumb for routes that are not nested in the route tree. */
    breadcrumbParent?: { label: string; to: string };
    /**
     * A trailing crumb for a tab or section chosen by search params (for
     * example "Images" on `/containers?tab=images`). Undefined for the
     * default view, which keeps the route's own crumb last.
     */
    breadcrumbSearch?: (search: Record<string, unknown>) => ReactNode | undefined;
  }
}
