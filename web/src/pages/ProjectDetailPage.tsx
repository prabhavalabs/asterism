import { useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { ArrowRight02Icon, CpuIcon, CubeIcon, FolderRemoveIcon, Globe02Icon, RamMemoryIcon, Route01Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { WorkloadRole } from "@/gen/asterism/v1/asterism_pb";
import { inventoryClient, topologyClient } from "@/lib/rpc";
import {
  CategoryBadge,
  DomainChip,
  EmptyState,
  ErrorState,
  ListRow,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  SectionCard,
  SortSelect,
  StatCard,
  ProjectIcon,
  ProxyKindLabel,
  StatusPill,
  Toolbar,
  ToolbarSpacer,
  healthStyle,
  type SortOption,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PortalCanvas, type PortalViewMode } from "@/components/topology/PortalCanvas";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { WorkloadRow } from "@/components/portal/WorkloadRow";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { formatBytes, formatPct, roleLabel } from "@/lib/format";
import { PROJECT_SERVICES_LIST } from "@/lib/routeSearch";

const BACK = { to: "/projects", label: "Projects" } as const;
const noop = () => {};

export function ProjectDetailPage() {
  const { projectId } = useParams({ from: "/projects/$projectId" });
  const [viewMode, setViewMode] = useState<PortalViewMode>("isometric");
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);

  const projectQuery = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => inventoryClient.getProject({ id: projectId }),
    refetchInterval: 15_000,
  });

  const graphQuery = useQuery({
    queryKey: ["graph", "all"],
    queryFn: () => topologyClient.getGraph({ category: "" }),
    refetchInterval: 15_000,
  });

  const project = projectQuery.data?.project;
  const graph = graphQuery.data?.graph;

  const projectRoutes = useMemo(() => {
    if (!project) return [];
    const ids = new Set(project.workloads.map((w) => w.id));
    return (graph?.ingressRoutes ?? []).filter((r) => r.upstreams.some((u) => ids.has(u.workloadId)));
  }, [graph, project]);

  const roleSummary = useMemo(() => {
    if (!project) return "";
    const counts = new Map<WorkloadRole, number>();
    for (const w of project.workloads) counts.set(w.role, (counts.get(w.role) ?? 0) + 1);
    return [...counts.entries()]
      .sort((a, b) => b[1] - a[1])
      .slice(0, 3)
      .map(([role, n]) => `${n} ${roleLabel(role).toLowerCase()}`)
      .join(" · ");
  }, [project]);

  if (!project) {
    return (
      <Page>
        <PageHeader back={BACK} title={projectId} />
        <Card>
          {projectQuery.isLoading ? (
            <LoadingState label="Loading project architecture…" />
          ) : projectQuery.isError ? (
            <ErrorState
              title="Couldn't load this project"
              error={projectQuery.error}
              onRetry={() => void projectQuery.refetch()}
              retrying={projectQuery.isFetching}
            />
          ) : (
            <EmptyState
              icon={FolderRemoveIcon}
              title="Project not found"
              description={`No project called "${projectId}" is running on this host.`}
            />
          )}
        </Card>
      </Page>
    );
  }

  const domainCount = new Set(projectRoutes.flatMap((r) => r.hostnames)).size;

  return (
    <Page>
      <PageHeader
        back={BACK}
        title={
          <span className="flex min-w-0 items-center gap-3">
            <ProjectIcon icon={project.icon} name={project.name} size={32} />
            <span className="min-w-0 break-words">{project.name}</span>
          </span>
        }
        meta={
          <>
            <CategoryBadge category={project.category} />
            <StatusPill health={project.status}>
              {healthStyle(project.status).label} {project.healthScore}%
            </StatusPill>
          </>
        }
        description={project.workingDir ? <span className="font-mono text-mono">{project.workingDir}</span> : undefined}
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          label="Memory cost"
          icon={RamMemoryIcon}
          value={formatBytes(project.memUsedBytes)}
          sub={`${project.memSharePct.toFixed(1)}% of VPS memory`}
        >
          <MeterBar size="lg" hatch value={project.memSharePct / 100} label="Share of VPS memory" />
        </StatCard>
        <StatCard label="CPU" icon={CpuIcon} value={formatPct(project.cpuUtil)} sub="of host CPU">
          <MeterBar size="lg" value={project.cpuUtil} label="CPU" />
        </StatCard>
        <StatCard label="Services" icon={CubeIcon} value={project.workloads.length} mono={false} sub={roleSummary || "—"} />
        <StatCard
          label="Public domains"
          icon={Globe02Icon}
          value={domainCount}
          mono={false}
          sub={`${projectRoutes.length} ingress ${projectRoutes.length === 1 ? "route" : "routes"}`}
        />
      </div>

      {graph ? (
        <PortalCanvas
          graph={graph}
          allProjects={graph.projects}
          focusedProjectId={project.id}
          onFocusProject={noop}
          viewMode={viewMode}
          onViewModeChange={setViewMode}
          category="all"
          selectedWorkloadId={selectedWorkloadId ?? undefined}
          onSelectWorkload={setSelectedWorkloadId}
          lockToProject
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
            <LoadingState label="Mapping services and connections…" />
          )}
        </Card>
      )}

      <div className="grid items-start gap-3 lg:grid-cols-12">
        <ServicesCard projectId={project.id} total={project.workloads.length} onSelect={setSelectedWorkloadId} />

        <SectionCard
          title="Domains and routing"
          icon={Route01Icon}
          description="Public hostnames proxied into this project"
          className="lg:col-span-5"
          flush
        >
          {projectRoutes.length === 0 ? (
            <EmptyState
              size="compact"
              icon={Globe02Icon}
              title="No public routes"
              description="No reverse proxy forwards traffic to this project."
            />
          ) : (
            <div className="divide-y divide-divider">
              {projectRoutes.map((r) => (
                <div key={r.id} className="flex flex-col gap-2.5 px-4 py-3.5">
                  <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                    {r.hostnames.map((h) => (
                      <DomainChip key={h} domain={h} tls={r.tls} />
                    ))}
                    <Badge variant="outline">
                      <ProxyKindLabel kind={r.proxyKind} />
                    </Badge>
                  </div>
                  <div className="flex flex-col gap-1">
                    {r.upstreams.map((u, i) => (
                      <ListRow
                        key={`${u.address}-${i}`}
                        className="min-h-9 rounded-tile bg-surface-subtle px-3 py-1"
                        leading={<Icon icon={ArrowRight02Icon} className="size-4 text-text-4" />}
                        trailing={
                          <>
                            {u.weight > 1 && (
                              <Badge variant="secondary" mono>
                                w{u.weight}
                              </Badge>
                            )}
                            <StatusPill tone={u.healthy ? "healthy" : "critical"}>{u.healthy ? "Up" : "Down"}</StatusPill>
                          </>
                        }
                      >
                        <span className="min-w-0 flex-1 truncate font-mono text-mono text-foreground">{u.address}</span>
                      </ListRow>
                    ))}
                  </div>
                  <p className="font-mono text-small text-muted-foreground">
                    :{r.listenPort}
                    {r.pathPrefix && r.pathPrefix !== "/" ? ` · ${r.pathPrefix}` : ""} · {r.strategy}
                  </p>
                </div>
              ))}
            </div>
          )}
        </SectionCard>
      </div>

      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}

