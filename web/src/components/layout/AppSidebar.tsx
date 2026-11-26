import { Fragment } from "react";
import { Link, useLocation } from "@tanstack/react-router";
import type { HostInfo } from "@/gen/asterism/v1/asterism_pb";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { Icon } from "@/components/ui/icon";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
  useSidebar,
} from "@/components/ui/sidebar";
import { cn } from "@/lib/utils";
import { BrandMark } from "./BrandMark";
import { HostStatusCard } from "./HostStatusCard";
import { NAV_SECTIONS, isNavItemActive, navItemsFor, type NavCounts, type NavItem, type NavSearch } from "./nav";
import { UserMenu } from "./UserMenu";

const COUNT_NOUNS: Record<keyof NavCounts, string> = {
  alerts: "active alerts",
  projects: "projects",
  publicPorts: "public ports",
  ingressRoutes: "ingress routes",
  containers: "containers running",
  agents: "agents connected",
};

/** Badge text and its accessible label; alerts show only when above zero. */
function countText(item: NavItem, counts: NavCounts): { text: string; label: string } | undefined {
  if (!item.count) return undefined;
  const raw = counts[item.count];
  if (raw === undefined) return undefined;
  if (typeof raw === "object") {
    const n = "running" in raw ? raw.running : raw.connected;
    return { text: `${n}/${raw.total}`, label: `${n} of ${raw.total} ${COUNT_NOUNS[item.count]}` };
  }
  if (item.count === "alerts" && raw <= 0) return undefined;
  return { text: String(raw), label: `${raw} ${COUNT_NOUNS[item.count]}` };
}

/** Badge tint: amber for active alerts and for agents that are not all connected. */
function countTone(item: NavItem, counts: NavCounts): string | undefined {
  if (item.count === "alerts") return "bg-status-warning-surface text-status-warning";
  if (item.count === "agents" && counts.agents) {
    if (counts.agents.connected === 0) return "bg-status-critical-surface text-status-critical";
    if (counts.agents.connected < counts.agents.total) return "bg-status-warning-surface text-status-warning";
  }
  return undefined;
}

function NavLinkItem({
  item,
  items,
  counts,
  pathname,
  search,
}: {
  item: NavItem;
  items: readonly NavItem[];
  counts: NavCounts;
  pathname: string;
  search: NavSearch;
}) {
  const { isMobile, setOpenMobile } = useSidebar();
  const isActive = isNavItemActive(item, pathname, search, items);
  const count = countText(item, counts);
  const tooltip = count ? `${item.label} · ${count.text}` : item.label;

  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild isActive={isActive} tooltip={tooltip}>
        <Link
          to={item.to}
          search={item.search ?? {}}
          onClick={() => isMobile && setOpenMobile(false)}
        >
          <Icon icon={item.icon} />
          <span>{item.label}</span>
        </Link>
      </SidebarMenuButton>
      {count && (
        <SidebarMenuBadge
          aria-label={count.label}
          className={cn(countTone(item, counts))}
        >
          {count.text}
        </SidebarMenuBadge>
      )}
    </SidebarMenuItem>
  );
}

export interface AppSidebarProps {
  host?: HostInfo;
  /** Collector mode; agent mode adds the Agents item. */
  sourceMode?: string;
  /** The primary agent in agent mode, for the host card. */
  agent?: AgentStatus;
  counts: NavCounts;
  userEmail?: string;
  onLogout: () => void;
}

/**
 * The 240px sidebar, after the shadcn sidebar blocks: a brand row in the
 * team-switcher style, five labelled nav groups (Overview, Workloads,
 * Network, Host, Administration) split by hairline separators, then the
 * live host card and the user menu (which also links the documentation). On
 * desktop it collapses to a 48px icon rail (Cmd/Ctrl+B, the rail edge or
 * the topbar toggle) with a tooltip on every item; below `md` it opens as
 * a sheet.
 */
export function AppSidebar({ host, sourceMode, agent, counts, userEmail, onLogout }: AppSidebarProps) {
  const { isMobile, setOpenMobile } = useSidebar();
  const pathname = useLocation({ select: (location) => location.pathname });
  const search = useLocation({ select: (location) => location.search as NavSearch });
  const visible = navItemsFor(sourceMode);
  const sections = NAV_SECTIONS.map((section) => ({
    ...section,
    items: visible.filter((item) => item.section === section.id),
  })).filter((section) => section.items.length > 0);

  return (
    <Sidebar collapsible="icon">
      <nav aria-label="Primary" className="flex min-h-0 flex-1 flex-col">
        <SidebarHeader className="pb-0">
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton asChild size="lg" tooltip="Asterism" className="gap-2.5 px-2">
                <Link to="/" onClick={() => isMobile && setOpenMobile(false)}>
                  <span data-rail-keep="" className="flex size-8 shrink-0 items-center justify-center">
                    <BrandMark className="size-7!" />
                  </span>
                  <span className="grid min-w-0 flex-1 leading-tight">
                    <span className="truncate font-heading text-[15px] font-semibold tracking-[-0.01em] text-foreground">
                      Asterism
                    </span>
                    <span className="truncate text-small text-muted-foreground">VPS observatory</span>
                  </span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent>
          {sections.map((section, index) => (
            <Fragment key={section.id}>
              {index > 0 && <SidebarSeparator aria-hidden="true" />}
              <SidebarGroup aria-labelledby={`nav-group-${section.id}`}>
                <SidebarGroupLabel id={`nav-group-${section.id}`}>{section.label}</SidebarGroupLabel>
                <SidebarMenu>
                  {section.items.map((item) => (
                    <NavLinkItem key={item.id} item={item} items={visible} counts={counts} pathname={pathname} search={search} />
                  ))}
                </SidebarMenu>
              </SidebarGroup>
            </Fragment>
          ))}

        </SidebarContent>

        <SidebarFooter className="gap-1.5">
          <HostStatusCard host={host} agent={agent} />
          <UserMenu email={userEmail} onLogout={onLogout} />
        </SidebarFooter>
      </nav>
      {!isMobile && <SidebarRail />}
    </Sidebar>
  );
}
