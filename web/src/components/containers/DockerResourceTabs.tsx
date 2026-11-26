import type { ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  CloudServerIcon,
  DatabaseIcon,
  HardDriveIcon,
  Image01Icon,
  NetworkIcon,
  SearchRemoveIcon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  DataTableFrame,
  EmptyState,
  dataTableClasses,
  ErrorState,
  LoadingState,
  MeterBar,
  PagedSearchInput,
  PropertyRow,
  CardGrid,
  SectionCard,
  SegmentedControl,
  SortHeader,
  SortSelect,
  Toolbar,
  type SortOption,
  ToolbarSpacer,
} from "@/components/common";
import type { DiskUsageCategory, DockerEngine } from "@/gen/asterism/v1/asterism_pb";
import { dockerClient } from "@/lib/rpc";
import { formatBytes, formatRelativeTime } from "@/lib/format";
import { IMAGES_LIST, NETWORKS_LIST, VOLUMES_LIST, type NetworkFilter } from "@/lib/routeSearch";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { cn } from "@/lib/utils";
import { containerKeys } from "./model";

const th = dataTableClasses.th;
const td = dataTableClasses.td;

function UsedBy({ names, count }: { names: readonly string[]; count: number }) {
  if (count === 0) return <span className="text-text-4">Unused</span>;
  const label = `${count} ${count === 1 ? "container" : "containers"}`;
  if (names.length === 0) return <span className="tabular-nums text-text-2">{label}</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="cursor-default text-text-2 tabular-nums underline decoration-dotted decoration-text-4 underline-offset-4 outline-none focus-visible:ring-1 focus-visible:ring-ring">
          {label}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-80 flex-wrap font-mono">{names.join(", ")}</TooltipContent>
    </Tooltip>
  );
}

function QueryFrame({
  query,
  emptyIcon,
  emptyTitle,
  children,
}: {
  query: { isError: boolean; error: unknown; data: unknown; refetch: () => unknown; isFetching: boolean };
  emptyIcon: Parameters<typeof EmptyState>[0]["icon"];
  emptyTitle: string;
  children: ReactNode;
}) {
  if (query.isError && !query.data) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} retrying={query.isFetching} />;
  }
  if (!query.data) return <LoadingState variant="rows" count={6} />;
  return children ?? <EmptyState icon={emptyIcon} title={emptyTitle} size="compact" />;
}

function NoMatches({ onClear }: { onClear: () => void }) {
  return (
    <EmptyState
      size="compact"
      icon={SearchRemoveIcon}
      title="Nothing matches"
      description="Try another filter or search term."
      action={
        <Button variant="outline" onClick={onClear}>
          Clear filters
        </Button>
      }
    />
  );
}

// --- Images -----------------------------------------------------------------
//
// Server-paged lists: page, size, query, order and filter live in the URL
// (usePagedSearch with the configs in lib/routeSearch), the request carries
// `paged.request` plus the list's own filter fields, and the footer and chip
// counts come from the response (PageInfo and facet counts).

type ImageFilter = "all" | "in-use" | "dangling";

const IMAGE_SORTS: readonly SortOption[] = [
  { value: "size desc", label: "Size, largest" },
  { value: "created desc", label: "Newest" },
  { value: "repository", label: "Repository" },
  { value: "containers desc", label: "Most used" },
];

