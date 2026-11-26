import { useEffect, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight02Icon, ArrowUpRight01Icon, CpuIcon, RamMemoryIcon, SecurityCheckIcon, ViewOffSlashIcon } from "@hugeicons/core-free-icons";
import { Link } from "@tanstack/react-router";
import { Icon } from "@/components/ui/icon";
import { WorkloadKind, type TopologyEdge } from "@/gen/asterism/v1/asterism_pb";
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  DomainChip,
  ErrorState,
  LoadingState,
  MeterBar,
  PortChip,
  StatCard,
  StatusDot,
  StatusPill,
  ProxyKindLabel,
  TechIcon,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { WorkloadLogs } from "@/components/portal/WorkloadLogs";
import { UPlotChart } from "@/components/charts/UPlotChart";
import { inventoryClient, metricsClient } from "@/lib/rpc";
import { liveStore, useLiveValue } from "@/live/LiveStore";
import {
  exposureLabel,
  formatBps,
  formatBytes,
  formatPct,
  formatRelativeTime,
  formatUptime,
  roleLabel,
} from "@/lib/format";
import { cn } from "@/lib/utils";
import { IsometricBlockSvg } from "./IsometricBlockSvg";
import { edgeColorVar, edgeKindLabel } from "./roles";

interface WorkloadInspectorSheetProps {
  workloadId: string | null;
  onClose: () => void;
}

// Module-level so the charts never see new function identities.
const formatCpuTick = (v: number) => `${Math.round(v * 100)}%`;
const formatMemTick = (v: number) => formatBytes(v);
const CPU_RANGE: [number, number] = [0, 1];

const CHART_HEIGHT = 160;

function kindLabel(kind: WorkloadKind): string {
  switch (kind) {
    case WorkloadKind.CONTAINER:
      return "Container";
    case WorkloadKind.SYSTEMD_UNIT:
      return "systemd unit";
    case WorkloadKind.PROCESS:
      return "Process";
    default:
      return "Workload";
  }
}

function shortId(id: string): string {
  return id.replace(/^(ctr|unit|svc|proc):/, "").replace(/\.service$/, "");
}

function SectionHeading({ children, meta }: { children: ReactNode; meta?: ReactNode }) {
  return (
    <div className="mb-3 flex min-h-control-sm items-center justify-between gap-3">
      <h3 className="text-body font-semibold text-foreground">{children}</h3>
      {meta !== undefined && (
        <span className="shrink-0 font-mono text-control text-muted-foreground tabular-nums">{meta}</span>
      )}
    </div>
  );
}

