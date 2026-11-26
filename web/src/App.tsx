import { Suspense, createContext, lazy, useContext, useEffect } from "react";
import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  type ErrorComponentProps,
} from "@tanstack/react-router";
import type { ContainerCounts, HostInfo } from "@/gen/asterism/v1/asterism_pb";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { connectedCount, primaryAgent } from "@/components/agents/model";
import { useAgents } from "@/components/agents/useAgents";
import { ErrorState, LoadingState, Page, ProjectIcon } from "@/components/common";
import { AppShell } from "@/components/layout/AppShell";
import "@/components/layout/breadcrumb-types";
import { Card } from "@/components/ui/card";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LoginPage } from "@/pages/LoginPage";
import { fetchAuthSession, inventoryClient, logoutAdmin, type AuthSession } from "@/lib/rpc";
import {
  ALERTS_TAB_LABELS,
  CONTAINERS_TAB_LABELS,
  PORTS_TAB_LABELS,
  validateAlertsSearch,
  validateContainerDetailSearch,
  validateContainersSearch,
  validateEventsSearch,
  validateNotificationsSearch,
  validatePortsSearch,
  validateProjectDetailSearch,
  validateProjectsSearch,
  validateSettingsSearch,
  validateTopologySearch,
  type AlertsSearch,
  type ContainersSearch,
  type EventsSearch,
  type PortsSearch,
  type SettingsSearch,
  type TopologySearch,
} from "@/lib/routeSearch";
import { liveStore } from "@/live/LiveStore";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: 1,
    },
  },
});

interface AppShellContextValue {
  host?: HostInfo;
  projectCount?: number;
  portCount?: number;
  ingressCount?: number;
  alertCount?: number;
  containerCounts?: ContainerCounts;
  sourceMode?: string;
  agent?: AgentStatus;
  agentCounts?: { connected: number; total: number };
  userEmail?: string;
  onLogout: () => void;
}

const AppShellContext = createContext<AppShellContextValue | null>(null);

function useAppShellContext(): AppShellContextValue {
  const ctx = useContext(AppShellContext);
  if (!ctx) {
    throw new Error("useAppShellContext must be used inside AppShellContext.Provider");
  }
  return ctx;
}

function RootLayout() {
  const ctx = useAppShellContext();
  return (
    <AppShell
      host={ctx.host}
      projectCount={ctx.projectCount}
      portCount={ctx.portCount}
      ingressCount={ctx.ingressCount}
      alertCount={ctx.alertCount}
      containerCounts={ctx.containerCounts}
      sourceMode={ctx.sourceMode}
      agent={ctx.agent}
      agentCounts={ctx.agentCounts}
      userEmail={ctx.userEmail}
      onLogout={ctx.onLogout}
    >
      <Outlet />
    </AppShell>
  );
}

// Pages are code-split per route so the initial bundle carries only the shell.
// The toast layer (sonner) loads right after the shell; only actions raise toasts.
const Toaster = lazy(() => import("@/components/ui/sonner").then((m) => ({ default: m.Toaster })));
const SettingsPage = lazy(() => import("@/pages/SettingsPage").then((m) => ({ default: m.SettingsPage })));

function SettingsRouteView() {
  const ctx = useAppShellContext();
  return <SettingsPage host={ctx.host} sourceMode={ctx.sourceMode} userEmail={ctx.userEmail} onLogout={ctx.onLogout} />;
}

/** Project crumb with its icon; shares the detail page's query, so no extra request. */
function ProjectCrumb({ name }: { name: string }) {
  const query = useQuery({
    queryKey: ["project", name],
    queryFn: () => inventoryClient.getProject({ id: name }),
    staleTime: 15_000,
  });
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <ProjectIcon icon={query.data?.project?.icon} name={name} size={16} />
      <span className="truncate">{name}</span>
    </span>
  );
}

function RoutePending() {
  return (
    <Page>
      <LoadingState label="Loading page…" className="min-h-[50vh]" />
    </Page>
  );
}

function RouteError({ error, reset }: ErrorComponentProps) {
  return (
    <Page>
      <Card>
        <ErrorState title="This page failed to load" error={error} onRetry={reset} />
      </Card>
    </Page>
  );
}