export function ImagesTab() {
  const paged = usePagedSearch(IMAGES_LIST);
  const filter = paged.filters.filter as ImageFilter;
  const query = useQuery({
    queryKey: paged.queryKey(containerKeys.images()),
    queryFn: () =>
      dockerClient.listImages({ ...paged.request, danglingOnly: filter === "dangling", inUseOnly: filter === "in-use" }),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
  const images = query.data?.images ?? [];
  const counts = query.data?.counts;
  const pager = usePager(paged, query.data?.pageInfo, "images");
  const sort = { order: paged.order, onOrderChange: paged.setOrder };

  return (
    <div className="flex flex-col gap-3">
      <Toolbar>
        <SegmentedControl
          aria-label="Image filter"
          value={filter}
          onValueChange={(value) => paged.setFilter("filter", value)}
          options={[
            { value: "all", label: "All", count: counts?.total ?? 0 },
            { value: "in-use", label: "In use", count: counts?.inUse ?? 0 },
            { value: "dangling", label: "Dangling", count: counts?.dangling ?? 0 },
          ]}
        />
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={IMAGE_SORTS} className="md:hidden" />
        <PagedSearchInput value={paged.query} onCommit={paged.setQuery} placeholder="Search images" aria-label="Search images" />
      </Toolbar>
      <SectionCard
        title="Images"
        icon={Image01Icon}
        description={
          query.data ? (
            <span className="tabular-nums">
              {pager.totalSize} of {counts?.total ?? 0} · {formatBytes(query.data.totalSizeBytes)} on disk
            </span>
          ) : undefined
        }
        flush
      >
        <DataTableFrame
          status={query}
          isEmpty={images.length === 0}
          empty={
            pager.totalSize === 0 && !paged.query && filter === "all" ? (
              <EmptyState icon={Image01Icon} title="No images" size="compact" />
            ) : (
              <NoMatches onClear={paged.reset} />
            )
          }
          minWidthClassName="min-w-[760px]"
          aria-label="Images"
          pagination={pager}
        >
          <thead className="text-small text-muted-foreground">
            <tr>
              <SortHeader label="Repository:tag" field="repository" {...sort} className="pl-4" />
              <th scope="col" className={cn(th, "w-44")}>Image ID</th>
              <SortHeader label="Size" field="size" {...sort} defaultDesc align="right" className="w-28" />
              <SortHeader label="Created" field="created" {...sort} defaultDesc className="w-32" />
              <SortHeader label="In use" field="containers" {...sort} defaultDesc className="w-36 pr-4" />
            </tr>
          </thead>
          <tbody>
            {images.map((img) => (              <tr key={img.id} className="transition-colors hover:bg-muted/50">
                <td className={cn(td, "pl-4")}>
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-mono text-mono text-foreground" title={img.repoTags.join(", ")}>
                      {img.dangling ? "<none>:<none>" : img.repoTags[0] || `${img.repository}:${img.tag}`}
                    </span>
                    {img.repoTags.length > 1 && (
                      <span className="shrink-0 text-small text-muted-foreground tabular-nums">+{img.repoTags.length - 1} tags</span>
                    )}
                    {img.dangling && <Badge variant="warning">Dangling</Badge>}
                  </span>
                </td>
                <td className={cn(td, "font-mono text-mono text-text-2")}>{img.id.replace(/^sha256:/, "")}</td>
                <td className={cn(td, "text-right font-mono text-mono text-foreground tabular-nums")}>{formatBytes(img.sizeBytes)}</td>
                <td className={cn(td, "text-text-2 tabular-nums")}>{img.createdAtMs > 0n ? formatRelativeTime(img.createdAtMs) : "—"}</td>
                <td className={cn(td, "pr-4")}>
                  <UsedBy names={img.usedBy} count={img.containers} />
                </td>
              </tr>
            ))}
          </tbody>
        </DataTableFrame>
      </SectionCard>
    </div>
  );
}

// --- Volumes ----------------------------------------------------------------

type VolumeFilter = "all" | "in-use" | "unused";

const VOLUME_SORTS: readonly SortOption[] = [
  { value: "name", label: "Name" },
  { value: "size desc", label: "Size, largest" },
  { value: "created desc", label: "Newest" },
  { value: "containers desc", label: "Most used" },
  { value: "project", label: "Project" },
];

export function VolumesTab() {
  const paged = usePagedSearch(VOLUMES_LIST);
  const filter = paged.filters.filter as VolumeFilter;
  const query = useQuery({
    queryKey: paged.queryKey(containerKeys.volumes()),
    queryFn: () =>
      dockerClient.listVolumes({ ...paged.request, inUseOnly: filter === "in-use", unusedOnly: filter === "unused" }),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
  const volumes = query.data?.volumes ?? [];
  const counts = query.data?.counts;
  const pager = usePager(paged, query.data?.pageInfo, "volumes");
  const sort = { order: paged.order, onOrderChange: paged.setOrder };

  return (
    <div className="flex flex-col gap-3">
      <Toolbar>
        <SegmentedControl
          aria-label="Volume filter"
          value={filter}
          onValueChange={(value) => paged.setFilter("filter", value)}
          options={[
            { value: "all", label: "All", count: counts?.total ?? 0 },
            { value: "in-use", label: "In use", count: counts?.inUse ?? 0 },
            { value: "unused", label: "Unused", count: counts?.unused ?? 0 },
          ]}
        />
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={VOLUME_SORTS} className="md:hidden" />
        <PagedSearchInput value={paged.query} onCommit={paged.setQuery} placeholder="Search volumes" aria-label="Search volumes" />
      </Toolbar>
      <SectionCard
        title="Volumes"
        icon={DatabaseIcon}
        description={
          query.data ? (
            <span className="tabular-nums">
              {pager.totalSize} of {counts?.total ?? 0} · {formatBytes(query.data.totalSizeBytes)} measured
            </span>
          ) : undefined
        }
        flush
      >
        <DataTableFrame
          status={query}
          isEmpty={volumes.length === 0}
          empty={
            pager.totalSize === 0 && !paged.query && filter === "all" ? (
              <EmptyState icon={DatabaseIcon} title="No volumes" size="compact" />
            ) : (
              <NoMatches onClear={paged.reset} />
            )
          }
          minWidthClassName="min-w-[860px]"
          aria-label="Volumes"
          pagination={pager}
        >
          <thead className="text-small text-muted-foreground">
            <tr>
              <SortHeader label="Name" field="name" {...sort} className="pl-4" />
              <th scope="col" className={cn(th, "w-24")}>Driver</th>
              <SortHeader label="Size" field="size" {...sort} defaultDesc align="right" className="w-28" />
              <SortHeader label="In use" field="containers" {...sort} defaultDesc className="w-36" />
              <th scope="col" className={cn(th, "w-[36%] pr-4")}>Mountpoint</th>
            </tr>
          </thead>
          <tbody>
            {volumes.map((v) => (
              <tr key={v.name} className="transition-colors hover:bg-muted/50">
                <td className={cn(td, "pl-4")}>
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-mono text-mono text-foreground" title={v.name}>
                      {v.anonymous ? `${v.name.slice(0, 12)}…` : v.name}
                    </span>
                    {v.anonymous && <Badge variant="outline">Anonymous</Badge>}
                    {v.composeProject && (
                      <span className="hidden shrink-0 text-small text-muted-foreground lg:inline">{v.composeProject}</span>
                    )}
                  </span>
                </td>
                <td className={cn(td, "font-mono text-mono text-text-2")}>{v.driver}</td>
                <td className={cn(td, "text-right font-mono text-mono tabular-nums")}>
                  {v.sizeBytes < 0n ? (
                    <span className="text-text-4" title="Docker has not measured this volume yet">
                      unknown
                    </span>
                  ) : (
                    <span className="text-foreground">{formatBytes(v.sizeBytes)}</span>
                  )}
                </td>
                <td className={td}>
                  <UsedBy names={v.usedBy} count={v.containers} />
                </td>
                <td className={cn(td, "max-w-0 pr-4")}>
                  <span className="block truncate font-mono text-small text-muted-foreground" title={v.mountpoint}>
                    {v.mountpoint}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </DataTableFrame>
      </SectionCard>
    </div>
  );
}

// --- Networks ---------------------------------------------------------------

const NETWORK_SORTS: readonly SortOption[] = [
  { value: "name", label: "Name" },
  { value: "driver", label: "Driver" },
  { value: "containers desc", label: "Most containers" },
  { value: "created desc", label: "Newest" },
];

export function NetworksTab() {
  const paged = usePagedSearch(NETWORKS_LIST);
  const filter = paged.filters.filter as NetworkFilter;
  const query = useQuery({
    queryKey: paged.queryKey(containerKeys.networks()),
    queryFn: () => dockerClient.listNetworks({ ...paged.request, userDefinedOnly: filter === "user-defined" }),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
  const networks = query.data?.networks ?? [];
  const counts = query.data?.counts;
  const pager = usePager(paged, query.data?.pageInfo, "networks");
  const sort = { order: paged.order, onOrderChange: paged.setOrder };
  const builtin = counts?.builtin ?? 0;

  return (
    <div className="flex flex-col gap-3">
      <Toolbar>
        <SegmentedControl
          aria-label="Network filter"
          value={filter}
          onValueChange={(value) => paged.setFilter("filter", value)}
          options={[
            { value: "user-defined", label: "User-defined", count: counts?.userDefined ?? 0 },
            { value: "all", label: "All", count: counts?.total ?? 0 },
          ]}
        />
        <ToolbarSpacer />
        <SortSelect value={paged.order} onValueChange={paged.setOrder} options={NETWORK_SORTS} className="md:hidden" />
        <PagedSearchInput
          value={paged.query}
          onCommit={paged.setQuery}
          placeholder="Search networks or containers"
          aria-label="Search networks"
        />
      </Toolbar>
      <SectionCard
        title="Networks"
        icon={NetworkIcon}
        description={
          query.data ? (
            <span className="tabular-nums">
              {pager.totalSize} of {counts?.total ?? 0}
              {filter === "user-defined" && builtin > 0
                ? ` · ${builtin} built-in hidden`
                : builtin > 0
                  ? ` · ${builtin} built-in included`
                  : ""}
            </span>
          ) : undefined
        }
        flush
      >
        <DataTableFrame
          status={query}
          isEmpty={networks.length === 0}
          empty={
            pager.totalSize === 0 && !paged.query && (filter === "all" || (counts?.total ?? 0) === 0) ? (
              <EmptyState icon={NetworkIcon} title="No networks" size="compact" />
            ) : pager.totalSize === 0 && !paged.query && filter === "user-defined" ? (
              <EmptyState
                icon={NetworkIcon}
                title="No user-defined networks"
                description={`Only Docker's ${builtin} built-in networks exist on this host.`}
                size="compact"
                action={
                  <Button variant="outline" onClick={() => paged.setFilter("filter", "all")}>
                    Show all networks
                  </Button>
                }
              />
            ) : (
              <NoMatches onClear={paged.reset} />
            )
          }
          minWidthClassName="min-w-[860px]"
          aria-label="Networks"
          pagination={pager}
        >
          <thead className="text-small text-muted-foreground">
            <tr>
              <SortHeader label="Name" field="name" {...sort} className="w-60 pl-4" />
              <SortHeader label="Driver" field="driver" {...sort} className="w-24" />
              <th scope="col" className={cn(th, "w-20")}>Scope</th>
              <th scope="col" className={cn(th, "w-40")}>Subnet</th>
              <SortHeader label="Attached containers" field="containers" {...sort} defaultDesc className="pr-4" />
            </tr>
          </thead>
          <tbody>
            {networks.map((n) => (
              <tr key={n.id} className="transition-colors hover:bg-muted/50">
                <td className={cn(td, "pl-4")}>
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-mono text-mono text-foreground">{n.name}</span>
                    {n.builtin && <Badge variant="outline">Built-in</Badge>}
                    {n.internal && <Badge variant="info">Internal</Badge>}
                  </span>
                </td>
                <td className={cn(td, "font-mono text-mono text-text-2")}>{n.driver}</td>
                <td className={cn(td, "text-text-2")}>{n.scope}</td>
                <td className={cn(td, "font-mono text-mono text-text-2 tabular-nums")}>{n.subnets.join(", ") || "—"}</td>
                <td className={cn(td, "py-1.5 pr-4")}>
                  {n.containers.length === 0 ? (
                    <span className="text-text-4">None</span>
                  ) : (
                    <span className="flex flex-wrap gap-1">
                      {n.containers.slice(0, 6).map((a) => (
                        <Badge key={a.containerId || a.name} variant="secondary" mono asChild>
                          <Link to="/containers/$containerId" params={{ containerId: a.name }} title={a.ipv4Address || undefined}>
                            {a.name}
                          </Link>
                        </Badge>
                      ))}
                      {n.containers.length > 6 && (
                        <span className="self-center text-small text-muted-foreground tabular-nums">+{n.containers.length - 6}</span>
                      )}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </DataTableFrame>
      </SectionCard>
    </div>
  );
}

// --- Disk usage -------------------------------------------------------------

const CATEGORY_LABEL: Record<string, { label: string; unit: [string, string] }> = {
  images: { label: "Images", unit: ["image", "images"] },
  containers: { label: "Containers", unit: ["container", "containers"] },
  volumes: { label: "Volumes", unit: ["volume", "volumes"] },
  build_cache: { label: "Build cache", unit: ["entry", "entries"] },
};

export function DiskUsageTab() {
  const query = useQuery({
    queryKey: containerKeys.disk(),
    queryFn: () => dockerClient.getDiskUsage({}),
    refetchInterval: 60_000,
  });
  const usage = query.data?.usage;
  const engine = query.data?.engine;
  const categories = usage?.categories ?? [];
  const max = Math.max(1, ...categories.map((c) => Number(c.sizeBytes)));

  return (
    <CardGrid split="7:5">
      <SectionCard
        title="Docker disk usage"
        icon={HardDriveIcon}
        description={usage ? `${formatBytes(usage.totalSizeBytes)} used · ${formatBytes(usage.totalReclaimableBytes)} reclaimable` : undefined}
        bodyClassName="flex flex-col"
        flush
      >
        <QueryFrame query={query} emptyIcon={HardDriveIcon} emptyTitle="No disk usage reported">
          {categories.length === 0 ? null : (
            <ul className="flex flex-1 flex-col divide-y divide-divider">
              {categories.map((c) => (
                <DiskRow key={c.kind} category={c} max={max} />
              ))}
            </ul>
          )}
        </QueryFrame>
        {usage && (
          <p className="mt-auto border-t border-divider px-4 py-3 text-small text-muted-foreground">
            Reclaimable space belongs to unused images, stopped containers, unused volumes and build cache. Asterism never prunes; run{" "}
            <code className="font-mono text-text-2">docker system prune</code> on the host if you want it back.
          </p>
        )}
      </SectionCard>
      <SectionCard title="Engine" icon={CloudServerIcon} flush>
        {engine ? <EngineFacts engine={engine} /> : <LoadingState variant="rows" count={6} />}
      </SectionCard>
    </CardGrid>
  );
}

function DiskRow({ category: c, max }: { category: DiskUsageCategory; max: number }) {
  const meta = CATEGORY_LABEL[c.kind] ?? { label: c.kind, unit: ["item", "items"] as [string, string] };
  const size = Number(c.sizeBytes);
  const reclaim = Math.min(size, Number(c.reclaimableBytes));
  const pctReclaim = size > 0 ? Math.round((reclaim / size) * 100) : 0;
  return (
    <li className="flex flex-1 flex-col justify-center gap-2 px-4 py-3.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span className="flex items-baseline gap-2">
          <span className="text-body font-medium text-foreground">{meta.label}</span>
          <span className="text-small text-muted-foreground tabular-nums">
            {c.activeCount} of {c.totalCount} {c.totalCount === 1 ? meta.unit[0] : meta.unit[1]} active
          </span>
        </span>
        <span className="font-mono text-small text-muted-foreground tabular-nums">
          <span className="text-mono font-semibold text-foreground">{formatBytes(size)}</span>
          {" · "}
          {formatBytes(reclaim)} reclaimable ({pctReclaim}%)
        </span>
      </div>
      <MeterBar
        size="lg"
        segments={[
          { value: (size - reclaim) / max, tone: "chart-1", label: "In use" },
          { value: reclaim / max, tone: "warning", label: "Reclaimable" },
        ]}
        label={`${meta.label} disk usage`}
        valueText={`${formatBytes(size)} used, ${formatBytes(reclaim)} reclaimable`}
      />
    </li>
  );
}

function EngineFacts({ engine: e }: { engine: DockerEngine }) {
  if (!e.available) {
    return <EmptyState size="compact" icon={CloudServerIcon} title="Docker is not available" description={e.unavailableReason} />;
  }
  return (
    <div className="divide-y divide-divider">
      <PropertyRow label="Server version" mono>{e.serverVersion || "—"}</PropertyRow>
      <PropertyRow label="Operating system">{e.operatingSystem || "—"}</PropertyRow>
      <PropertyRow label="Kernel" mono>{e.kernelVersion || "—"}</PropertyRow>
      <PropertyRow label="Architecture" mono>{e.architecture || "—"}</PropertyRow>
      <PropertyRow label="Storage driver" mono>{e.storageDriver || "—"}</PropertyRow>
      <PropertyRow label="Cgroup" mono>
        {[e.cgroupDriver, e.cgroupVersion ? `v${e.cgroupVersion}` : ""].filter(Boolean).join(" · ") || "—"}
      </PropertyRow>
      <PropertyRow label="Logging driver" mono>{e.loggingDriver || "—"}</PropertyRow>
      <PropertyRow label="Docker root" mono>{e.dockerRootDir || "—"}</PropertyRow>
      <PropertyRow label="Resources" mono>
        {e.ncpu} CPU · {formatBytes(e.memTotalBytes)}
      </PropertyRow>
      <PropertyRow label="Containers" mono>
        {e.containersRunning} running · {e.containersPaused} paused · {e.containersStopped} stopped
      </PropertyRow>
      <PropertyRow label="Images" mono>{e.images}</PropertyRow>
    </div>
  );
}
