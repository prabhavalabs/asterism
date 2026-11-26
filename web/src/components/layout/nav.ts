import {
  Activity01Icon,
  AuditIcon,
  ConnectIcon,
  ContainerIcon,
  CubeIcon,
  DashboardSquare01Icon,
  DatabaseIcon,
  Folder01Icon,
  Globe02Icon,
  HardDriveIcon,
  Image01Icon,
  NetworkIcon,
  Notification03Icon,
  Plug01Icon,
  Satellite01Icon,
  Settings01Icon,
} from "@hugeicons/core-free-icons";
import { type IconData } from "@/components/ui/icon";

export type NavSection = "overview" | "workloads" | "network" | "host" | "admin";

/** Counts the shell can show next to nav items (cheap: from GetOverview). */
export interface NavCounts {
  projects?: number;
  publicPorts?: number;
  ingressRoutes?: number;
  alerts?: number;
  containers?: { running: number; total: number };
  /** Enrolled agents (agent mode only). */
  agents?: { connected: number; total: number };
}

/** Search params as the router parses them. */
export type NavSearch = Record<string, unknown>;

export interface NavItem {
  id: string;
  label: string;
  to: string;
  /** Search params the link sets (tab deep links such as `?tab=images`). */
  search?: Record<string, string>;
  icon: IconData;
  section: NavSection;
  /** Active only on an exact path match (otherwise nested paths match too). */
  exact?: boolean;
  /**
   * Search param this item owns on its path. The item is active when the
   * param equals `search[param]`, or, for the item without `search`, when
   * the param is absent or names none of its siblings.
   */
  param?: string;
  /** Which count to show as a badge. */
  count?: keyof NavCounts;
  /** Shown only in this collector mode (Capabilities.source_mode). */
  mode?: string;
}

export const NAV_SECTIONS: ReadonlyArray<{ id: NavSection; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "workloads", label: "Workloads" },
  { id: "network", label: "Network" },
  { id: "host", label: "Host" },
  { id: "admin", label: "Administration" },
];

export const NAV_ITEMS: readonly NavItem[] = [
  { id: "dashboard", label: "Dashboard", to: "/", icon: DashboardSquare01Icon, section: "overview", exact: true },
  { id: "topology", label: "Topology map", to: "/topology", icon: CubeIcon, section: "overview" },
  { id: "events", label: "Events", to: "/events", icon: Notification03Icon, section: "overview", param: "kind", count: "alerts" },

  { id: "projects", label: "Projects", to: "/projects", icon: Folder01Icon, section: "workloads", count: "projects" },
  { id: "containers", label: "Containers", to: "/containers", icon: ContainerIcon, section: "workloads", param: "tab", count: "containers" },
  { id: "images", label: "Images", to: "/containers", search: { tab: "images" }, icon: Image01Icon, section: "workloads", param: "tab" },
  { id: "volumes", label: "Volumes", to: "/containers", search: { tab: "volumes" }, icon: DatabaseIcon, section: "workloads", param: "tab" },
  { id: "networks", label: "Networks", to: "/containers", search: { tab: "networks" }, icon: ConnectIcon, section: "workloads", param: "tab" },

  { id: "ports", label: "Ports", to: "/ports", icon: NetworkIcon, section: "network", param: "tab", count: "publicPorts" },
  { id: "ingress", label: "Ingress & domains", to: "/ports", search: { tab: "ingress" }, icon: Globe02Icon, section: "network", param: "tab", count: "ingressRoutes" },

  { id: "host", label: "Host vitals", to: "/host", icon: Activity01Icon, section: "host" },
  { id: "disk", label: "Disk usage", to: "/containers", search: { tab: "disk" }, icon: HardDriveIcon, section: "host", param: "tab" },

  { id: "audit", label: "Audit log", to: "/events", search: { kind: "audit" }, icon: AuditIcon, section: "admin", param: "kind" },
  { id: "agents", label: "Agents", to: "/settings", search: { section: "agents" }, icon: Satellite01Icon, section: "admin", param: "section", count: "agents", mode: "agent" },
  { id: "settings", label: "Settings", to: "/settings", icon: Settings01Icon, section: "admin", param: "section" },
  { id: "mcp", label: "API & MCP", to: "/settings", search: { section: "mcp" }, icon: Plug01Icon, section: "admin", param: "section" },
];

/** The items for a collector mode: mode-specific items (Agents) only in their mode. */
export function navItemsFor(sourceMode: string | undefined): readonly NavItem[] {
  return NAV_ITEMS.filter((item) => !item.mode || item.mode === sourceMode);
}

/** Public repository, linked from the sidebar footer. */
export const DOCS_URL = "https://github.com/prabhavalabs/asterism";

function pathMatches(item: NavItem, pathname: string): boolean {
  if (pathname === item.to) return true;
  if (item.exact) return false;
  return pathname.startsWith(item.to.endsWith("/") ? item.to : `${item.to}/`);
}

/**
 * Whether a nav item is the current page. Items that share a path (the
 * Containers tabs, Ports and Ingress, Events and the audit log, Settings and
 * its MCP section) are told apart by the search param they own. Nested
 * paths (a container's detail page) activate only the item without search.
 * Pass the visible `items` so a section whose item is hidden in this mode
 * (Agents outside agent mode) falls back to its page's main item.
 */
export function isNavItemActive(
  item: NavItem,
  pathname: string,
  search: NavSearch,
  items: readonly NavItem[] = NAV_ITEMS,
): boolean {
  if (!pathMatches(item, pathname)) return false;
  if (!item.param) return true;
  const nested = pathname !== item.to;
  if (item.search) return !nested && search[item.param] === item.search[item.param];
  if (nested) return true;
  const value = search[item.param];
  const claimed = items.some(
    (other) => other !== item && other.to === item.to && other.search?.[item.param ?? ""] === value,
  );
  return value === undefined || !claimed;
}
