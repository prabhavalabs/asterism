import { useEffect, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  Alert02Icon,
  ArrowDataTransferVerticalIcon,
  ChartLineData01Icon,
  ContainerIcon,
  CpuIcon,
  FileScriptIcon,
  Folder02Icon,
  HeartCheckIcon,
  Idea01Icon,
  NetworkIcon,
  PlugSocketIcon,
  RamMemoryIcon,
  Settings02Icon,
  Tag01Icon,
  Timer02Icon,
  ViewOffSlashIcon,
} from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  ChipScroller,
  EmptyState,
  ErrorState,
  ListRow,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PaginatedScrollArea,
  PortChip,
  ProjectIcon,
  SectionCard,
  StatCard,
  StatusDot,
  StatusPill,
  TONE_TEXT_CLASSES,
  TechIcon,
  projectNameFromId,
} from "@/components/common";
import { UPlotChart } from "@/components/charts/UPlotChart";
import { ContainerActionBar } from "@/components/containers/ContainerActions";
import { CONTAINER_METRICS, seedContainerHistory } from "@/components/containers/history";
import { LiveMetricText } from "@/components/containers/live";
import {
  containerKeys,
  healthStyle,
  insightTone,
  isUp,
  lifetimeText,
  portText,
  shortImage,
  stateStyle,
} from "@/components/containers/model";
import { useContainerActions } from "@/components/containers/useContainerActions";
import { liveStore } from "@/live/LiveStore";
import { EventRow } from "@/components/portal/EventRow";
import { WorkloadLogs } from "@/components/portal/WorkloadLogs";
import { ContainerHealth, ContainerState, type Container } from "@/gen/asterism/v1/asterism_pb";
import { dockerClient, eventsClient, inventoryClient } from "@/lib/rpc";
import { formatBps, formatBytes, formatDateTime, formatPct, formatRelativeTime, roleLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { CONTAINER_EVENTS_LIST } from "@/lib/routeSearch";

const BACK = { to: "/containers", label: "Containers" } as const;
const CHART_HEIGHT = 170;
const HISTORY_MS = 15 * 60_000;
const TABS = ["overview", "config", "events", "logs"] as const;
type DetailTab = (typeof TABS)[number];

// Module-level so charts and live text never see new function identities.
const fmtPctTick = (v: number) => `${Math.round(v * 100)}%`;
const fmtBytesTick = (v: number) => formatBytes(v);
const fmtBpsTick = (v: number) => formatBps(v);
const fmtPct = (v: number) => formatPct(v);
const fmtBytes = (v: number) => formatBytes(v);
const fmtBps = (v: number) => formatBps(v);

function isDetailTab(v: unknown): v is DetailTab {
  return typeof v === "string" && (TABS as readonly string[]).includes(v);
}

export function ContainerDetailPage() {
  const { containerId } = useParams({ strict: false }) as { containerId: string };
  const search = useSearch({ strict: false }) as { tab?: string };
  const navigate = useNavigate();
  const tab: DetailTab = isDetailTab(search.tab) ? search.tab : "overview";

  const query = useQuery({
    queryKey: containerKeys.detail(containerId),
    queryFn: () => dockerClient.getContainer({ id: containerId }),
    refetchInterval: 5_000,
  });
  const projectsQuery = useQuery({
    queryKey: ["projects", "all"],
    queryFn: () => inventoryClient.listProjects({ category: "" }),
    refetchInterval: 15_000,
  });

  const c = query.data?.container;
  const actions = useContainerActions(query.data?.capabilities);
  const entityId = c?.id ?? "";

  useEffect(() => {
    if (!entityId) return;
    return seedContainerHistory([entityId], CONTAINER_METRICS, HISTORY_MS);
  }, [entityId]);

  if (!c) {
    return (
      <Page>
        <PageHeader back={BACK} title={containerId} />
        <Card>
          {query.isError ? (
            <ErrorState
              title="Couldn't load this container"
              error={query.error}
              onRetry={() => void query.refetch()}
              retrying={query.isFetching}
            />
          ) : (
            <LoadingState label="Loading container…" />
          )}
        </Card>
      </Page>
    );
  }

  const project = projectsQuery.data?.projects.find((p) => p.id === c.projectId);
  const projectName = project?.name ?? (projectNameFromId(c.projectId) || "standalone");
  const state = stateStyle(c);
  const health = healthStyle(c.health);
  const running = c.state === ContainerState.RUNNING;
  const memLimit = Number(c.stats?.memLimitBytes || c.resources?.memLimitBytes || 0n);
  const memUsed = Number(c.stats?.memUsedBytes ?? 0n);
  const actionError = actions.errors[c.id];
  const socketMounts = c.mounts.filter((m) => m.dockerSocket);
  // Stopped containers keep their charts while recent history exists; read at render (every poll).
  const hasHistory = running || liveStore.getSeries(c.id, "mem.used_bytes").values.some((v) => v > 0);

  return (
    <Page>
      <PageHeader
        back={BACK}
        title={
          <span className="flex min-w-0 items-center gap-3">
            <TechIcon slug={c.tech} role={c.role} image={c.image} size={32} />
            <span className="min-w-0 break-words">{c.name}</span>
          </span>
        }
        meta={
          <>
            <StatusPill tone={state.tone} pulse={c.state === ContainerState.RESTARTING}>
              {state.label}
              {c.state === ContainerState.EXITED ? ` (${c.exitCode})` : ""}
            </StatusPill>
            {health && <StatusPill tone={health.tone}>{health.label}</StatusPill>}
            {c.oomKilled && <Badge variant="critical">OOM killed</Badge>}
            {c.protected && <Badge variant="outline">Protected</Badge>}
          </>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono text-mono text-text-2" title={c.image}>
              {shortImage(c)}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <ProjectIcon icon={project?.icon} name={projectName} size={16} />
              {project ? (
                <Link
                  to="/projects/$projectId"
                  params={{ projectId: project.name }}
                  className="outline-none hover:text-foreground hover:underline focus-visible:ring-1 focus-visible:ring-ring"
                >
                  {projectName}
                </Link>
              ) : (
                projectName
              )}
              {c.composeService && <span className="text-text-4">/ {c.composeService}</span>}
            </span>
            <span className="font-mono text-mono">{c.containerId}</span>
          </span>
        }
        actions={<ContainerActionBar container={c} actions={actions} />}
      />

      {actionError && (
        <div role="alert" className="flex items-start gap-3 rounded-tile bg-status-critical-surface px-4 py-3 text-control ring-1 ring-inset ring-status-critical/25">
          <Icon icon={Alert02Icon} className="mt-0.5 size-4 shrink-0 text-status-critical" />
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="font-medium text-foreground">{actionError.title}</span>
            <span className="text-small text-text-2 text-pretty">{actionError.description}</span>
          </div>
        </div>
      )}

      {c.insights.length > 0 && (
        <SectionCard title="Insights" icon={Idea01Icon} description={`${c.insights.length} observations`} flush>
          <ul className="divide-y divide-divider">
            {c.insights.map((i) => (
              <li key={i.code} className="flex min-h-10 items-center gap-3 px-4 py-2">
                <StatusPill tone={insightTone(i.severity)} className="w-[5.5rem] justify-center">
                  {i.severity.charAt(0).toUpperCase() + i.severity.slice(1)}
                </StatusPill>
                <span className="min-w-0 flex-1 text-control text-foreground">{i.message}</span>
                <span className="hidden shrink-0 font-mono text-small text-text-4 sm:inline">{i.code}</span>
              </li>
            ))}
          </ul>
        </SectionCard>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          label="CPU"
          icon={CpuIcon}
          value={<LiveMetricText entityId={c.id} metric="cpu.util" fallback={c.stats?.cpuUtil ?? 0} format={fmtPct} live={running} />}
          sub={c.resources?.cpuLimitCores ? `limit ${c.resources.cpuLimitCores} cores` : "no CPU limit · 100% = 1 core"}
        />
        <StatCard
          label="Memory"
          icon={RamMemoryIcon}
          value={<LiveMetricText entityId={c.id} metric="mem.used_bytes" fallback={memUsed} format={fmtBytes} live={running} />}
          sub={memLimit > 0 ? `of ${formatBytes(memLimit)} limit` : "no memory limit"}
        >
          {memLimit > 0 && <MeterBar value={memUsed / memLimit} label="Memory used of limit" valueText={`${formatPct(memUsed / memLimit, 0)} of limit`} />}
        </StatCard>
        <StatCard
          label="Network"
          icon={ArrowDataTransferVerticalIcon}
          value={<LiveMetricText entityId={c.id} metric="net.rx_bps" fallback={c.stats?.netRxBps ?? 0} format={fmtBps} live={running} />}
          sub={
            <>
              in · out <LiveMetricText entityId={c.id} metric="net.tx_bps" fallback={c.stats?.netTxBps ?? 0} format={fmtBps} live={running} />
            </>
          }
        />
        <StatCard
          label={isUp(c.state) ? "Uptime" : "Stopped"}
          icon={Timer02Icon}
          value={lifetimeText(c)}
          sub={
            <span className={cn(c.restartCount > 0 && TONE_TEXT_CLASSES.warning)}>
              {c.restartCount} {c.restartCount === 1 ? "restart" : "restarts"} · policy {c.restartPolicy || "no"}
            </span>
          }
        />
      </div>

      <SectionCard
        title="Live usage"
        icon={ChartLineData01Icon}
        description={running ? "Last 15 minutes, streamed every second" : "No live samples while the container is not running"}
        flush={!hasHistory}
      >
        {!hasHistory ? (
          <EmptyState
            size="compact"
            icon={ChartLineData01Icon}
            title="No usage recorded yet"
            description={running ? "Samples arrive every second; the charts fill in shortly." : "Start the container to stream CPU, memory, network and disk usage."}
          />
        ) : (
          <div className="grid gap-x-6 gap-y-5 md:grid-cols-2 xl:grid-cols-3">
            <ChartBlock title="CPU" meta={<LiveMetricText entityId={c.id} metric="cpu.util" fallback={c.stats?.cpuUtil ?? 0} format={fmtPct} live={running} />}>
              <UPlotChart entityId={c.id} metric="cpu.util" colorVar="--chart-1" height={CHART_HEIGHT} syncKey="container" valueFormatter={fmtPctTick} ariaLabel={`${c.name} CPU`} />
            </ChartBlock>
            <ChartBlock
              title="Memory"
              meta={
                <>
                  <LiveMetricText entityId={c.id} metric="mem.used_bytes" fallback={memUsed} format={fmtBytes} live={running} />
                  {memLimit > 0 && <span className="text-text-4"> / {formatBytes(memLimit)}</span>}
                </>
              }
            >
              <UPlotChart
                entityId={c.id}
                metric="mem.used_bytes"
                colorVar="--chart-5"
                height={CHART_HEIGHT}
                syncKey="container"
                valueFormatter={fmtBytesTick}
                yRange={memLimit > 0 ? [0, memLimit] : undefined}
                ariaLabel={`${c.name} memory`}
              />
            </ChartBlock>
            <ChartBlock title="Network in" meta={<LiveMetricText entityId={c.id} metric="net.rx_bps" fallback={c.stats?.netRxBps ?? 0} format={fmtBps} live={running} />}>
              <UPlotChart entityId={c.id} metric="net.rx_bps" colorVar="--chart-2" height={CHART_HEIGHT} syncKey="container" valueFormatter={fmtBpsTick} ariaLabel={`${c.name} network received`} />
            </ChartBlock>
            <ChartBlock title="Network out" meta={<LiveMetricText entityId={c.id} metric="net.tx_bps" fallback={c.stats?.netTxBps ?? 0} format={fmtBps} live={running} />}>
              <UPlotChart entityId={c.id} metric="net.tx_bps" colorVar="--chart-3" height={CHART_HEIGHT} syncKey="container" valueFormatter={fmtBpsTick} ariaLabel={`${c.name} network sent`} />
            </ChartBlock>
            <ChartBlock title="Disk read" meta={<LiveMetricText entityId={c.id} metric="blk.read_bps" fallback={c.stats?.blkReadBps ?? 0} format={fmtBps} live={running} />}>
              <UPlotChart entityId={c.id} metric="blk.read_bps" colorVar="--chart-4" height={CHART_HEIGHT} syncKey="container" valueFormatter={fmtBpsTick} ariaLabel={`${c.name} disk reads`} />
            </ChartBlock>
            <ChartBlock title="Disk write" meta={<LiveMetricText entityId={c.id} metric="blk.write_bps" fallback={c.stats?.blkWriteBps ?? 0} format={fmtBps} live={running} />}>
              <UPlotChart entityId={c.id} metric="blk.write_bps" colorVar="--chart-1" height={CHART_HEIGHT} syncKey="container" valueFormatter={fmtBpsTick} ariaLabel={`${c.name} disk writes`} />
            </ChartBlock>
          </div>
        )}
      </SectionCard>

      <Tabs
        value={tab}
        onValueChange={(v) => void navigate({ to: ".", search: { tab: v === "overview" ? undefined : v }, replace: true })}
        className="gap-3"
      >
        <ChipScroller>
          <TabsList variant="line" className="w-max">
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="config">Configuration</TabsTrigger>
            <TabsTrigger value="events">Events</TabsTrigger>
            <TabsTrigger value="logs">Logs</TabsTrigger>
          </TabsList>
        </ChipScroller>

        <TabsContent value="overview" className="flex flex-col gap-3">
          <SectionCard title="Facts" icon={ContainerIcon}>
            <FactsGrid container={c} />
          </SectionCard>
          <div className="grid items-start gap-3 lg:grid-cols-2">
            <SectionCard title="Ports" icon={PlugSocketIcon} description={`${c.ports.filter((p) => p.published).length} published · ${c.ports.filter((p) => !p.published).length} exposed only`} flush>
              {c.ports.length === 0 ? (
                <EmptyState size="compact" icon={PlugSocketIcon} title="No ports" description="The image exposes no ports and none are published." />
              ) : (
                <ul className="divide-y divide-divider">
                  {c.ports.map((p) => (
                    <ListRow
                      key={portText(p)}
                      leading={<PortChip port={p.containerPort} hostPort={p.hostPort} exposure={p.exposure} protocol={p.proto} />}
                      trailing={<span className="text-small text-muted-foreground">{p.published ? "Published" : "Exposed only"}</span>}
                    >
                      <span className="min-w-0 flex-1 truncate font-mono text-mono text-text-2 tabular-nums">{portText(p)}</span>
                    </ListRow>
                  ))}
                </ul>
              )}
            </SectionCard>
            <SectionCard title="Networks" icon={NetworkIcon} description={c.networkMode ? `mode ${c.networkMode}` : undefined} flush>
              {c.networks.length === 0 ? (
                <EmptyState size="compact" icon={NetworkIcon} title="No networks" />
              ) : (
                <ul className="divide-y divide-divider">
                  {c.networks.map((n) => (
                    <ListRow
                      key={n.name}
                      title={<span className="font-mono text-mono">{n.name}</span>}
                      description={n.aliases.length > 0 ? `aliases ${n.aliases.join(", ")}` : undefined}
                      trailing={
                        <span className="font-mono text-small text-muted-foreground tabular-nums">
                          {n.ipAddress || "—"}
                          {n.gateway ? ` via ${n.gateway}` : ""}
                        </span>
                      }
                    />
                  ))}
                </ul>
              )}
            </SectionCard>
          </div>
          <SectionCard title="Mounts" icon={Folder02Icon} description={`${c.mounts.length} mounts · contents are never read`} flush>
            {socketMounts.length > 0 && (
              <p className="flex items-start gap-2 border-b border-divider bg-status-warning-surface px-4 py-2.5 text-control text-foreground">
                <Icon icon={Alert02Icon} className="mt-0.5 size-4 shrink-0 text-status-warning" />
                This container mounts the Docker socket, which gives it root-equivalent control of the host.
              </p>
            )}
            {c.mounts.length === 0 ? (
              <EmptyState size="compact" icon={Folder02Icon} title="No mounts" />
            ) : (
              <ul className="divide-y divide-divider">
                {c.mounts.map((m) => (
                  <ListRow
                    key={`${m.source}->${m.destination}`}
                    leading={<Badge variant={m.dockerSocket ? "warning" : "outline"} className="w-16 justify-center">{m.type}</Badge>}
                    trailing={
                      <>
                        {m.dockerSocket && <Badge variant="warning">Docker socket</Badge>}
                        <Badge variant={m.readWrite ? "secondary" : "outline"} mono>
                          {m.readWrite ? "rw" : "ro"}
                        </Badge>
                      </>
                    }
                  >
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate font-mono text-mono text-foreground" title={m.destination}>
                        {m.destination}
                      </span>
                      <span className="truncate font-mono text-small text-muted-foreground" title={m.source}>
                        {m.type === "volume" && m.name ? m.name : m.source}
                      </span>
                    </span>
                  </ListRow>
                ))}
              </ul>
            )}
          </SectionCard>
        </TabsContent>

        <TabsContent value="config" className="grid items-start gap-3 lg:grid-cols-2">
          <HealthcheckCard container={c} />
          <SectionCard title="Command" icon={FileScriptIcon} description="Secret-looking arguments are redacted">
            <div className="flex flex-col gap-3">
              <CommandBlock label="Entrypoint" args={c.entrypoint} />
              <CommandBlock label="Command" args={c.command} />
            </div>
          </SectionCard>
          <SectionCard title="Environment" icon={ViewOffSlashIcon} description="Names only; values are never collected">
            {c.envKeys.length === 0 ? (
              <p className="text-control text-muted-foreground">No environment variables.</p>
            ) : (
              <ul className="flex flex-wrap gap-1.5">
                {c.envKeys.map((k) => (
                  <li key={k}>
                    <Badge variant="secondary" mono>
                      {k}
                    </Badge>
                  </li>
                ))}
              </ul>
            )}
          </SectionCard>
          <SectionCard
            title="Labels"
            icon={Tag01Icon}
            description={
              c.redactedLabelCount > 0
                ? `${Object.keys(c.labels).length} shown · ${c.redactedLabelCount} hidden because they look secret`
                : `${Object.keys(c.labels).length} labels`
            }
            flush
          >
            {Object.keys(c.labels).length === 0 ? (
              <EmptyState size="compact" icon={Tag01Icon} title="No labels" />
            ) : (
              <ScrollArea viewportClassName="max-h-[420px]" viewportProps={{ tabIndex: 0, "aria-label": "Labels" }}>
                <dl className="divide-y divide-divider">
                  {Object.entries(c.labels)
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([k, v]) => (
                      <div key={k} className="flex min-h-10 flex-col justify-center gap-0.5 px-4 py-1.5">
                        <dt className="truncate font-mono text-small text-muted-foreground" title={k}>
                          {k}
                        </dt>
                        <dd className="font-mono text-mono break-all text-foreground">{v || "—"}</dd>
                      </div>
                    ))}
                </dl>
              </ScrollArea>
            )}
          </SectionCard>
          <SectionCard title="Resources and security" icon={Settings02Icon} className="lg:col-span-2">
            <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-3 lg:grid-cols-6">
              <Fact label="Memory limit">{memLimit > 0 ? formatBytes(memLimit) : "unlimited"}</Fact>
              <Fact label="Memory reservation">
                {c.resources?.memReservationBytes ? formatBytes(c.resources.memReservationBytes) : "—"}
              </Fact>
              <Fact label="CPU limit">{c.resources?.cpuLimitCores ? `${c.resources.cpuLimitCores} cores` : "unlimited"}</Fact>
              <Fact label="CPU set">{c.resources?.cpusetCpus || "all"}</Fact>
              <Fact label="PIDs limit">{c.resources?.pidsLimit ? String(c.resources.pidsLimit) : "unlimited"}</Fact>
              <Fact label="User">{c.user || "root (image default)"}</Fact>
              <Fact label="Privileged" tone={c.privileged ? "warning" : undefined}>
                {c.privileged ? "yes" : "no"}
              </Fact>
              <Fact label="Read-only rootfs">{c.readOnlyRootfs ? "yes" : "no"}</Fact>
              <Fact label="Network mode">{c.networkMode || "—"}</Fact>
            </dl>
          </SectionCard>
        </TabsContent>

        <TabsContent value="events">
          <ContainerEvents entityId={c.id} />
        </TabsContent>

        <TabsContent value="logs">
          <SectionCard title="Logs" icon={FileScriptIcon} description={`Newest output of ${c.name}; load older lines from the top`}>
            <WorkloadLogs workloadId={c.id} limit={200} toolbar maxHeightClassName="max-h-[560px]" />
          </SectionCard>
        </TabsContent>
      </Tabs>

      {actions.dialog}
    </Page>
  );
}

function ChartBlock({ title, meta, children }: { title: string; meta: ReactNode; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-2">
      <div className="flex min-h-control-sm items-center justify-between gap-3">
        <h3 className="text-control font-medium text-text-2">{title}</h3>
        <span className="shrink-0 font-mono text-mono text-foreground tabular-nums">{meta}</span>
      </div>
      {children}
    </section>
  );
}

function Fact({ label, children, tone }: { label: string; children: ReactNode; tone?: "warning" | "critical" }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-small text-muted-foreground">{label}</dt>
      <dd className={cn("truncate font-mono text-mono text-foreground tabular-nums", tone && TONE_TEXT_CLASSES[tone])}>{children}</dd>
    </div>
  );
}

function FactsGrid({ container: c }: { container: Container }) {
  return (
    <dl className="grid grid-cols-2 gap-x-6 gap-y-3.5 sm:grid-cols-3 lg:grid-cols-6">
      <Fact label="Image">
        <span title={c.image}>{shortImage(c)}</span>
      </Fact>
      <Fact label="Image ID">{c.imageId.replace(/^sha256:/, "") || "—"}</Fact>
      <Fact label="Container ID">{c.containerId}</Fact>
      <Fact label="Role">{roleLabel(c.role)}</Fact>
      <Fact label="Status">
        <span title={c.status}>{c.status || "—"}</span>
      </Fact>
      <Fact label="PID">{c.pid > 0 ? c.pid : "—"}</Fact>
      <Fact label="Created">
        <span title={formatDateTime(c.createdAtMs)}>{c.createdAtMs > 0n ? formatRelativeTime(c.createdAtMs) : "—"}</span>
      </Fact>
      <Fact label="Started">
        <span title={formatDateTime(c.startedAtMs)}>{c.startedAtMs > 0n ? formatRelativeTime(c.startedAtMs) : "—"}</span>
      </Fact>
      <Fact label={isUp(c.state) ? "Uptime" : "Finished"}>
        {isUp(c.state) ? lifetimeText(c) : c.finishedAtMs > 0n ? formatRelativeTime(c.finishedAtMs) : "—"}
      </Fact>
      <Fact label="Restart policy">
        {c.restartPolicy || "no"}
        {c.restartPolicy === "on-failure" && c.restartMaxRetries > 0 ? ` (max ${c.restartMaxRetries})` : ""}
      </Fact>
      <Fact label="Restart count" tone={c.restartCount >= 5 ? "critical" : c.restartCount > 0 ? "warning" : undefined}>
        {c.restartCount}
      </Fact>
      <Fact label="Exit code" tone={c.exitCode !== 0 ? "critical" : undefined}>
        {c.state === ContainerState.RUNNING && c.exitCode === 0 ? "—" : c.exitCode}
      </Fact>
      <Fact label="OOM killed" tone={c.oomKilled ? "critical" : undefined}>
        {c.oomKilled ? "yes" : "no"}
      </Fact>
      <Fact label="Compose project">{c.composeProject || "—"}</Fact>
      <Fact label="Compose service">{c.composeService || "—"}</Fact>
      <Fact label="Working dir">
        <span title={c.composeWorkingDir}>{c.composeWorkingDir || "—"}</span>
      </Fact>
      {c.error && (
        <div className="col-span-full flex min-w-0 flex-col gap-0.5">
          <dt className="text-small text-muted-foreground">Docker error</dt>
          <dd className={cn("font-mono text-mono break-words", TONE_TEXT_CLASSES.critical)}>{c.error}</dd>
        </div>
      )}
    </dl>
  );
}

function CommandBlock({ label, args }: { label: string; args: readonly string[] }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-small text-muted-foreground">{label}</span>
      {args.length === 0 ? (
        <span className="text-control text-text-4">Image default</span>
      ) : (
        <code className="block rounded-tile bg-surface-subtle px-3 py-2 font-mono text-mono break-all whitespace-pre-wrap text-foreground ring-1 ring-inset ring-border">
          {args.map((a) => (/\s/.test(a) ? JSON.stringify(a) : a)).join(" ")}
        </code>
      )}
    </div>
  );
}