const rootRoute = createRootRoute({ component: RootLayout });
const portalRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: lazyRouteComponent(() => import("@/pages/PortalPage"), "PortalPage"),
  staticData: { breadcrumb: "Overview" },
});
const topologyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/topology",
  component: lazyRouteComponent(() => import("@/pages/TopologyPage"), "TopologyPage"),
  validateSearch: validateTopologySearch,
  staticData: {
    breadcrumb: "Topology map",
    breadcrumbSearch: (search) => (search as TopologySearch).project,
  },
});
const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects",
  component: lazyRouteComponent(() => import("@/pages/ProjectsPage"), "ProjectsPage"),
  validateSearch: validateProjectsSearch,
  staticData: { breadcrumb: "Projects" },
});
const projectDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects/$projectId",
  component: lazyRouteComponent(() => import("@/pages/ProjectDetailPage"), "ProjectDetailPage"),
  validateSearch: validateProjectDetailSearch,
  staticData: {
    breadcrumb: (params) => <ProjectCrumb name={params.projectId} />,
    breadcrumbParent: { label: "Projects", to: "/projects" },
  },
});
const containersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/containers",
  component: lazyRouteComponent(() => import("@/pages/ContainersPage"), "ContainersPage"),
  validateSearch: validateContainersSearch,
  staticData: {
    breadcrumb: "Containers",
    breadcrumbSearch: (search) => {
      const { tab } = search as ContainersSearch;
      return tab ? CONTAINERS_TAB_LABELS[tab] : undefined;
    },
  },
});
const containerDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/containers/$containerId",
  component: lazyRouteComponent(() => import("@/pages/ContainerDetailPage"), "ContainerDetailPage"),
  validateSearch: validateContainerDetailSearch,
  staticData: {
    breadcrumb: (params) => <span className="font-mono">{params.containerId}</span>,
    breadcrumbParent: { label: "Containers", to: "/containers" },
  },
});
const portsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/ports",
  component: lazyRouteComponent(() => import("@/pages/PortsIngressPage"), "PortsIngressPage"),
  validateSearch: validatePortsSearch,
  staticData: {
    breadcrumb: "Ports & Ingress",
    breadcrumbSearch: (search) => {
      const { tab } = search as PortsSearch;
      return tab ? PORTS_TAB_LABELS[tab] : undefined;
    },
  },
});
const hostRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/host",
  component: lazyRouteComponent(() => import("@/pages/HostPage"), "HostPage"),
  staticData: { breadcrumb: "Host vitals" },
});
const eventsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/events",
  component: lazyRouteComponent(() => import("@/pages/EventsPage"), "EventsPage"),
  validateSearch: validateEventsSearch,
  staticData: {
    breadcrumb: "Events",
    breadcrumbSearch: (search) => ((search as EventsSearch).kind === "audit" ? "Audit log" : undefined),
  },
});
const alertsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/alerts",
  component: lazyRouteComponent(() => import("@/pages/AlertsPage"), "AlertsPage"),
  validateSearch: validateAlertsSearch,
  staticData: {
    breadcrumb: "Alerts",
    breadcrumbSearch: (search) => {
      const { tab } = search as AlertsSearch;
      return tab ? ALERTS_TAB_LABELS[tab] : undefined;
    },
  },
});
const notificationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings/notifications",
  component: lazyRouteComponent(() => import("@/pages/NotificationsPage"), "NotificationsPage"),
  validateSearch: validateNotificationsSearch,
  staticData: {
    breadcrumb: "Notifications",
    breadcrumbParent: { label: "Settings", to: "/settings" },
  },
});
const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: SettingsRouteView,
  validateSearch: validateSettingsSearch,
  staticData: {
    breadcrumb: "Settings",
    breadcrumbSearch: (search) => {
      const { section } = search as SettingsSearch;
      return section === "mcp" ? "API & MCP" : section === "agents" ? "Agents" : undefined;
    },
  },
});

const routeTree = rootRoute.addChildren([
  portalRoute,
  topologyRoute,
  projectsRoute,
  projectDetailRoute,
  containersRoute,
  containerDetailRoute,
  portsRoute,
  hostRoute,
  eventsRoute,
  alertsRoute,
  settingsRoute,
  notificationsRoute,
]);

const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  defaultPendingComponent: RoutePending,
  defaultErrorComponent: RouteError,
});

function AuthenticatedApp({ session, onLogout }: { session: AuthSession; onLogout: () => void }) {
  const overviewQuery = useQuery({
    queryKey: ["overview"],
    queryFn: () => inventoryClient.getOverview({}),
    refetchInterval: 15_000,
  });

  useEffect(() => {
    liveStore.start();
    return () => {
      liveStore.stop();
    };
  }, []);

  const ov = overviewQuery.data;
  const sourceMode = ov?.capabilities?.sourceMode || ov?.host?.sourceMode || undefined;
  const agentsQuery = useAgents(sourceMode === "agent");
  const agents = sourceMode === "agent" ? agentsQuery.data : undefined;

  return (
    <AppShellContext.Provider
      value={{
        host: ov?.host,
        projectCount: ov?.projects.length,
        portCount: ov?.publicPortCount,
        ingressCount: ov?.ingressRouteCount,
        alertCount: ov?.activeAlertCount,
        containerCounts: ov?.containerCounts,
        sourceMode,
        agent: primaryAgent(agents),
        agentCounts: agents ? { connected: connectedCount(agents.agents), total: agents.agents.length } : undefined,
        userEmail: session.email,
        onLogout,
      }}
    >
      <RouterProvider router={router} />
      <Suspense fallback={null}>
        <Toaster />
      </Suspense>
    </AppShellContext.Provider>
  );
}

function AuthGate() {
  const qc = useQueryClient();

  const sessionQuery = useQuery({
    queryKey: ["auth-session"],
    queryFn: fetchAuthSession,
    staleTime: 60_000,
    retry: false,
  });

  const handleLogout = async () => {
    liveStore.stop();
    try {
      await logoutAdmin();
    } finally {
      // Update the session query in place before dropping everything else:
      // clearing it would detach the AuthGate observer, which would then
      // never see the signed-out state.
      qc.setQueryData<AuthSession>(["auth-session"], { authenticated: false });
      qc.removeQueries({ predicate: (query) => query.queryKey[0] !== "auth-session" });
    }
  };

  if (sessionQuery.isLoading) {
    return (
      <div className="flex min-h-svh items-center justify-center bg-background">
        <LoadingState label="Checking observatory session…" />
      </div>
    );
  }

  const session = sessionQuery.data;
  if (!session?.authenticated) {
    return (
      <LoginPage
        onAuthenticated={(next) => {
          qc.setQueryData(["auth-session"], next);
        }}
      />
    );
  }

  return <AuthenticatedApp session={session} onLogout={() => void handleLogout()} />;
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <AuthGate />
      </TooltipProvider>
    </QueryClientProvider>
  );
}
