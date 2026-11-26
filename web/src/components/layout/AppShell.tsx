import type { CSSProperties, ReactNode } from "react";
import type { ContainerCounts, HostInfo } from "@/gen/asterism/v1/asterism_pb";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { AppSidebar } from "./AppSidebar";
import type { NavCounts } from "./nav";
import { Topbar } from "./Topbar";

export interface AppShellProps {
  host?: HostInfo;
  projectCount?: number;
  /** Public port count, shown on "Ports". */
  portCount?: number;
  /** Ingress route count, shown on "Ingress & domains". */
  ingressCount?: number;
  /** Active alert count, shown on "Events" when above zero. */
  alertCount?: number;
  /** Docker container counts, shown as running/total on "Containers". */
  containerCounts?: ContainerCounts;
  /** Collector mode from Capabilities.source_mode. */
  sourceMode?: string;
  /** Agent mode: the primary agent and the connected/enrolled counts. */
  agent?: AgentStatus;
  agentCounts?: { connected: number; total: number };
  userEmail?: string;
  onLogout: () => void;
  children: ReactNode;
}

const sidebarStyle = { "--sidebar-width": "240px" } as CSSProperties;

/**
 * Signed-in layout: 240px sidebar (offcanvas, Cmd/Ctrl+B, sheet below 768px)
 * + inset with a 48px topbar and the page in #content.
 */
export function AppShell({
  host,
  projectCount,
  portCount,
  ingressCount,
  alertCount,
  containerCounts,
  sourceMode,
  agent,
  agentCounts,
  userEmail,
  onLogout,
  children,
}: AppShellProps) {
  const counts: NavCounts = {
    projects: projectCount,
    publicPorts: portCount,
    ingressRoutes: ingressCount,
    alerts: alertCount,
    containers: containerCounts && containerCounts.total > 0
      ? { running: containerCounts.running, total: containerCounts.total }
      : undefined,
    agents: sourceMode === "agent" ? agentCounts : undefined,
  };

  return (
    <SidebarProvider style={sidebarStyle}>
      <a
        href="#content"
        className="sr-only z-50 rounded-button bg-card px-3 py-2 text-control shadow-pop focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <AppSidebar host={host} sourceMode={sourceMode} agent={agent} counts={counts} userEmail={userEmail} onLogout={onLogout} />
      <SidebarInset>
        <Topbar hostname={host?.hostname} agent={sourceMode === "agent" ? agent : undefined} />
        <div id="content" tabIndex={-1} className="flex min-w-0 flex-1 flex-col outline-none">
          {children}
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