function HealthcheckCard({ container: c }: { container: Container }) {
  const hc = c.healthcheck;
  const status = healthStyle(c.health);
  return (
    <SectionCard
      title="Healthcheck"
      icon={HeartCheckIcon}
      actions={status ? <StatusPill tone={status.tone}>{status.label}</StatusPill> : undefined}
      description={hc && c.health !== ContainerHealth.NONE ? undefined : "No healthcheck configured"}
    >
      {!hc || c.health === ContainerHealth.NONE ? (
        <p className="text-control text-muted-foreground text-pretty">
          Docker only knows whether the process is running. Add a <code className="font-mono text-text-2">healthcheck</code> to the
          service to catch hung or degraded containers.
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          <CommandBlock label="Test" args={hc.test} />
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-3">
            <Fact label="Interval">{hc.intervalSeconds ? `${hc.intervalSeconds}s` : "—"}</Fact>
            <Fact label="Timeout">{hc.timeoutSeconds ? `${hc.timeoutSeconds}s` : "—"}</Fact>
            <Fact label="Retries">{hc.retries || "—"}</Fact>
            <Fact label="Failing streak" tone={hc.failingStreak > 0 ? "warning" : undefined}>
              {hc.failingStreak}
            </Fact>
            <Fact label="Last exit">{hc.lastCheckAtMs > 0n ? hc.lastExitCode : "—"}</Fact>
            <Fact label="Last check">{hc.lastCheckAtMs > 0n ? formatRelativeTime(hc.lastCheckAtMs) : "—"}</Fact>
          </dl>
          {hc.lastOutput && (
            <div className="flex flex-col gap-1.5">
              <span className="flex items-center gap-1.5 text-small text-muted-foreground">
                <StatusDot tone={hc.lastExitCode === 0 ? "healthy" : "critical"} />
                Last output (redacted)
              </span>
              <ScrollArea
                className="rounded-tile bg-surface-subtle ring-1 ring-inset ring-border"
                viewportClassName="max-h-40"
                viewportProps={{ tabIndex: 0, "aria-label": "Last health check output" }}
              >
                <code className="block px-3 py-2 font-mono text-mono break-words whitespace-pre-wrap text-foreground">
                  {hc.lastOutput}
                </code>
              </ScrollArea>
            </div>
          )}
        </div>
      )}
    </SectionCard>
  );
}

/** Events of one container, newest first, one server page of ListEvents at a time. */
function ContainerEvents({ entityId }: { entityId: string }) {
  const paged = usePagedSearch(CONTAINER_EVENTS_LIST);
  const query = useQuery({
    queryKey: paged.queryKey(["events", "entity", entityId]),
    queryFn: () => eventsClient.listEvents({ ...paged.request, entityId }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const events = query.data?.events ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "events");
  return (
    <SectionCard title="Events" icon={HeartCheckIcon} description="Lifecycle changes and audited actions for this container" flush>
      <PaginatedScrollArea
        status={query}
        isEmpty={events.length === 0}
        orientation="vertical"
        aria-label="Container events"
        pagination={pager}
        empty={
          <EmptyState size="compact" icon={HeartCheckIcon} title="No events yet" description="Starts, exits, health changes and actions show up here." />
        }
      >
        <div className="divide-y divide-divider">
          {events.map((e) => (
            <EventRow key={e.id} event={e} />
          ))}
        </div>
      </PaginatedScrollArea>
    </SectionCard>
  );
}
