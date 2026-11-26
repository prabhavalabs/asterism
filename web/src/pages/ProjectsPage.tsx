import { useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight01Icon, ChartUpIcon, CpuIcon, CubeIcon, Folder01Icon, RamMemoryIcon, SearchRemoveIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { HealthStatus, WorkloadRole, type Project } from "@/gen/asterism/v1/asterism_pb";
import { inventoryClient } from "@/lib/rpc";
import {
  ChipScroller,
  CategoryBadge,
  EmptyState,
  ErrorState,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  SectionCard,
  SegmentedControl,
  SortSelect,
  StatCard,
  StatusPill,
  Toolbar,
  ToolbarSpacer,
  ProjectIcon,
  categoryLabel,
  healthStyle,
  type SortOption,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { WorkloadRow } from "@/components/portal/WorkloadRow";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { formatBytes, formatPct } from "@/lib/format";
import { PROJECTS_LIST } from "@/lib/routeSearch";
import { cn } from "@/lib/utils";

type RankTone = "chart-1" | "chart-2" | "chart-3" | "chart-4" | "chart-5";
const RANK_TONES: readonly RankTone[] = ["chart-1", "chart-3", "chart-5", "chart-4", "chart-2"];
const RANK_DOT: Record<RankTone, string> = {
  "chart-1": "bg-chart-1",
  "chart-2": "bg-chart-2",
  "chart-3": "bg-chart-3",
  "chart-4": "bg-chart-4",
  "chart-5": "bg-chart-5",
};
const RANKED = 8;
const CARD_WORKLOADS = 4;

const PROJECT_SORTS: readonly SortOption[] = [
  { value: "memory desc", label: "Memory, highest" },
  { value: "cpu desc", label: "CPU, highest" },
  { value: "name", label: "Name" },
  { value: "health", label: "Health, worst first" },
  { value: "workloads desc", label: "Most services" },
];

const CATEGORY_ORDER = ["production", "staging", "infra"];

function categoryRank(category: string): number {
  const index = CATEGORY_ORDER.indexOf(category);
  return index === -1 ? CATEGORY_ORDER.length : index;
}

export function ProjectsPage() {
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);

  // Totals and the memory ranking cover every project (GetOverview carries
  // the whole set); the cards below are server pages of ListProjects.
  const overviewQuery = useQuery({
    queryKey: ["overview"],
    queryFn: () => inventoryClient.getOverview({}),
    refetchInterval: 15_000,
  });
  const allProjects = useMemo(() => overviewQuery.data?.projects ?? [], [overviewQuery.data?.projects]);

  const ranked = useMemo(
    () => [...allProjects].sort((a, b) => Number(b.memUsedBytes) - Number(a.memUsedBytes)).slice(0, RANKED),
    [allProjects],
  );
  const maxMem = Math.max(...ranked.map((p) => Number(p.memUsedBytes)), 1);
  const rankedTotal = ranked.reduce((acc, p) => acc + Number(p.memUsedBytes), 0);

  const workloads = allProjects.flatMap((p) => p.workloads);
  const totalMem = allProjects.reduce((acc, p) => acc + Number(p.memUsedBytes), 0);
  const totalShare = allProjects.reduce((acc, p) => acc + p.memSharePct, 0);
  const totalCpu = allProjects.reduce((acc, p) => acc + p.cpuUtil, 0);
  const healthy = allProjects.filter((p) => p.status === HealthStatus.HEALTHY).length;
  const apps = workloads.filter((w) => w.role === WorkloadRole.APP || w.role === WorkloadRole.WORKER).length;
  const dataStores = workloads.filter(
    (w) => w.role === WorkloadRole.DATABASE || w.role === WorkloadRole.CACHE || w.role === WorkloadRole.QUEUE,
  ).length;

  return (
    <Page>
      <PageHeader
        title="Projects"
        description="Every Compose stack and unit group, ranked by resident memory."
      />

      {overviewQuery.isError && !overviewQuery.data ? (
        <Card>
          <ErrorState
            title="Couldn't load projects"
            error={overviewQuery.error}
            onRetry={() => void overviewQuery.refetch()}
            retrying={overviewQuery.isFetching}
          />
        </Card>
      ) : !overviewQuery.data ? (
        <>
          <LoadingState variant="cards" label="Loading projects…" />
          <Card>
            <LoadingState variant="rows" count={6} />
          </Card>
        </>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <StatCard
              label="Projects"
              icon={Folder01Icon}
              value={allProjects.length}
              mono={false}
              sub={`${healthy} of ${allProjects.length} healthy`}
            />
            <StatCard
              label="Workloads"
              icon={CubeIcon}
              value={workloads.length}
              mono={false}
              sub={`${apps} apps · ${dataStores} data stores`}
            />
            <StatCard
              label="Container memory"
              icon={RamMemoryIcon}
              value={formatBytes(totalMem)}
              sub={`${totalShare.toFixed(1)}% of VPS memory`}
            >
              <MeterBar size="lg" value={totalShare / 100} hatch label="Container memory share" />
            </StatCard>
            <StatCard
              label="Container CPU"
              icon={CpuIcon}
              value={formatPct(totalCpu)}
              sub={`across ${workloads.length} workloads`}
            >
              <MeterBar size="lg" value={totalCpu} label="Container CPU" />
            </StatCard>
          </div>

          <SectionCard
            title="Memory cost ranking"
            icon={ChartUpIcon}
            description="Live container memory summed per project"
            actions={
              <Badge variant="secondary" mono>
                Top {ranked.length} · {formatBytes(rankedTotal)}
              </Badge>
            }
          >
            <ol className="grid gap-x-10 gap-y-5 md:grid-cols-2">
              {ranked.map((proj, idx) => {
                const tone = RANK_TONES[idx % RANK_TONES.length];
                return (
                  <li key={proj.id} className="flex min-w-0 flex-col gap-2">
                    <div className="flex min-w-0 items-baseline justify-between gap-3">
                      <Link
                        to="/projects/$projectId"
                        params={{ projectId: proj.name }}
                        className="inline-flex min-w-0 items-center gap-2 rounded-sm text-body font-medium text-foreground outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
                      >
                        <ProjectIcon icon={proj.icon} name={proj.name} size={20} />
                        <span className="truncate">{proj.name}</span>
                        <span aria-hidden="true" className={cn("size-2 shrink-0", RANK_DOT[tone])} />
                      </Link>
                      <span className="shrink-0 font-mono text-small text-muted-foreground tabular-nums">
                        <span className="text-mono font-semibold text-foreground">{formatBytes(proj.memUsedBytes)}</span>
                        {" · "}
                        {proj.memSharePct.toFixed(1)}% of VPS
                      </span>
                    </div>
                    <MeterBar
                      size="lg"
                      tone={tone}
                      hatch
                      value={Number(proj.memUsedBytes) / maxMem}
                      label={`${proj.name} memory`}
                      valueText={`${formatBytes(proj.memUsedBytes)}, ${proj.memSharePct.toFixed(1)}% of VPS`}
                    />
                  </li>
                );
              })}
            </ol>
          </SectionCard>

          <ProjectCards onSelectWorkload={setSelectedWorkloadId} />
        </>
      )}

      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}

/** Project cards: category chips, sort and search in the URL, one server page of ListProjects. */
function ProjectCards({ onSelectWorkload }: { onSelectWorkload: (id: string) => void }) {
  const paged = usePagedSearch(PROJECTS_LIST);
  const category = paged.filters.category;
  const query = useQuery({
    queryKey: paged.queryKey(["projects", "page"]),
    queryFn: () => inventoryClient.listProjects({ ...paged.request, category: category === "all" ? "" : category }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const projects = query.data?.projects ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "projects");

  // Facet counts: per category after the search, ignoring the category chip.
  const categoryOptions = useMemo(() => {
    const facets = query.data?.categoryCounts ?? [];
    const counts = new Map(facets.map((f) => [f.value, f.count]));
    if (category !== "all" && !counts.has(category)) counts.set(category, 0);
    const cats = [...counts.keys()].sort((a, b) => categoryRank(a) - categoryRank(b) || a.localeCompare(b));
    const all = facets.reduce((acc, f) => acc + f.count, 0);
    return [
      { value: "all", label: "All", count: all },
      ...cats.map((c) => ({ value: c, label: categoryLabel(c), count: counts.get(c) ?? 0 })),
    ];
  }, [query.data?.categoryCounts, category]);
  const filtered = paged.query !== "" || category !== "all";

  return (
    <>
      <Toolbar>
        <ChipScroller>
          <SegmentedControl
            aria-label="Category"
            className="max-w-none"
            value={category}
            onValueChange={(value) => paged.setFilter("category", value)}
            options={categoryOptions}
          />
        </ChipScroller>
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={PROJECT_SORTS} />
        <PagedSearchInput
          value={paged.query}
          onCommit={paged.setQuery}
          placeholder="Search projects"
          aria-label="Search projects"
        />
      </Toolbar>

      <SectionCard
        title="All projects"
        icon={Folder01Icon}
        description={query.data ? <span className="tabular-nums">{pager.totalSize} projects match</span> : undefined}
        flush
      >
        <PaginatedScrollArea
          status={query}
          isEmpty={projects.length === 0}
          orientation="vertical"
          aria-label="Projects"
          pagination={pager}
          empty={
            <EmptyState
              size="compact"
              icon={SearchRemoveIcon}
              title={filtered ? "No projects match" : "No projects yet"}
              description={
                paged.query ? `Nothing matches "${paged.query}" in this category.` : filtered ? "No projects in this category." : undefined
              }
              action={
                filtered ? (
                  <Button variant="outline" onClick={paged.reset}>
                    Clear filters
                  </Button>
                ) : undefined
              }
            />
          }
        >
          <div className="grid gap-3 p-3 lg:grid-cols-2">
            {projects.map((proj) => (
              <ProjectCard key={proj.id} project={proj} onSelectWorkload={onSelectWorkload} />
            ))}
          </div>
        </PaginatedScrollArea>
      </SectionCard>
    </>
  );
}

function ProjectCard({ project, onSelectWorkload }: { project: Project; onSelectWorkload: (id: string) => void }) {
  const shown = project.workloads.slice(0, CARD_WORKLOADS);
  const more = project.workloads.length - shown.length;
  const services = project.workloads.length;

  return (
    <SectionCard
      title={
        <span className="flex min-w-0 items-center gap-2.5">
          <ProjectIcon icon={project.icon} name={project.name} size={24} />
          <Link
            to="/projects/$projectId"
            params={{ projectId: project.name }}
            className="truncate rounded-sm outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
          >
            {project.name}
          </Link>
        </span>
      }
      description={<span className="font-mono text-mono">{project.workingDir || project.id}</span>}
      actions={
        <>
          <CategoryBadge category={project.category} className="hidden sm:inline-flex" />
          <StatusPill health={project.status}>
            {healthStyle(project.status).label} {project.healthScore}%
          </StatusPill>
        </>
      }
      flush
      footer={
        <div className="flex w-full items-center justify-between gap-3">
          <span className="tabular-nums">
            {more > 0 ? `+${more} more ${more === 1 ? "service" : "services"}` : `${services} ${services === 1 ? "service" : "services"}`}
          </span>
          <Button variant="outline" size="sm" asChild>
            <Link to="/projects/$projectId" params={{ projectId: project.name }}>
              Architecture
              <Icon icon={ArrowUpRight01Icon} />
            </Link>
          </Button>
        </div>
      }
    >
      <div className="grid grid-cols-2 gap-3 px-4 pt-4 pb-4">
        <div className="flex min-w-0 flex-col gap-1 rounded-tile bg-surface-subtle p-4 ring-1 ring-inset ring-border">
          <span className="text-small text-muted-foreground">Memory</span>
          <span className="flex min-w-0 items-baseline gap-2 font-mono tabular-nums">
            <span className="truncate font-heading text-heading tracking-[-0.02em] text-foreground tabular-nums sm:text-title">{formatBytes(project.memUsedBytes)}</span>
            <span className="shrink-0 text-small text-muted-foreground">{project.memSharePct.toFixed(1)}%</span>
          </span>
        </div>
        <div className="flex min-w-0 flex-col gap-1 rounded-tile bg-surface-subtle p-4 ring-1 ring-inset ring-border">
          <span className="text-small text-muted-foreground">CPU</span>
          <span className="flex min-w-0 items-baseline gap-2 font-mono tabular-nums">
            <span className="truncate font-heading text-heading tracking-[-0.02em] text-foreground tabular-nums sm:text-title">{formatPct(project.cpuUtil)}</span>
            <span className="shrink-0 text-small text-muted-foreground">
              {services} svc
            </span>
          </span>
        </div>
      </div>
      <div className="divide-y divide-divider border-t border-divider">
        {shown.map((w) => (
          <WorkloadRow key={w.id} workload={w} onSelect={onSelectWorkload} maxPorts={1} />
        ))}
      </div>
    </SectionCard>
  );
}
