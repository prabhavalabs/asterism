import { useEffect, useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  Alert02Icon,
  ArrowDown01Icon,
  ContainerIcon,
  HardDriveIcon,
  Layers01Icon,
  RamMemoryIcon,
  SearchRemoveIcon,
  SecurityCheckIcon,
  Tick02Icon,
} from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  ChipScroller,
  CountChip,
  EmptyState,
  ErrorState,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  ProjectIcon,
  SectionCard,
  SegmentedControl,
  SortSelect,
  StatCard,
  type SortOption,
  Toolbar,
  ToolbarGroup,
  ToolbarSpacer,
  projectNameFromId,
} from "@/components/common";
import { ContainerCardList, ContainersTable } from "@/components/containers/ContainersTable";
import { DiskUsageTab, ImagesTab, NetworksTab, VolumesTab } from "@/components/containers/DockerResourceTabs";
import { CONTAINER_METRICS, seedContainerHistory } from "@/components/containers/history";
import { STATE_FILTER_STATES, containerKeys } from "@/components/containers/model";
import { useContainerActions, type ContainerActionsApi } from "@/components/containers/useContainerActions";
import { useMediaQuery } from "@/components/topology/useMediaQuery";
import { ContainerState, type Project } from "@/gen/asterism/v1/asterism_pb";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { dockerClient, inventoryClient } from "@/lib/rpc";
import { formatBytes } from "@/lib/format";
import {
  CONTAINERS_DEFAULT_ORDER,
  CONTAINERS_LIST,
  CONTAINERS_TABS,
  type ContainerStateFilter,
  type ContainersSearch,
  type ContainersTab,
} from "@/lib/routeSearch";

const SPARK_METRICS = CONTAINER_METRICS.slice(0, 2);
const SPARK_WINDOW_MS = 5 * 60_000;
const MAX_SEEDED = 50;

const SORT_OPTIONS: readonly SortOption[] = [
  { value: CONTAINERS_DEFAULT_ORDER, label: "Project (grouped)" },
  { value: "name", label: "Name" },
  { value: "state", label: "State" },
  { value: "cpu desc", label: "CPU, highest" },
  { value: "memory desc", label: "Memory, highest" },
  { value: "uptime desc", label: "Uptime, longest" },
  { value: "restarts desc", label: "Restarts, most" },
  { value: "image", label: "Image" },
  { value: "created desc", label: "Newest" },
];

function isContainersTab(value: string): value is ContainersTab {
  return (CONTAINERS_TABS as readonly string[]).includes(value);
}

