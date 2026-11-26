import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Clock01Icon, CpuIcon, Download01Icon, HardDriveIcon, RamMemoryIcon, ServerStack01Icon, Upload01Icon } from "@hugeicons/core-free-icons";
import { type IconData } from "@/components/ui/icon";
import type { HostInfo } from "@/gen/asterism/v1/asterism_pb";
import { inventoryClient } from "@/lib/rpc";
import {
  ErrorState,
  LoadingState,
  MeterBar,
  Page,
  PageHeader,
  PropertyRow,
  SectionCard,
  StatCard,
  categoryLabel,
} from "@/components/common";
import { Card } from "@/components/ui/card";
import { UPlotChart } from "@/components/charts/UPlotChart";
import { RATIO_RANGE, formatRatioAxis } from "@/components/portal/chartFormat";
import { useHostHistory } from "@/components/portal/useHostHistory";
import { useLiveValue } from "@/live/LiveStore";
import { formatBps, formatBytes, formatPct, formatUptime } from "@/lib/format";

const CHART_HEIGHT = 180;
const SYNC_KEY = "host-vitals";

export function HostPage() {
  const overviewQuery = useQuery({
    queryKey: ["overview"],
    queryFn: () => inventoryClient.getOverview({}),
    refetchInterval: 15_000,
  });
  useHostHistory();

  const host = overviewQuery.data?.host;
  const liveCpu = useLiveValue("host", "cpu.util", host?.cpuUtil ?? 0);
  const liveMem = useLiveValue("host", "mem.used_bytes", Number(host?.memUsedBytes ?? 0n));
  const liveRx = useLiveValue("host", "net.rx_bps", host?.netRxBps ?? 0);
  const liveTx = useLiveValue("host", "net.tx_bps", host?.netTxBps ?? 0);

  const memTotal = Number(host?.memTotalBytes ?? 0n);
  const diskTotal = Number(host?.diskTotalBytes ?? 0n);
  const diskUsed = Number(host?.diskUsedBytes ?? 0n);
  const memRatio = memTotal > 0 ? liveMem / memTotal : 0;
  const diskRatio = diskTotal > 0 ? diskUsed / diskTotal : 0;

  return (
    <Page>
      <PageHeader
        title="Host vitals"
        description={
          host ? (
            <span className="tabular-nums">
              {host.hostname} · {host.os} · {host.arch} · {host.cores} cores
            </span>
          ) : (
            "CPU, memory, disk and network for this machine"
          )
        }
      />

      {overviewQuery.isError && !host ? (
        <Card>
          <ErrorState
            title="Couldn't load host details"
            error={overviewQuery.error}
            onRetry={() => void overviewQuery.refetch()}
            retrying={overviewQuery.isFetching}
          />
        </Card>
      ) : !host ? (
        <LoadingState variant="cards" label="Loading host vitals…" />
      ) : (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatCard
            label="CPU"
            icon={CpuIcon}
            value={formatPct(liveCpu)}
            sub={`load ${host.load1.toFixed(2)} / ${host.load5.toFixed(2)} / ${host.load15.toFixed(2)}`}
          >
            <MeterBar size="lg" value={liveCpu} label="CPU utilisation" />
          </StatCard>
          <StatCard
            label="Memory"
            icon={RamMemoryIcon}
            value={formatBytes(liveMem)}
            sub={`${Math.round(memRatio * 100)}% of ${formatBytes(memTotal)}`}
          >
            <MeterBar size="lg" hatch value={memRatio} label="Memory used" />
          </StatCard>
          <StatCard
            label="Root disk"
            icon={HardDriveIcon}
            value={formatBytes(diskUsed)}
            sub={`${Math.round(diskRatio * 100)}% of ${formatBytes(diskTotal)}`}
          >
            <MeterBar size="lg" hatch value={diskRatio} label="Root disk used" />
          </StatCard>
          <StatCard
            label="Uptime"
            icon={Clock01Icon}
            value={formatUptime(host.uptimeSeconds)}
            sub={host.bootTimeMs > 0n ? `since ${new Date(Number(host.bootTimeMs)).toLocaleDateString(undefined, { dateStyle: "medium" })}` : undefined}
          />
        </div>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        <ChartCard title="CPU utilisation" icon={CpuIcon} value={formatPct(liveCpu)}>
          <UPlotChart
            entityId="host"
            metric="cpu.util"
            colorVar="--chart-1"
            height={CHART_HEIGHT}
            syncKey={SYNC_KEY}
            yRange={RATIO_RANGE}
            valueFormatter={formatRatioAxis}
          />
        </ChartCard>
        <ChartCard title="Memory used" icon={RamMemoryIcon} value={formatBytes(liveMem)}>
          <UPlotChart
            entityId="host"
            metric="mem.used_bytes"
            colorVar="--chart-3"
            height={CHART_HEIGHT}
            syncKey={SYNC_KEY}
            valueFormatter={formatBytes}
          />
        </ChartCard>
        <ChartCard title="Network in" icon={Download01Icon} value={formatBps(liveRx)}>
          <UPlotChart
            entityId="host"
            metric="net.rx_bps"
            colorVar="--chart-2"
            height={CHART_HEIGHT}
            syncKey={SYNC_KEY}
            valueFormatter={formatBps}
          />
        </ChartCard>
        <ChartCard title="Network out" icon={Upload01Icon} value={formatBps(liveTx)}>
          <UPlotChart
            entityId="host"
            metric="net.tx_bps"
            colorVar="--chart-4"
            height={CHART_HEIGHT}
            syncKey={SYNC_KEY}
            valueFormatter={formatBps}
          />
        </ChartCard>
      </div>

      {host && <SystemCard host={host} />}
    </Page>
  );
}

function ChartCard({
  title,
  icon,
  value,
  children,
}: {
  title: string;
  icon: IconData;
  value: string;
  children: ReactNode;
}) {
  return (
    <SectionCard
      title={title}
      icon={icon}
      description="Last 15 minutes"
      actions={<span className="font-heading text-heading text-foreground tabular-nums">{value}</span>}
    >
      {children}
    </SectionCard>
  );
}

function SystemCard({ host }: { host: HostInfo }) {
  const swapTotal = Number(host.swapTotalBytes);
  const swapUsed = Number(host.swapUsedBytes);
  return (
    <SectionCard title="System" icon={ServerStack01Icon} description="Hardware and kernel as reported by the collector" flush>
      <div className="grid md:grid-cols-2 md:divide-x md:divide-divider">
        <div className="divide-y divide-divider">
          <PropertyRow label="Hostname" mono>{host.hostname}</PropertyRow>
          <PropertyRow label="CPU model" mono>{host.cpuModel || "—"}</PropertyRow>
          <PropertyRow label="Cores" mono>{host.cores}</PropertyRow>
          <PropertyRow label="CPU steal" mono>{formatPct(host.cpuStealUtil)}</PropertyRow>
        </div>
        <div className="divide-y divide-divider border-t border-divider md:border-t-0">
          <PropertyRow label="Operating system" mono>{host.os} · {host.arch}</PropertyRow>
          <PropertyRow label="Kernel" mono>{host.kernel || "—"}</PropertyRow>
          <PropertyRow label="Swap" mono>
            {swapTotal > 0 ? `${formatBytes(swapUsed)} of ${formatBytes(swapTotal)}` : "None"}
          </PropertyRow>
          <PropertyRow label="Collector">
            {categoryLabel(host.sourceMode || "local")}
            {host.sshTarget && <span className="font-mono text-mono text-muted-foreground"> via {host.sshTarget}</span>}
          </PropertyRow>
        </div>
      </div>
    </SectionCard>
  );
}
