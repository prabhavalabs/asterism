import { useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowRight02Icon, Globe02Icon, NetworkIcon, Route01Icon, SearchRemoveIcon, Shield01Icon, SquareLock02Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { PortExposure, type IngressRoute, type PortBinding, type PortExposureCounts } from "@/gen/asterism/v1/asterism_pb";
import { inventoryClient } from "@/lib/rpc";
import {
  ChipScroller,
  CountChip,
  DataTableFrame,
  DomainChip,
  EmptyState,
  ErrorState,
  ListRow,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  PortChip,
  ProxyKindLabel,
  SectionCard,
  SegmentedControl,
  SortHeader,
  SortSelect,
  StatusPill,
  Toolbar,
  ToolbarSpacer,
  dataTableClasses,
  projectNameFromId,
  type ExposureKind,
  type SortOption,
  type StatusTone,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import {
  EXPOSURE_FILTERS,
  INGRESS_LIST,
  INGRESS_VIEWS,
  PORTS_LIST,
  PORTS_TABS,
  PORTS_TAB_LABELS,
  type PortsSearch,
  type PortsTab,
} from "@/lib/routeSearch";
import { cn } from "@/lib/utils";

type ExposureFilter = (typeof EXPOSURE_FILTERS)[number];

const EXPOSURE_OF: Record<ExposureFilter, PortExposure> = {
  all: PortExposure.UNSPECIFIED,
  public: PortExposure.PUBLIC,
  loopback: PortExposure.LOOPBACK,
  internal: PortExposure.INTERNAL,
};

const EXPOSURE_META: Record<ExposureKind, { label: string; hint: string; bar: string; dot: string }> = {
  public: {
    label: "Public",
    hint: "Reachable from the internet",
    bar: "bg-exposure-public",
    dot: "bg-exposure-public",
  },
  loopback: {
    label: "Loopback",
    hint: "Bound to 127.0.0.1, host only",
    bar: "bg-exposure-loopback",
    dot: "bg-exposure-loopback",
  },
  internal: {
    label: "Internal",
    hint: "Container network only, not published",
    bar: "bg-exposure-internal",
    dot: "bg-exposure-internal",
  },
};

const KINDS: readonly ExposureKind[] = ["public", "loopback", "internal"];

const PORT_SORTS: readonly SortOption[] = [
  { value: "exposure, port", label: "Exposure" },
  { value: "port", label: "Port" },
  { value: "workload", label: "Workload" },
  { value: "project", label: "Project" },
  { value: "proto", label: "Protocol" },
];

const ROUTE_SORTS: readonly SortOption[] = [
  { value: "hostname", label: "Hostname" },
  { value: "port", label: "Listen port" },
  { value: "proxy", label: "Proxy" },
  { value: "upstreams desc", label: "Most upstreams" },
];

function isPortsTab(value: string): value is PortsTab {
  return (PORTS_TABS as readonly string[]).includes(value);
}

export function PortsIngressPage() {
  // Listening ports and ingress are two URL-driven tabs (?tab=ingress), so
  // the sidebar's "Ports" and "Ingress & domains" each land on their own.
  const params = useSearch({ strict: false }) as PortsSearch;
  const navigate = useNavigate();
  const tab: PortsTab = params.tab ?? "ports";
  const setTab = (next: string) => {
    if (!isPortsTab(next) || next === tab) return;
    void navigate({ to: "/ports", search: next === "ingress" ? { tab: "ingress" } : {} });
  };
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);

  // Host-wide totals for the tab chips and the exposure surface; the lists
  // below fetch their own pages.
  const portsSummary = useQuery({
    queryKey: ["ports", "summary"],
    queryFn: () => inventoryClient.listPorts({ page: { page: 1, pageSize: 10 } }),
    refetchInterval: 15_000,
  });
  const ingressSummary = useQuery({
    queryKey: ["ingress", "summary"],
    queryFn: () => inventoryClient.listIngress({ page: { page: 1, pageSize: 10 } }),
    refetchInterval: 30_000,
  });

  return (
    <Page>
      <PageHeader
        title="Ports & Ingress"
        description="Every listening port by exposure, and how reverse proxies route public traffic."
      />

      <Tabs value={tab} onValueChange={setTab} className="gap-5">
        <ChipScroller>
          <TabsList variant="line" className="w-max">
            <TabsTrigger value="ports">
              {PORTS_TAB_LABELS.ports}
              {portsSummary.data && <CountChip>{portsSummary.data.pageInfo?.totalSize ?? 0}</CountChip>}
            </TabsTrigger>
            <TabsTrigger value="ingress">
              {PORTS_TAB_LABELS.ingress}
              {ingressSummary.data && <CountChip>{ingressSummary.data.pageInfo?.totalSize ?? 0}</CountChip>}
            </TabsTrigger>
          </TabsList>
        </ChipScroller>

        <TabsContent value="ports" className="flex flex-col gap-5">
          {portsSummary.isError && !portsSummary.data ? (
            <Card>
              <ErrorState
                title="Couldn't load ports"
                error={portsSummary.error}
                onRetry={() => void portsSummary.refetch()}
                retrying={portsSummary.isFetching}
              />
            </Card>
          ) : (
            <>
              <ExposureSurface counts={portsSummary.data?.exposureCounts} />
              <PortsList onSelectWorkload={setSelectedWorkloadId} />
            </>
          )}
        </TabsContent>

        <TabsContent value="ingress" className="flex flex-col gap-5">
          <IngressList />
        </TabsContent>
      </Tabs>

      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}

function ExposureSurface({ counts }: { counts?: PortExposureCounts }) {
  const c: Record<ExposureKind, number> = {
    public: counts?.public ?? 0,
    loopback: counts?.loopback ?? 0,
    internal: counts?.internal ?? 0,
  };
  const all = counts?.total ?? 0;
  const total = Math.max(1, all);
  return (
    <SectionCard
      title="Exposure surface"
      icon={Shield01Icon}
      description="Public internet, loopback and container-only listeners"
      actions={
        <span className="font-mono text-mono text-muted-foreground tabular-nums">
          <span className="font-semibold text-foreground">{all}</span> listeners
        </span>
      }
      bodyClassName="flex flex-col gap-5"
    >
      <div
        role="img"
        aria-label={`${c.public} public, ${c.loopback} loopback, ${c.internal} internal ports`}
        className="flex h-3 w-full gap-1 overflow-hidden rounded-bar"
      >
        {all === 0 ? (
          <span className="h-full flex-1 bg-meter-track" />
        ) : (
          KINDS.filter((k) => c[k] > 0).map((k) => (
            <span key={k} className={cn("hatch h-full", EXPOSURE_META[k].bar)} style={{ width: `${(c[k] / total) * 100}%` }} />
          ))
        )}
      </div>
      <dl className="grid gap-3 sm:grid-cols-3">
        {KINDS.map((k) => (
          <div key={k} className="flex flex-col gap-1 rounded-tile bg-surface-subtle p-4 ring-1 ring-inset ring-border">
            <dt className="flex items-center gap-2 text-small font-medium text-text-2">
              <span aria-hidden="true" className={cn("size-2.5 rounded-xs", EXPOSURE_META[k].dot)} />
              {EXPOSURE_META[k].label}
            </dt>
            <dd className="flex items-baseline gap-2">
              <span className="font-heading text-kpi tracking-[-0.02em] text-foreground tabular-nums">{c[k]}</span>
              <span className="text-small text-muted-foreground">{EXPOSURE_META[k].hint}</span>
            </dd>
          </div>
        ))}
      </dl>
    </SectionCard>
  );
}

/** Listening ports, one server page of ListPorts at a time. */
function PortsList({ onSelectWorkload }: { onSelectWorkload: (id: string) => void }) {
  const paged = usePagedSearch(PORTS_LIST);
  const filter = paged.filters.exposure as ExposureFilter;
  const query = useQuery({
    queryKey: paged.queryKey(["ports", "page"]),
    queryFn: () => inventoryClient.listPorts({ ...paged.request, exposure: EXPOSURE_OF[filter] }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const ports = query.data?.ports ?? [];
  const counts = query.data?.exposureCounts;
  const pager = usePager(paged, query.data?.pageInfo, "ports");
  const sort = { order: paged.order, onOrderChange: paged.setOrder };

  const filterOptions = [
    { value: "all" as const, label: "All", count: counts?.total ?? 0 },
    ...KINDS.map((k) => ({ value: k, label: EXPOSURE_META[k].label, count: counts?.[k] ?? 0 })),
  ];

  return (
    <SectionCard title="Listening ports" icon={NetworkIcon} description="Bound addresses, published mappings and owning workloads" flush>
      <Toolbar className="border-b border-divider px-4 py-3">
        <ChipScroller>
          <SegmentedControl
            aria-label="Exposure"
            className="max-w-none"
            value={filter}
            onValueChange={(value) => paged.setFilter("exposure", value)}
            options={filterOptions}
          />
        </ChipScroller>
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={PORT_SORTS} className="md:hidden" />
        <PagedSearchInput
          value={paged.query}
          onCommit={paged.setQuery}
          placeholder="Filter by port, workload or process"
          aria-label="Filter ports"
        />
      </Toolbar>
      <DataTableFrame
        status={query}
        isEmpty={ports.length === 0}
        empty={
          <EmptyState
            size="compact"
            icon={SearchRemoveIcon}
            title="No ports match"
            description={paged.query ? `Nothing matches "${paged.query}".` : "No listeners with this exposure."}
            action={
              paged.query || filter !== "all" ? (
                <Button variant="outline" onClick={paged.reset}>
                  Clear filters
                </Button>
              ) : undefined
            }
          />
        }
        minWidthClassName="min-w-[760px]"
        tableClassName="whitespace-nowrap"
        aria-label="Listening ports"
        pagination={pager}
      >
        <thead className={dataTableClasses.thead}>
          <tr>
            <SortHeader label="Port" field="port" {...sort} className="w-44 pl-4" />
            <th scope="col" className={cn(th, "w-44")}>Bind address</th>
            <SortHeader label="Workload" field="workload" {...sort} />
            <SortHeader label="Project" field="project" {...sort} className="w-40" />
            <th scope="col" className={cn(th, "w-36")}>Process</th>
            <SortHeader label="Protocol" field="proto" {...sort} align="right" className="w-28 pr-4" />
          </tr>
        </thead>
        <tbody>
          {ports.map((p) => (
            <PortRow key={p.id} port={p} onSelectWorkload={onSelectWorkload} />
          ))}
        </tbody>
      </DataTableFrame>
    </SectionCard>
  );
}

const th = dataTableClasses.th;
const td = dataTableClasses.td;

function PortRow({ port: p, onSelectWorkload }: { port: PortBinding; onSelectWorkload: (id: string) => void }) {
  const project = projectNameFromId(p.projectId);
  const published = p.hostPort > 0;
  return (
    <tr className={dataTableClasses.tr}>
      <td className={cn(td, "pl-4")}>
        <span className="flex items-center gap-2">
          <PortChip port={p.containerPort || p.hostPort} hostPort={p.hostPort} exposure={p.exposure} protocol={p.proto} />
          {!published && <span className="text-small text-muted-foreground">not published</span>}
        </span>
      </td>
      <td className={cn(td, "font-mono text-mono text-text-2 tabular-nums")}>{published ? p.hostIp || "0.0.0.0" : "container network"}</td>
      <td className={cn(td, "max-w-0")}>
        {p.workloadId.startsWith("ctr:") ? (
          <Link
            to="/containers/$containerId"
            params={{ containerId: p.workloadId.slice(4) }}
            className="block truncate rounded-sm font-medium text-foreground outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
          >
            {p.workloadName || p.workloadId}
          </Link>
        ) : p.workloadId ? (
          <button
            type="button"
            onClick={() => onSelectWorkload(p.workloadId)}
            className="block max-w-full truncate rounded-sm font-medium text-foreground outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
          >
            {p.workloadName || p.workloadId}
          </button>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className={cn(td, "max-w-0")}>
        {project ? (
          <Link
            to="/projects/$projectId"
            params={{ projectId: project }}
            className="block truncate rounded-sm text-text-2 outline-none hover:text-foreground hover:underline focus-visible:ring-1 focus-visible:ring-ring"
          >
            {project}
          </Link>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className={cn(td, "max-w-0 truncate font-mono text-mono text-text-2")}>{p.processName || "—"}</td>
      <td className={cn(td, "pr-4 text-right font-mono text-mono text-muted-foreground uppercase")}>{p.proto}</td>
    </tr>
  );
}

function upstreamTone(route: IngressRoute): StatusTone {
  const total = route.upstreams.length;
  const healthy = route.upstreams.filter((u) => u.healthy).length;
  if (total === 0) return "neutral";
  if (healthy === total) return "healthy";
  return healthy === 0 ? "critical" : "warning";
}

/**
 * Ingress routes, one server page of ListIngress at a time, as a domains
 * table (one row per route, its hostnames as chips) or as route cards with
 * every upstream.
 */
function IngressList() {
  const paged = usePagedSearch(INGRESS_LIST);
  const view = paged.filters.view as (typeof INGRESS_VIEWS)[number];
  const query = useQuery({
    queryKey: paged.queryKey(["ingress", "page"]),
    queryFn: () => inventoryClient.listIngress(paged.request),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
  const routes = query.data?.routes ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "routes");
  const sort = { order: paged.order, onOrderChange: paged.setOrder };
  const hostnames = routes.reduce((n, r) => n + r.hostnames.length, 0);

  const empty =
    pager.totalSize === 0 && !paged.query ? (
      <EmptyState
        size="compact"
        icon={Globe02Icon}
        title="No ingress routes found"
        description="Asterism reads Nginx, Caddy and HAProxy configs, Traefik labels and cloudflared tunnels."
      />
    ) : (
      <EmptyState
        size="compact"
        icon={SearchRemoveIcon}
        title="No routes match"
        description={`Nothing matches "${paged.query}".`}
        action={
          <Button variant="outline" onClick={() => paged.setQuery("")}>
            Clear search
          </Button>
        }
      />
    );

  return (
    <>
      <Toolbar>
        <SegmentedControl
          aria-label="Layout"
          value={view}
          onValueChange={(value) => paged.setFilter("view", value)}
          options={[
            { value: "table", label: "Domains" },
            { value: "cards", label: "Route cards" },
          ]}
        />
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={ROUTE_SORTS} className={cn(view === "table" && "md:hidden")} />
        <PagedSearchInput
          value={paged.query}
          onCommit={paged.setQuery}
          placeholder="Search hostnames, proxies or upstreams"
          aria-label="Search ingress routes"
        />
      </Toolbar>

      <SectionCard
        title={view === "table" ? "Domains" : "Ingress routes"}
        icon={view === "table" ? Globe02Icon : Route01Icon}
        description={
          query.data ? (
            <span className="tabular-nums">
              {pager.totalSize} routes · {hostnames} hostnames on this page · Nginx, Caddy, HAProxy, Traefik and Cloudflare Tunnel
            </span>
          ) : undefined
        }
        flush
      >
        {view === "table" ? (
          <DataTableFrame
            status={query}
            isEmpty={routes.length === 0}
            empty={empty}
            minWidthClassName="min-w-[820px]"
            tableClassName="whitespace-nowrap"
            aria-label="Domains"
            pagination={pager}
          >
            <thead className={dataTableClasses.thead}>
              <tr>
                <SortHeader label="Domain" field="hostname" {...sort} className="pl-4" />
                <SortHeader label="Proxy" field="proxy" {...sort} className="w-36" />
                <SortHeader label="Listen" field="port" {...sort} className="w-24" />
                <th scope="col" className={cn(th, "w-32")}>Strategy</th>
                <SortHeader label="Upstreams" field="upstreams" {...sort} defaultDesc align="right" className="w-32 pr-4" />
              </tr>
            </thead>
            <tbody>
              {routes.map((r) => (
                <DomainRow key={r.id} route={r} />
              ))}
            </tbody>
          </DataTableFrame>
        ) : (
          <PaginatedScrollArea status={query} isEmpty={routes.length === 0} empty={empty} orientation="vertical" aria-label="Ingress routes" pagination={pager}>
            <div className="grid gap-3 p-3 md:grid-cols-2 xl:grid-cols-3">
              {routes.map((route) => (
                <RouteCard key={route.id} route={route} />
              ))}
            </div>
          </PaginatedScrollArea>
        )}
      </SectionCard>
    </>
  );
}

function DomainRow({ route: r }: { route: IngressRoute }) {
  const [primary, ...aliases] = r.hostnames;
  const healthy = r.upstreams.filter((u) => u.healthy).length;
  const path = r.pathPrefix && r.pathPrefix !== "/" ? r.pathPrefix : "";
  return (
    <tr className={dataTableClasses.tr}>
      <td className={cn(td, "max-w-0 pl-4")}>
        <span className="flex min-w-0 items-center gap-2">
          <DomainChip domain={primary || r.id} tls={r.tls} className="max-w-[22rem]" />
          {path && <span className="font-mono text-mono text-muted-foreground">{path}</span>}
          {aliases.length > 0 && (
            <span className="truncate text-small text-muted-foreground tabular-nums" title={aliases.join(", ")}>
              +{aliases.length} {aliases.length === 1 ? "alias" : "aliases"}
            </span>
          )}
        </span>
      </td>
      <td className={cn(td, "text-text-2")}>
        <ProxyKindLabel kind={r.proxyKind} />
      </td>
      <td className={cn(td, "font-mono text-mono text-text-2 tabular-nums")}>:{r.listenPort}</td>
      <td className={cn(td, "font-mono text-mono text-text-2")}>{r.strategy || "—"}</td>
      <td className={cn(td, "pr-4 text-right")}>
        <StatusPill tone={upstreamTone(r)}>
          <span className="tabular-nums">
            {healthy}/{r.upstreams.length} up
          </span>
        </StatusPill>
      </td>
    </tr>
  );
}

function RouteCard({ route }: { route: IngressRoute }) {
  const [primary, ...aliases] = route.hostnames;
  const healthy = route.upstreams.filter((u) => u.healthy).length;
  const path = route.pathPrefix && route.pathPrefix !== "/" ? ` · ${route.pathPrefix}` : "";

  return (
    <SectionCard
      title={<span className="font-mono">{primary || route.id}</span>}
      icon={route.tls ? SquareLock02Icon : Globe02Icon}
      description={
        <span className="font-mono tabular-nums">
          :{route.listenPort}
          {path} · {route.strategy}
        </span>
      }
      actions={
        <Badge variant="outline">
          <ProxyKindLabel kind={route.proxyKind} />
        </Badge>
      }
      flush
      footer={
        <span className="tabular-nums">
          {healthy} of {route.upstreams.length} upstreams healthy
        </span>
      }
    >
      {aliases.length > 0 && (
        <div className="flex flex-wrap gap-1.5 border-b border-divider px-4 py-3">
          {aliases.map((h) => (
            <DomainChip key={h} domain={h} tls={route.tls} />
          ))}
        </div>
      )}
      <div className="divide-y divide-divider">
        {route.upstreams.map((u, idx) => (
          <ListRow
            key={`${u.address}-${idx}`}
            leading={<Icon icon={ArrowRight02Icon} className="size-4 text-text-4" />}
            trailing={
              <>
                {u.weight > 1 && (
                  <Badge variant="secondary" mono>
                    w{u.weight}
                  </Badge>
                )}
                {u.backup && <Badge variant="outline">Backup</Badge>}
                <StatusPill tone={u.healthy ? "healthy" : "critical"}>{u.healthy ? "Up" : "Down"}</StatusPill>
              </>
            }
          >
            <span className="min-w-0 flex-1 truncate font-mono text-mono text-foreground" title={u.address}>
              {u.address}
            </span>
          </ListRow>
        ))}
      </div>
    </SectionCard>
  );
}