export function ContainersPage() {
  // The tab lives in the URL (?tab=images) so sidebar links, back and
  // forward all land on the right tab.
  const params = useSearch({ strict: false }) as ContainersSearch;
  const navigate = useNavigate();
  const tab: ContainersTab = params.tab ?? "containers";
  const setTab = (next: string) => {
    if (!isContainersTab(next) || next === tab) return;
    void navigate({ to: "/containers", search: next === "containers" ? {} : { tab: next } });
  };

  // Host-wide numbers for the KPI tiles, independent of the table's filters.
  const summaryQuery = useQuery({
    queryKey: containerKeys.summary(),
    queryFn: () => dockerClient.listContainers({ page: { page: 1, pageSize: 10 } }),
    refetchInterval: 10_000,
  });
  const projectsQuery = useQuery({
    queryKey: ["projects", "all"],
    queryFn: () => inventoryClient.listProjects({ category: "" }),
    refetchInterval: 15_000,
  });
  const diskQuery = useQuery({
    queryKey: containerKeys.disk(),
    queryFn: () => dockerClient.getDiskUsage({}),
    refetchInterval: 60_000,
  });

  const summary = summaryQuery.data;
  const capabilities = summary?.capabilities;
  const actions = useContainerActions(capabilities);

  const projects = useMemo(() => {
    const map = new Map<string, Project>();
    for (const p of projectsQuery.data?.projects ?? []) map.set(p.id, p);
    return map;
  }, [projectsQuery.data?.projects]);

  const counts = summary?.counts;
  const total = counts?.total ?? 0;
  const running = counts?.running ?? 0;
  const stopped = (counts?.exited ?? 0) + (counts?.created ?? 0) + (counts?.dead ?? 0);
  const attention = counts?.attention ?? 0;
  const totalMem = Number(summary?.memUsedBytes ?? 0n);
  const hostMem = Number(summary?.engine?.memTotalBytes ?? 0n);
  const usage = diskQuery.data?.usage;
  const diskTotal = Number(usage?.totalSizeBytes ?? 0n);
  const diskReclaim = Number(usage?.totalReclaimableBytes ?? 0n);

  const engine = summary?.engine;
  const unavailable = summary && engine && !engine.available && total === 0;

  return (
    <Page>
      <PageHeader
        title="Containers"
        description="Every Docker container on this host, running or stopped, with live usage and lifecycle controls."
        meta={
          capabilities ? (
            capabilities.canPerformActions ? (
              <Badge variant="healthy">
                <Icon icon={Tick02Icon} />
                Actions enabled
              </Badge>
            ) : (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Badge variant="outline" tabIndex={0} className="cursor-default">
                    <Icon icon={SecurityCheckIcon} className="text-status-healthy" />
                    Read-only
                  </Badge>
                </TooltipTrigger>
                <TooltipContent className="max-w-80">{capabilities.actionsDisabledReason}</TooltipContent>
              </Tooltip>
            )
          ) : undefined
        }
        actions={
          engine?.serverVersion ? (
            <Badge variant="secondary" mono>
              Docker {engine.serverVersion}
            </Badge>
          ) : undefined
        }
      />

      {summaryQuery.isError && !summary ? (
        <Card>
          <ErrorState
            title="Couldn't load containers"
            error={summaryQuery.error}
            onRetry={() => void summaryQuery.refetch()}
            retrying={summaryQuery.isFetching}
          />
        </Card>
      ) : !summary ? (
        <>
          <LoadingState variant="cards" label="Loading containers…" />
          <Card>
            <LoadingState variant="rows" count={8} />
          </Card>
        </>
      ) : unavailable ? (
        <Card>
          <EmptyState icon={ContainerIcon} title="Docker inventory is not available" description={engine?.unavailableReason} />
        </Card>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <StatCard
              label="Running"
              icon={ContainerIcon}
              value={
                <>
                  {running}
                  <span className="text-muted-foreground">/{total}</span>
                </>
              }
              sub={`${counts?.paused ?? 0} paused · ${stopped} stopped`}
            />
            <StatCard
              label="Needs attention"
              icon={Alert02Icon}
              tone={attention > 0 ? "warning" : "healthy"}
              value={attention}
              sub={`${counts?.unhealthy ?? 0} unhealthy · ${counts?.restarting ?? 0} restarting · ${counts?.dead ?? 0} dead`}
            />
            <StatCard
              label="Container memory"
              icon={RamMemoryIcon}
              value={formatBytes(totalMem)}
              sub={hostMem > 0 ? `${((totalMem / hostMem) * 100).toFixed(1)}% of ${formatBytes(hostMem)}` : `across ${running} running`}
            >
              {hostMem > 0 && <MeterBar size="lg" hatch value={totalMem / hostMem} label="Container memory share of host" />}
            </StatCard>
            <StatCard
              label="Docker disk"
              icon={HardDriveIcon}
              value={usage ? formatBytes(diskTotal) : "—"}
              sub={usage ? `${formatBytes(diskReclaim)} reclaimable` : diskQuery.isError ? "Unavailable" : "Measuring…"}
            >
              {usage && diskTotal > 0 && (
                <MeterBar
                  size="lg"
                  segments={[
                    { value: (diskTotal - diskReclaim) / diskTotal, tone: "chart-1", label: "In use" },
                    { value: diskReclaim / diskTotal, tone: "warning", label: "Reclaimable" },
                  ]}
                  label="Docker disk usage"
                  valueText={`${formatBytes(diskTotal)} used, ${formatBytes(diskReclaim)} reclaimable`}
                />
              )}
            </StatCard>
          </div>

          <Tabs value={tab} onValueChange={setTab} className="gap-3">
            <ChipScroller>
              <TabsList variant="line" className="w-max">
                <TabsTrigger value="containers">
                  Containers <CountChip>{total}</CountChip>
                </TabsTrigger>
                <TabsTrigger value="images">Images</TabsTrigger>
                <TabsTrigger value="volumes">Volumes</TabsTrigger>
                <TabsTrigger value="networks">Networks</TabsTrigger>
                <TabsTrigger value="disk">Disk usage</TabsTrigger>
              </TabsList>
            </ChipScroller>

            <TabsContent value="containers" className="flex flex-col gap-3">
              <ContainersList projects={projects} actions={actions} hostTotal={total} />
            </TabsContent>
            <TabsContent value="images">
              <ImagesTab />
            </TabsContent>
            <TabsContent value="volumes">
              <VolumesTab />
            </TabsContent>
            <TabsContent value="networks">
              <NetworksTab />
            </TabsContent>
            <TabsContent value="disk">
              <DiskUsageTab />
            </TabsContent>
          </Tabs>
        </>
      )}
      {actions.dialog}
    </Page>
  );
}

