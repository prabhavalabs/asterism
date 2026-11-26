import { useState } from "react";
import { Activity01Icon } from "@hugeicons/core-free-icons";
import { SectionCard, SegmentedControl, StatusDot } from "@/components/common";
import { liveIndicator } from "@/components/agents/model";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { UPlotChart } from "@/components/charts/UPlotChart";
import { liveStore, useLiveConnected, useLiveValue } from "@/live/LiveStore";
import { formatBps, formatBytes, formatPct } from "@/lib/format";
import { RATIO_RANGE, formatRatioAxis } from "./chartFormat";
import { useHostHistory } from "./useHostHistory";

type TelemetryTab = "cpu" | "memory" | "network";

interface TabConfig {
  metric: string;
  colorVar: string;
  label: string;
  format: (value: number) => string;
  axis: (value: number) => string;
  range?: [number, number];
}

const TABS: Record<TelemetryTab, TabConfig> = {
  cpu: {
    metric: "cpu.util",
    colorVar: "--chart-1",
    label: "CPU utilisation",
    format: formatPct,
    axis: formatRatioAxis,
    range: RATIO_RANGE,
  },
  memory: {
    metric: "mem.used_bytes",
    colorVar: "--chart-3",
    label: "Memory used",
    format: formatBytes,
    axis: formatBytes,
  },
  network: {
    metric: "net.rx_bps",
    colorVar: "--chart-2",
    label: "Network in",
    format: formatBps,
    axis: formatBps,
  },
};

const TAB_OPTIONS = [
  { value: "cpu", label: "CPU" },
  { value: "memory", label: "Memory" },
  { value: "network", label: "Network" },
] as const;

const COLOR_SWATCH: Record<TelemetryTab, string> = {
  cpu: "bg-chart-1",
  memory: "bg-chart-3",
  network: "bg-chart-2",
};

interface HostTelemetryCardProps {
  fallbackCpu: number;
  fallbackMemBytes: number;
  memTotalBytes: number;
  fallbackNetRx: number;
  /** Primary agent, in agent mode only: its freshness drives the Live indicator. */
  agent?: AgentStatus;
  className?: string;
}

/** Card subtitle for each live state; matches the Topbar pill label. */
function telemetryStatusText(live: ReturnType<typeof liveIndicator>): string {
  switch (live.kind) {
    case "live":
      return "Live · 1 s resolution · last 15 min";
    case "reconnecting":
      return "Reconnecting to the live stream…";
    case "stale":
    case "no-data":
      return `${live.label} · ${live.detail ?? "not receiving data"}`;
  }
}

/** One live host chart with CPU / Memory / Network tabs and now, average and peak readouts. */
export function HostTelemetryCard({
  fallbackCpu,
  fallbackMemBytes,
  memTotalBytes,
  fallbackNetRx,
  agent,
  className,
}: HostTelemetryCardProps) {
  const [tab, setTab] = useState<TelemetryTab>("cpu");
  const live = liveIndicator(useLiveConnected(), agent);
  useHostHistory();

  const liveCpu = useLiveValue("host", "cpu.util", fallbackCpu);
  const liveMem = useLiveValue("host", "mem.used_bytes", fallbackMemBytes);
  const liveRx = useLiveValue("host", "net.rx_bps", fallbackNetRx);

  const config = TABS[tab];
  const current = tab === "cpu" ? liveCpu : tab === "memory" ? liveMem : liveRx;

  // Read the ring buffer at the 4 Hz render cadence; nothing is copied into state.
  const values = liveStore.getSeries("host", config.metric).values;
  let sum = 0;
  let peak = -Infinity;
  for (const v of values) {
    sum += v;
    if (v > peak) peak = v;
  }
  const avg = values.length > 0 ? sum / values.length : current;
  if (values.length === 0) peak = current;

  return (
    <SectionCard
      title="Host telemetry"
      icon={Activity01Icon}
      className={className}
      description={
        <span className="inline-flex items-center gap-1.5" data-live-state={live.kind} title={live.description}>
          <StatusDot tone={live.tone} pulse={live.pulse} />
          <span className="tabular-nums">{telemetryStatusText(live)}</span>
        </span>
      }
      actions={
        <SegmentedControl
          size="sm"
          aria-label="Telemetry metric"
          value={tab}
          onValueChange={setTab}
          options={TAB_OPTIONS}
          className="hidden sm:inline-flex"
        />
      }
    >
      <div className="flex flex-col gap-4">
        <SegmentedControl
          aria-label="Telemetry metric"
          value={tab}
          onValueChange={setTab}
          options={TAB_OPTIONS}
          fullWidth
          className="sm:hidden"
        />
        <dl className="flex flex-wrap items-end gap-x-6 gap-y-3 sm:gap-x-10">
          <div className="flex min-w-0 flex-col gap-1">
            <dt className="flex items-center gap-1.5 text-small text-muted-foreground">
              <span aria-hidden="true" className={`size-2 rounded-xs ${COLOR_SWATCH[tab]}`} />
              <span className="hidden sm:inline">{config.label} ·</span> now
              {tab === "memory" && memTotalBytes > 0 && (
                <span className="tabular-nums"> of {formatBytes(memTotalBytes)}</span>
              )}
            </dt>
            <dd className="font-heading text-kpi tracking-[-0.02em] text-foreground tabular-nums">
              {config.format(current)}
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-small text-muted-foreground">Average</dt>
            <dd className="font-heading text-title font-medium tracking-[-0.02em] text-text-2 tabular-nums">
              {config.format(avg)}
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-small text-muted-foreground">Peak</dt>
            <dd className="font-heading text-title font-medium tracking-[-0.02em] text-text-2 tabular-nums">
              {config.format(peak)}
            </dd>
          </div>
        </dl>

        <UPlotChart
          entityId="host"
          metric={config.metric}
          colorVar={config.colorVar}
          height={240}
          syncKey="overview-telemetry"
          yRange={config.range}
          valueFormatter={config.axis}
        />
      </div>
    </SectionCard>
  );
}
