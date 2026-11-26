import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight01Icon, CpuIcon, Globe02Icon, HeartCheckIcon, NetworkIcon, Notification03Icon, RamMemoryIcon, RefreshIcon, SearchRemoveIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { PortExposure, type Project } from "@/gen/asterism/v1/asterism_pb";
import { inventoryClient, topologyClient } from "@/lib/rpc";
import {
  DomainChip,
  EmptyState,
  ErrorState,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PortChip,
  SearchInput,
  SectionCard,
  SectionHeader,
  StatCard,
  StatusPill,
  categoryLabel,
  healthTone,
  type MeterSegment,
  ProjectIcon,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PortalCanvas, type PortalViewMode } from "@/components/topology/PortalCanvas";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { EventRow } from "@/components/portal/EventRow";
import { HostTelemetryCard } from "@/components/portal/HostTelemetryCard";
import { LiveSparkline } from "@/components/portal/MicroVisuals";
import { MemoryAllocationCard } from "@/components/portal/MemoryAllocationCard";
import { AgentNoticeBanner } from "@/components/agents/AgentNoticeBanner";
import { primaryAgent } from "@/components/agents/model";
import { useAgents } from "@/components/agents/useAgents";
import { useLiveValue } from "@/live/LiveStore";
import { formatBytes, formatPct } from "@/lib/format";
import { cn } from "@/lib/utils";

const MAX_KPI_PORTS = 4;
const MAX_TILE_DOMAINS = 2;
const RECENT_EVENTS = 6;
/** Project tiles on the overview; the rest are one click away on /projects. */
const MAX_TILES = 8;

function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}