const SERVICE_SORTS: readonly SortOption[] = [
  { value: "name", label: "Name" },
  { value: "role", label: "Role" },
  { value: "state", label: "State" },
  { value: "memory desc", label: "Memory, highest" },
  { value: "cpu desc", label: "CPU, highest" },
  { value: "restarts desc", label: "Restarts, most" },
];

/** The project's services, one server page of ListWorkloads at a time. */
function ServicesCard({ projectId, total, onSelect }: { projectId: string; total: number; onSelect: (id: string) => void }) {
  const paged = usePagedSearch(PROJECT_SERVICES_LIST);
  const query = useQuery({
    queryKey: paged.queryKey(["workloads", "page", projectId]),
    queryFn: () => inventoryClient.listWorkloads({ ...paged.request, projectId }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const workloads = query.data?.workloads ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "services");

  return (
    <SectionCard
      title="Services"
      icon={CubeIcon}
      description="Select a service for live charts, ports and redacted logs"
      className="lg:col-span-7"
      flush
    >
      {total > PROJECT_SERVICES_LIST.defaultPageSize && (
        <Toolbar size="sm" className="border-b border-divider px-4 py-2">
          <PagedSearchInput
            size="sm"
            value={paged.query}
            onCommit={paged.setQuery}
            placeholder="Search services"
            aria-label="Search services"
            containerClassName="w-full sm:w-60"
          />
          <ToolbarSpacer />
          <SortSelect size="sm" value={paged.order} onValueChange={paged.setOrder} options={SERVICE_SORTS} />
        </Toolbar>
      )}
      <PaginatedScrollArea
        status={query}
        isEmpty={workloads.length === 0}
        orientation="vertical"
        aria-label="Services"
        pagination={total > PROJECT_SERVICES_LIST.defaultPageSize ? pager : undefined}
        empty={
          <EmptyState
            size="compact"
            icon={CubeIcon}
            title={paged.query ? "No services match" : "No services"}
            description={paged.query ? `Nothing matches "${paged.query}".` : undefined}
            action={
              paged.query ? (
                <Button variant="outline" onClick={paged.reset}>
                  Clear search
                </Button>
              ) : undefined
            }
          />
        }
      >
        <div className="divide-y divide-divider">
          {workloads.map((w) => (
            <WorkloadRow key={w.id} workload={w} onSelect={onSelect} showImage alwaysShowHealth />
          ))}
        </div>
      </PaginatedScrollArea>
    </SectionCard>
  );
}