interface ContainersListProps {
  projects: ReadonlyMap<string, Project>;
  actions: ContainerActionsApi;
  /** Every container on the host, for the "N of M" line and the empty state. */
  hostTotal: number;
}

/**
 * The Containers tab: state chips, project filter, sort and search in the
 * URL, one server page of ListContainers at a time. The default order
 * (project, name) groups rows under project headers; any other order is flat.
 */
function ContainersList({ projects, actions, hostTotal }: ContainersListProps) {
  const paged = usePagedSearch(CONTAINERS_LIST);
  const stateFilter = paged.filters.state as ContainerStateFilter;
  const projectFilter = paged.filters.project;
  const grouped = paged.order === CONTAINERS_DEFAULT_ORDER;
  const desktop = useMediaQuery("(min-width: 48rem)");

  const listQuery = useQuery({
    queryKey: paged.queryKey(containerKeys.list()),
    queryFn: () =>
      dockerClient.listContainers({
        ...paged.request,
        projectId: projectFilter,
        states: STATE_FILTER_STATES[stateFilter],
        attentionOnly: stateFilter === "attention",
      }),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });
  const data = listQuery.data;
  const rows = useMemo(() => data?.containers ?? [], [data?.containers]);
  const pager = usePager(paged, data?.pageInfo, "containers");

  // Backfill sparklines for the running containers on this page only.
  const runningKey = useMemo(
    () =>
      rows
        .filter((c) => c.state === ContainerState.RUNNING)
        .map((c) => c.id)
        .slice(0, MAX_SEEDED)
        .join(","),
    [rows],
  );
  useEffect(() => {
    if (!runningKey) return;
    return seedContainerHistory(runningKey.split(","), SPARK_METRICS, SPARK_WINDOW_MS);
  }, [runningKey]);

  // Chip counts are facets: they follow the project filter and the search,
  // never the state chip itself.
  const facet = data?.counts;
  const stateOptions = [
    { value: "all" as const, label: "All", count: facet?.total ?? 0 },
    { value: "running" as const, label: "Running", count: (facet?.running ?? 0) + (facet?.paused ?? 0) + (facet?.restarting ?? 0) },
    { value: "stopped" as const, label: "Stopped", count: (facet?.exited ?? 0) + (facet?.created ?? 0) + (facet?.dead ?? 0) },
    { value: "attention" as const, label: "Needs attention", count: facet?.attention ?? 0 },
  ];

  const projectTotals = useMemo(() => {
    const map = new Map<string, number>();
    for (const f of data?.projectCounts ?? []) map.set(f.value, f.count);
    return map;
  }, [data?.projectCounts]);
  const projectOptions = useMemo(() => {
    const ids = new Set(projectTotals.keys());
    if (projectFilter) ids.add(projectFilter);
    return [...ids]
      .map((id) => ({
        id,
        count: projectTotals.get(id) ?? 0,
        name: projects.get(id)?.name ?? (projectNameFromId(id) || "standalone"),
        project: projects.get(id),
      }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [projectTotals, projectFilter, projects]);
  const allProjectsCount = [...projectTotals.values()].reduce((a, b) => a + b, 0);
  const selectedProject = projectOptions.find((p) => p.id === projectFilter);

  return (
    <>
      <Toolbar>
        <ChipScroller>
          <SegmentedControl
            aria-label="Container state"
            className="max-w-none"
            value={stateFilter}
            onValueChange={(value) => paged.setFilter("state", value)}
            options={stateOptions}
          />
        </ChipScroller>
        <ToolbarGroup>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" className="max-w-60" aria-label="Filter by project">
                {selectedProject ? (
                  <ProjectIcon icon={selectedProject.project?.icon} name={selectedProject.name} size={16} />
                ) : (
                  <Icon icon={Layers01Icon} />
                )}
                <span className="truncate">{selectedProject ? selectedProject.name : "All projects"}</span>
                <Icon icon={ArrowDown01Icon} data-icon="inline-end" className="text-muted-foreground" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-64">
              <DropdownMenuLabel>Project</DropdownMenuLabel>
              <DropdownMenuRadioGroup
                value={projectFilter || ALL_PROJECTS}
                onValueChange={(value) => paged.setFilter("project", value === ALL_PROJECTS ? "" : value)}
              >
                <DropdownMenuRadioItem value={ALL_PROJECTS}>
                  All projects
                  <span className="ml-auto font-mono text-small text-text-4 tabular-nums">{allProjectsCount}</span>
                </DropdownMenuRadioItem>
                <DropdownMenuSeparator />
                {projectOptions.map((p) => (
                  <DropdownMenuRadioItem key={p.id} value={p.id}>
                    <ProjectIcon icon={p.project?.icon} name={p.name} size={16} />
                    <span className="truncate">{p.name}</span>
                    <span className="ml-auto font-mono text-small text-text-4 tabular-nums">{p.count}</span>
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
          <SortSelect value={paged.order} onValueChange={paged.setOrder} options={SORT_OPTIONS} />
        </ToolbarGroup>
        <ToolbarSpacer />
        <PagedSearchInput
          value={paged.query}
          onCommit={paged.setQuery}
          placeholder="Search name, image or port"
          aria-label="Search containers"
        />
      </Toolbar>

      <SectionCard
        title="All containers"
        icon={ContainerIcon}
        description={
          data ? (
            <span className="tabular-nums">
              {pager.totalSize} of {hostTotal} · refreshed every 10 s, usage live
            </span>
          ) : undefined
        }
        flush
      >
        <PaginatedScrollArea
          status={listQuery}
          isEmpty={rows.length === 0}
          empty={
            hostTotal === 0 && pager.totalSize === 0 ? (
              <EmptyState icon={ContainerIcon} title="No containers" description="Docker reports no containers on this host." />
            ) : (
              <EmptyState
                size="compact"
                icon={SearchRemoveIcon}
                title="No containers match"
                description="Try another state, project or search term."
                action={
                  <Button variant="outline" onClick={paged.reset}>
                    Clear filters
                  </Button>
                }
              />
            )
          }
          orientation={desktop ? "both" : "vertical"}
          aria-label="Containers"
          pagination={pager}
        >
          {desktop ? (
            <ContainersTable
              containers={rows}
              projects={projects}
              actions={actions}
              order={paged.order}
              onOrderChange={paged.setOrder}
              groupByProject={grouped}
              projectTotals={projectTotals}
            />
          ) : (
            <ContainerCardList containers={rows} projects={projects} actions={actions} />
          )}
        </PaginatedScrollArea>
      </SectionCard>
    </>
  );
}

const ALL_PROJECTS = "__all__";