export function PortalPage() {
  const qc = useQueryClient();
  const [viewMode, setViewMode] = useState<PortalViewMode>("isometric");
  const [category, setCategory] = useState<string>("all");
  const [search, setSearch] = useState("");
  const [focusedProjectId, setFocusedProjectId] = useState<string | null>(null);
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);
  const canvasRef = useRef<HTMLDivElement>(null);

  const overviewQuery = useQuery({
    queryKey: ["overview"],
    queryFn: () => inventoryClient.getOverview({}),
    refetchInterval: 15_000,
  });

  const graphQuery = useQuery({
    queryKey: ["graph", category],
    queryFn: () => topologyClient.getGraph({ category: category === "all" ? "" : category }),
    refetchInterval: 15_000,
  });

  // Only the first few public ports are drawn as chips; the total comes
  // from PageInfo, so this stays one small page however many ports exist.
  const publicPortsQuery = useQuery({
    queryKey: ["ports", "public-preview"],
    queryFn: () => inventoryClient.listPorts({ exposure: PortExposure.PUBLIC, page: { page: 1, pageSize: 10 } }),
    refetchInterval: 30_000,
  });

  const overview = overviewQuery.data;
  const agentsQuery = useAgents(overview?.capabilities?.sourceMode === "agent");
  const agent = overview?.capabilities?.sourceMode === "agent" ? primaryAgent(agentsQuery.data) : undefined;
  const graph = graphQuery.data?.graph;
  const host = overview?.host;
  const allProjects = useMemo(() => overview?.projects ?? [], [overview?.projects]);

  const liveCpu = useLiveValue("host", "cpu.util", host?.cpuUtil ?? 0);
  const liveMemUsed = useLiveValue("host", "mem.used_bytes", Number(host?.memUsedBytes ?? 0n));
  const memTotal = Number(host?.memTotalBytes ?? 0n);
  const memRatio = memTotal > 0 ? liveMemUsed / memTotal : 0;

  const totalWorkloads = allProjects.reduce((a, p) => a + p.workloads.length, 0);
  const avgHealth =
    allProjects.length > 0
      ? Math.round(allProjects.reduce((a, p) => a + p.healthScore, 0) / allProjects.length)
      : 100;
  const healthSegments: MeterSegment[] = allProjects.map((p) => ({
    value: 1 / allProjects.length,
    tone: healthTone(p.status),
    label: `${p.name}: ${p.healthScore}%`,
  }));

  const publicPorts = publicPortsQuery.data?.ports ?? [];
  const publicPortTotal = publicPortsQuery.data?.pageInfo?.totalSize ?? publicPorts.length;

  const domainsByProject = useMemo(() => {
    const map = new Map<string, { domain: string; tls: boolean }[]>();
    if (!graph) return map;
    const wlToProj = new Map<string, string>();
    for (const p of allProjects) {
      for (const w of p.workloads) wlToProj.set(w.id, p.id);
    }
    for (const r of graph.ingressRoutes) {
      for (const u of r.upstreams) {
        const pid = wlToProj.get(u.workloadId);
        if (!pid) continue;
        const list = map.get(pid) ?? [];
        for (const h of r.hostnames) {
          if (!list.some((d) => d.domain === h)) list.push({ domain: h, tls: r.tls });
        }
        map.set(pid, list);
      }
    }
    return map;
  }, [graph, allProjects]);

  const filteredProjects = useMemo(() => {
    const q = search.trim().toLowerCase();
    // Heaviest first, so the capped tile grid shows what costs the most.
    const byMemory = [...allProjects].sort((a, b) => Number(b.memUsedBytes - a.memUsedBytes) || a.name.localeCompare(b.name));
    return byMemory.filter((p) => {
      if (category !== "all" && p.category !== category) return false;
      if (!q) return true;
      const domains = domainsByProject.get(p.id) ?? [];
      return (
        p.name.toLowerCase().includes(q) ||
        domains.some((d) => d.domain.toLowerCase().includes(q)) ||
        p.workloads.some((w) => w.name.toLowerCase().includes(q))
      );
    });
  }, [allProjects, category, search, domainsByProject]);

  const showProjectDiagram = (projectId: string) => {
    setFocusedProjectId(projectId);
    canvasRef.current?.scrollIntoView({
      behavior: prefersReducedMotion() ? "auto" : "smooth",
      block: "start",
    });
  };

  const refreshing = overviewQuery.isFetching || graphQuery.isFetching;

  return (
    <Page>
      <PageHeader
        title="Overview"
        description={
          overview ? (
            <span className="tabular-nums">
              {allProjects.length} projects · {totalWorkloads} workloads · {overview.publicPortCount} public
              ports · {overview.ingressRouteCount} ingress routes
            </span>
          ) : (
            "Projects, ports and live host load at a glance"
          )
        }
        actions={
          <>
            <Button
              variant="outline"
              disabled={refreshing}
              onClick={() => {
                void qc.invalidateQueries({ queryKey: ["overview"] });
                void qc.invalidateQueries({ queryKey: ["graph"] });
                void qc.invalidateQueries({ queryKey: ["ports"] });
              }}
            >
              <Icon icon={RefreshIcon} className={cn(refreshing && "animate-spin motion-reduce:animate-none")} />
              Refresh
            </Button>
            <Button variant="outline" asChild>
              <Link to="/topology">
                Topology map
                <Icon icon={ArrowUpRight01Icon} />
              </Link>
            </Button>
            <Button variant="outline" asChild>
              <Link to="/ports">
                Port map
                <Icon icon={ArrowUpRight01Icon} />
              </Link>
            </Button>
          </>
        }
      />

      <AgentNoticeBanner agent={agent} />

      {overviewQuery.isError && !overview ? (
        <Card>
          <ErrorState
            title="Couldn't load the overview"
            error={overviewQuery.error}
            onRetry={() => void overviewQuery.refetch()}
            retrying={overviewQuery.isFetching}
          />
        </Card>
      ) : !overview ? (
        <LoadingState variant="cards" label="Loading host overview…" />
      ) : (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard
            label="Host CPU"
            icon={CpuIcon}
            value={formatPct(liveCpu)}
            sub={`load ${(host?.load1 ?? 0).toFixed(2)} · ${host?.cores ?? 0} cores`}
          >
            <LiveSparkline entityId="host" metric="cpu.util" tone="chart-1" />
          </StatCard>

          <StatCard
            label="Host memory"
            icon={RamMemoryIcon}
            value={formatBytes(liveMemUsed)}
            sub={`${Math.round(memRatio * 100)}% of ${formatBytes(memTotal)}`}
          >
            <MeterBar
              size="lg"
              value={memRatio}
              hatch
              label="Host memory used"
              valueText={`${formatBytes(liveMemUsed)} of ${formatBytes(memTotal)}`}
            />
          </StatCard>

          <StatCard
            label="Public ports"
            icon={Globe02Icon}
            value={overview.publicPortCount}
            sub={`${overview.ingressRouteCount} ingress routes`}
          >
            {publicPorts.length > 0 ? (
              <div className="flex flex-wrap items-center gap-1.5">
                {publicPorts.slice(0, MAX_KPI_PORTS).map((p) => (
                  <PortChip
                    key={p.id}
                    port={p.containerPort || p.hostPort}
                    hostPort={p.hostPort}
                    exposure={p.exposure}
                    protocol={p.proto}
                  />
                ))}
                {publicPortTotal > MAX_KPI_PORTS && (
                  <span className="text-small text-muted-foreground tabular-nums">
                    +{publicPortTotal - Math.min(MAX_KPI_PORTS, publicPorts.length)}
                  </span>
                )}
              </div>
            ) : (
              <span className="text-small text-muted-foreground">Nothing listens publicly</span>
            )}
          </StatCard>

          <StatCard
            label="Project health"
            icon={HeartCheckIcon}
            value={avgHealth}
            unit="/ 100"
            sub={
              overview.activeAlertCount > 0 ? (
                <span className="text-status-warning">
                  {overview.activeAlertCount} active {overview.activeAlertCount === 1 ? "alert" : "alerts"}
                </span>
              ) : (
                "No active alerts"
              )
            }
          >
            {healthSegments.length > 0 && (
              <MeterBar size="lg" segments={healthSegments} label="Health by project" />
            )}
          </StatCard>
        </div>
      )}

      <HostTelemetryCard
        fallbackCpu={host?.cpuUtil ?? 0}
        fallbackMemBytes={Number(host?.memUsedBytes ?? 0n)}
        memTotalBytes={memTotal}
        fallbackNetRx={host?.netRxBps ?? 0}
        agent={agent}
      />

      <div ref={canvasRef} className="scroll-mt-16">
        {graph ? (
          <PortalCanvas
            graph={graph}
            allProjects={allProjects}
            focusedProjectId={focusedProjectId}
            onFocusProject={setFocusedProjectId}
            viewMode={viewMode}
            onViewModeChange={setViewMode}
            category={category}
            onCategoryChange={setCategory}
            selectedWorkloadId={selectedWorkloadId ?? undefined}
            onSelectWorkload={setSelectedWorkloadId}
            heightClassName="h-[580px]"
          />
        ) : (
          <Card className="h-[580px] justify-center">
            {graphQuery.isError ? (
              <ErrorState
                title="Couldn't load the topology"
                error={graphQuery.error}
                onRetry={() => void graphQuery.refetch()}
                retrying={graphQuery.isFetching}
              />
            ) : (
              <LoadingState label="Mapping projects, proxies and ports…" />
            )}
          </Card>
        )}
      </div>

      <section aria-label="Projects" className="flex flex-col gap-3">
        <SectionHeader
          title="Projects"
          count={filteredProjects.length}
          description="Memory cost, services and public domains per project"
          actions={
            <>
              <SearchInput
                value={search}
                onValueChange={setSearch}
                placeholder="Search projects, services or domains"
                aria-label="Search projects, services or domains"
              />
              <Button variant="outline" asChild>
                <Link to="/projects">
                  All projects
                  <Icon icon={ArrowUpRight01Icon} />
                </Link>
              </Button>
            </>
          }
        />

        {overview && filteredProjects.length === 0 ? (
          <Card>
            <EmptyState
              size="compact"
              icon={SearchRemoveIcon}
              title="No projects match"
              description={search ? `Nothing matches "${search}" in this category.` : "No projects in this category yet."}
              action={
                search ? (
                  <Button variant="outline" onClick={() => setSearch("")}>
                    Clear search
                  </Button>
                ) : undefined
              }
            />
          </Card>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,17rem),1fr))] gap-3">
            {filteredProjects.slice(0, MAX_TILES).map((proj) => (
              <ProjectTile
                key={proj.id}
                project={proj}
                domains={domainsByProject.get(proj.id) ?? []}
                focused={focusedProjectId === proj.id}
                onShowDiagram={() => showProjectDiagram(proj.id)}
              />
            ))}
          </div>
        )}
        {filteredProjects.length > MAX_TILES && (
          <div className="flex justify-center">
            <Button variant="outline" asChild>
              <Link to="/projects" search={search.trim() ? { q: search.trim() } : {}}>
                View all {filteredProjects.length} projects
                <Icon icon={ArrowUpRight01Icon} />
              </Link>
            </Button>
          </div>
        )}
      </section>

      <div className="grid gap-3 lg:grid-cols-12">
        <MemoryAllocationCard projects={allProjects} className="lg:col-span-7" />

        <SectionCard
          title="Recent activity"
          icon={Notification03Icon}
          description="Health signals and lifecycle events"
          className="lg:col-span-5"
          flush
          actions={
            <Button variant="ghost" size="sm" asChild>
              <Link to="/events">
                View all
                <Icon icon={ArrowUpRight01Icon} />
              </Link>
            </Button>
          }
        >
          {!overview ? (
            <LoadingState variant="rows" count={4} />
          ) : overview.recentEvents.length === 0 ? (
            <EmptyState size="compact" icon={Notification03Icon} title="All quiet" description="No events recorded recently." />
          ) : (
            <div className="divide-y divide-divider">
              {overview.recentEvents.slice(0, RECENT_EVENTS).map((evt) => (
                <EventRow
                  key={evt.id}
                  event={evt}
                  variant="compact"
                  onSelectWorkload={setSelectedWorkloadId}
                />
              ))}
            </div>
          )}
        </SectionCard>
      </div>

      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}