export function WorkloadInspectorSheet({ workloadId, onClose }: WorkloadInspectorSheetProps) {
  const detailQuery = useQuery({
    queryKey: ["workload", workloadId],
    enabled: Boolean(workloadId),
    queryFn: () => inventoryClient.getWorkload({ id: workloadId ?? "" }),
  });

  // Seed recent history into the LiveStore so the charts start filled.
  useEffect(() => {
    if (!workloadId) return;
    let active = true;
    void metricsClient
      .queryRange({
        entityId: workloadId,
        metrics: ["cpu.util", "mem.used_bytes"],
        fromMs: BigInt(Date.now() - 120_000),
        toMs: BigInt(Date.now()),
      })
      .then((res) => {
        if (!active) return;
        for (const s of res.series) {
          liveStore.seed(
            s.entityId,
            s.metric,
            s.points.map((pt) => ({ tsMs: Number(pt.tsMs), value: pt.value })),
          );
        }
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [workloadId]);

  const wl = detailQuery.data?.workload;
  const liveCpu = useLiveValue(wl?.id ?? "", "cpu.util", wl?.cpuUtil ?? 0);
  const liveMem = useLiveValue(wl?.id ?? "", "mem.used_bytes", Number(wl?.memUsedBytes ?? 0n));
  const memLimit = Number(wl?.memLimitBytes ?? 0n);
  const memRatio = memLimit > 0 ? Math.min(1, liveMem / memLimit) : 0;

  const routes = detailQuery.data?.ingressRoutes ?? [];
  const edges = detailQuery.data?.edges ?? [];
  const events = detailQuery.data?.recentEvents ?? [];

  return (
    <Sheet open={Boolean(workloadId)} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="sm:max-w-xl">
        {wl ? (
          <>
            <SheetHeader className="gap-3">
              <div className="flex items-start gap-4">
                <IsometricBlockSvg role={wl.role} replicas={wl.replicas} size={56} className="shrink-0" />
                <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                  <SheetTitle className="flex min-w-0 items-center gap-2 text-title">
                    {wl.tech && <TechIcon slug={wl.tech} role={wl.role} image={wl.image} size={24} />}
                    <span className="truncate">{wl.name}</span>
                  </SheetTitle>
                  <SheetDescription className="truncate font-mono" title={wl.image || wl.id}>
                    {wl.image || wl.id}
                  </SheetDescription>
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-1.5">
                <StatusPill health={wl.health} />
                <Badge variant="secondary">{roleLabel(wl.role)}</Badge>
                <Badge variant="outline">{kindLabel(wl.kind)}</Badge>
                {wl.replicas > 1 && (
                  <Badge variant="outline" mono>
                    {wl.replicas} replicas
                  </Badge>
                )}
                {wl.kind !== WorkloadKind.CONTAINER && (
                  <Badge variant="outline">
                    <Icon icon={SecurityCheckIcon} className="text-status-healthy" />
                    Read-only
                  </Badge>
                )}
                {wl.kind === WorkloadKind.CONTAINER && (
                  <Button variant="outline" size="sm" className="ml-auto" asChild>
                    <Link to="/containers/$containerId" params={{ containerId: shortId(wl.id) }} onClick={onClose}>
                      Container details
                      <Icon icon={ArrowUpRight01Icon} data-icon="inline-end" />
                    </Link>
                  </Button>
                )}
              </div>
            </SheetHeader>

            <SheetBody className="flex flex-col gap-4">
              <div className="grid grid-cols-2 gap-3">
                <StatCard
                  label="CPU"
                  icon={CpuIcon}
                  value={formatPct(liveCpu)}
                  sub={wl.restartCount > 0 ? `${wl.restartCount} restarts` : "No restarts"}
                />
                <StatCard
                  label="Memory"
                  icon={RamMemoryIcon}
                  value={formatBytes(liveMem)}
                  sub={memLimit > 0 ? `of ${formatBytes(memLimit)} · ${formatPct(memRatio, 0)}` : "No limit set"}
                >
                  {memLimit > 0 && (
                    <MeterBar value={memRatio} label="Memory used" valueText={`${formatPct(memRatio, 0)} of limit`} />
                  )}
                </StatCard>
              </div>

              <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-control sm:grid-cols-3">
                <Fact label="Started">
                  {wl.startedAtMs > 0n ? formatRelativeTime(wl.startedAtMs) : "—"}
                </Fact>
                <Fact label="Uptime">
                  {wl.startedAtMs > 0n
                    ? formatUptime(Math.max(0, (Date.now() - Number(wl.startedAtMs)) / 1000))
                    : "—"}
                </Fact>
                <Fact label="PID">{wl.pid > 0 ? wl.pid : "—"}</Fact>
                <Fact label="Net in">{formatBps(wl.netRxBps)}</Fact>
                <Fact label="Net out">{formatBps(wl.netTxBps)}</Fact>
                <Fact label="State">{wl.state || "—"}</Fact>
              </dl>

              <Tabs defaultValue="metrics" className="gap-4">
                <TabsList className="w-full">
                  <TabsTrigger value="metrics" className="flex-1">
                    Metrics
                  </TabsTrigger>
                  <TabsTrigger value="network" className="flex-1">
                    Network
                  </TabsTrigger>
                  <TabsTrigger value="logs" className="flex-1">
                    Logs
                  </TabsTrigger>
                  <TabsTrigger value="config" className="flex-1">
                    Config
                  </TabsTrigger>
                </TabsList>

                <TabsContent value="metrics" className="flex flex-col gap-5">
                  <section>
                    <SectionHeading meta={formatPct(liveCpu)}>CPU utilisation</SectionHeading>
                    <UPlotChart
                      entityId={wl.id}
                      metric="cpu.util"
                      colorVar="--chart-1"
                      height={CHART_HEIGHT}
                      syncKey="inspector"
                      yRange={CPU_RANGE}
                      valueFormatter={formatCpuTick}
                      ariaLabel={`${wl.name} CPU utilisation`}
                    />
                  </section>
                  <section>
                    <SectionHeading meta={formatBytes(liveMem)}>Memory</SectionHeading>
                    <UPlotChart
                      entityId={wl.id}
                      metric="mem.used_bytes"
                      colorVar="--chart-5"
                      height={CHART_HEIGHT}
                      syncKey="inspector"
                      valueFormatter={formatMemTick}
                      ariaLabel={`${wl.name} memory usage`}
                    />
                  </section>
                  {events.length > 0 && (
                    <section>
                      <SectionHeading>Recent events</SectionHeading>
                      <ul className="flex flex-col divide-y divide-divider rounded-card bg-card shadow-card">
                        {events.slice(0, 5).map((ev) => (
                          <li key={ev.id} className="flex items-start gap-3 px-4 py-3">
                            <StatusPill severity={ev.severity} className="mt-0.5" />
                            <span className="min-w-0 flex-1 text-control text-foreground">{ev.message}</span>
                            <span className="shrink-0 text-small text-muted-foreground tabular-nums">
                              {formatRelativeTime(ev.tsMs)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    </section>
                  )}
                </TabsContent>

                <TabsContent value="network" className="flex flex-col gap-5">
                  <section>
                    <SectionHeading meta={wl.ports.length}>Open ports</SectionHeading>
                    {wl.ports.length === 0 ? (
                      <p className="text-control text-muted-foreground">No listening ports.</p>
                    ) : (
                      <ul className="flex flex-col divide-y divide-divider rounded-card bg-card shadow-card">
                        {wl.ports.map((pb) => (
                          <li key={pb.id} className="flex min-h-10 items-center gap-3 px-4 py-1.5">
                            <PortChip
                              port={pb.containerPort || pb.hostPort}
                              hostPort={pb.hostPort || undefined}
                              exposure={pb.exposure}
                              protocol={pb.proto}
                            />
                            <span className="min-w-0 flex-1 truncate font-mono text-control text-muted-foreground tabular-nums">
                              {pb.hostPort > 0
                                ? `${pb.hostIp || "0.0.0.0"}:${pb.hostPort}`
                                : `:${pb.containerPort} (not published)`}
                            </span>
                            <span className="shrink-0 text-control text-text-2">{exposureLabel(pb.exposure)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>

                  {routes.length > 0 && (
                    <section>
                      <SectionHeading meta={routes.length}>Ingress and load balancing</SectionHeading>
                      <div className="flex flex-col gap-3">
                        {routes.map((route) => (
                          <div key={route.id} className="flex flex-col gap-3 rounded-card bg-card p-4 shadow-card">
                            <div className="flex flex-wrap items-center gap-1.5">
                              {route.hostnames.map((h) => (
                                <DomainChip key={h} domain={h} tls={route.tls} />
                              ))}
                              <Badge variant="outline">
                                <ProxyKindLabel kind={route.proxyKind} />
                              </Badge>
                              {route.strategy && (
                                <Badge variant="info" mono>
                                  {route.strategy}
                                </Badge>
                              )}
                            </div>
                            <ul className="flex flex-col gap-1.5 font-mono text-control tabular-nums">
                              {route.upstreams.map((u, i) => (
                                <li key={`${u.address}-${i}`} className="flex items-center gap-2">
                                  <StatusDot tone={u.healthy ? "healthy" : "critical"} />
                                  <span className="min-w-0 flex-1 truncate text-foreground">{u.address}</span>
                                  {u.backup && <Badge variant="outline">backup</Badge>}
                                  <span className="shrink-0 text-muted-foreground">weight {u.weight}</span>
                                </li>
                              ))}
                            </ul>
                          </div>
                        ))}
                      </div>
                    </section>
                  )}

                  <section>
                    <SectionHeading meta={edges.length}>Connections</SectionHeading>
                    {edges.length === 0 ? (
                      <p className="text-control text-muted-foreground">No observed or configured connections.</p>
                    ) : (
                      <ul className="flex flex-col divide-y divide-divider rounded-card bg-card shadow-card">
                        {edges.map((edge) => (
                          <ConnectionRow key={edge.id} edge={edge} selfId={wl.id} />
                        ))}
                      </ul>
                    )}
                  </section>
                </TabsContent>

                <TabsContent value="logs" className="flex flex-col gap-3">
                  <WorkloadLogs workloadId={wl.id} />
                </TabsContent>

                <TabsContent value="config" className="flex flex-col gap-5">
                  <section>
                    <SectionHeading meta={wl.envKeys.length}>Environment variables</SectionHeading>
                    <p className="mb-3 flex items-center gap-2 text-small text-muted-foreground">
                      <Icon icon={ViewOffSlashIcon} className="size-4 shrink-0" />
                      Names only. Values are never read or shown.
                    </p>
                    {wl.envKeys.length === 0 ? (
                      <p className="text-control text-muted-foreground">None.</p>
                    ) : (
                      <ul className="flex flex-wrap gap-1.5">
                        {wl.envKeys.map((k) => (
                          <li key={k}>
                            <Badge variant="secondary" mono>
                              {k}
                            </Badge>
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                  <section>
                    <SectionHeading meta={wl.networks.length}>Networks</SectionHeading>
                    {wl.networks.length === 0 ? (
                      <p className="text-control text-muted-foreground">None.</p>
                    ) : (
                      <ul className="flex flex-wrap gap-1.5">
                        {wl.networks.map((net) => (
                          <li key={net}>
                            <Badge variant="outline" mono>
                              {net}
                            </Badge>
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                  {wl.serviceName && (
                    <section>
                      <SectionHeading>Identity</SectionHeading>
                      <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-control">
                        <Fact label="Service">{wl.serviceName}</Fact>
                        <Fact label="ID">{wl.id}</Fact>
                      </dl>
                    </section>
                  )}
                </TabsContent>
              </Tabs>
            </SheetBody>
          </>
        ) : detailQuery.isError ? (
          <>
            <SheetHeader>
              <SheetTitle>Workload</SheetTitle>
            </SheetHeader>
            <ErrorState
              error={detailQuery.error}
              onRetry={() => void detailQuery.refetch()}
              retrying={detailQuery.isFetching}
            />
          </>
        ) : (
          <>
            <SheetHeader>
              <SheetTitle>Workload</SheetTitle>
              <SheetDescription className="sr-only">Loading workload details</SheetDescription>
            </SheetHeader>
            <LoadingState label="Loading workload details…" />
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-small text-muted-foreground">{label}</dt>
      <dd className="truncate font-mono text-control text-foreground tabular-nums">{children}</dd>
    </div>
  );
}

function ConnectionRow({ edge, selfId }: { edge: TopologyEdge; selfId: string }) {
  const outgoing = edge.sourceId === selfId;
  return (
    <li className="flex min-h-10 items-center gap-3 px-4 py-1.5">
      <span
        aria-hidden="true"
        className="h-0.5 w-4 shrink-0 rounded-full"
        style={{ backgroundColor: edgeColorVar(edge.edgeKind) }}
      />
      <span className="flex min-w-0 flex-1 items-center gap-1.5 font-mono text-control">
        <span className={cn("truncate", outgoing ? "text-muted-foreground" : "text-foreground")}>
          {shortId(edge.sourceId)}
        </span>
        <Icon icon={ArrowRight02Icon} className="size-3.5 shrink-0 text-muted-foreground" />
        <span className={cn("truncate", outgoing ? "text-foreground" : "text-muted-foreground")}>
          {shortId(edge.targetId)}
        </span>
      </span>
      <span className="shrink-0 text-small text-muted-foreground tabular-nums">
        {edgeKindLabel(edge.edgeKind)}
        {edge.port > 0 ? ` :${edge.port}` : ""} · {edge.connCount} conn
      </span>
    </li>
  );
}