interface ProjectTileProps {
  project: Project;
  domains: readonly { domain: string; tls: boolean }[];
  focused: boolean;
  onShowDiagram: () => void;
}

function ProjectTile({ project, domains, focused, onShowDiagram }: ProjectTileProps) {
  const shown = domains.slice(0, MAX_TILE_DOMAINS);
  const services = project.workloads.length;

  return (
    <SectionCard
      title={
        <span className="flex min-w-0 items-center gap-2">
          <ProjectIcon icon={project.icon} name={project.name} size={20} />
          <Link
            to="/projects/$projectId"
            params={{ projectId: project.name }}
            className="truncate rounded-sm outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
          >
            {project.name}
          </Link>
        </span>
      }
      description={`${categoryLabel(project.category)} · ${services} ${services === 1 ? "service" : "services"}`}
      actions={<StatusPill health={project.status}>{project.healthScore}%</StatusPill>}
      className={cn(focused && "ring-2 ring-chart-1/55")}
      bodyClassName="flex flex-col gap-4"
      footer={
        <div className="flex w-full items-center justify-end gap-1.5">
          <Button variant={focused ? "secondary" : "ghost"} size="sm" onClick={onShowDiagram}>
            <Icon icon={NetworkIcon} />
            Diagram
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/projects/$projectId" params={{ projectId: project.name }}>
              Open
              <Icon icon={ArrowUpRight01Icon} />
            </Link>
          </Button>
        </div>
      }
    >
      <div className="flex flex-col gap-2">
        <div className="flex items-baseline justify-between gap-3">
          <span className="text-small text-muted-foreground">Memory</span>
          <span className="font-mono text-mono font-semibold text-foreground tabular-nums">
            {formatBytes(project.memUsedBytes)}
          </span>
        </div>
        <MeterBar
          value={project.memSharePct / 100}
          hatch
          label={`${project.name} memory share`}
          valueText={`${project.memSharePct.toFixed(1)}% of host memory`}
        />
        <div className="flex items-baseline justify-between gap-3 text-small text-muted-foreground tabular-nums">
          <span>{project.memSharePct.toFixed(1)}% of VPS</span>
          <span className="font-mono">CPU {formatPct(project.cpuUtil)}</span>
        </div>
      </div>

      <div className="flex min-h-pill min-w-0 flex-wrap items-center gap-1.5">
        {shown.length > 0 ? (
          <>
            {shown.map((d) => (
              <DomainChip key={d.domain} domain={d.domain} tls={d.tls} className="max-w-[14rem]" />
            ))}
            {domains.length > shown.length && (
              <span className="text-small text-muted-foreground tabular-nums">+{domains.length - shown.length}</span>
            )}
          </>
        ) : (
          <span className="text-small text-text-4">No public domains</span>
        )}
      </div>
    </SectionCard>
  );
}
