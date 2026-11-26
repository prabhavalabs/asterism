import { Link } from "@tanstack/react-router";
import { ServerStack01Icon } from "@hugeicons/core-free-icons";
import type { HostInfo } from "@/gen/asterism/v1/asterism_pb";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { MeterBar, StatusDot, TONE_TEXT_CLASSES, type StatusTone } from "@/components/common";
import { agentChipText, agentState, agentStateStyle, agentTooltipText, lastSeenText } from "@/components/agents/model";
import { Icon } from "@/components/ui/icon";
import { Skeleton } from "@/components/ui/skeleton";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import { useLiveConnected, useLiveValue } from "@/live/LiveStore";
import { formatBytes, formatPct, formatUptime } from "@/lib/format";
import { cn } from "@/lib/utils";

interface HostStatusCardProps {
  host?: HostInfo;
  /** Agent mode: the agent feeding this host. A stale or offline agent turns the card amber or red. */
  agent?: AgentStatus;
}

/**
 * Sidebar footer card: the observed host with a live dot, cores, uptime and
 * a live memory meter (4 Hz via useLiveValue). Links to Host Vitals. On the
 * icon rail it shrinks to a server icon with the live dot and a tooltip.
 * In agent mode the status reads "via agent <name> · <lag>"; when the agent
 * is stale or offline the dot and status follow it instead of the stream,
 * and the card is one row shorter so the extra Agents nav item still fits.
 */
export function HostStatusCard({ host, agent }: HostStatusCardProps) {
  const connected = useLiveConnected();
  const { isMobile, setOpenMobile } = useSidebar();
  const memTotal = Number(host?.memTotalBytes ?? 0n);
  const memUsed = useLiveValue("host", "mem.used_bytes", Number(host?.memUsedBytes ?? 0n));
  const ratio = memTotal > 0 ? memUsed / memTotal : 0;

  if (!host) {
    return (
      <div
        className="flex flex-col gap-1.5 rounded-card bg-card px-3 py-2.5 shadow-card group-data-[collapsible=icon]:hidden"
        aria-busy="true"
      >
        <Skeleton className="h-3.5 w-32" />
        <Skeleton className="h-3 w-24" />
        <Skeleton className="h-1.5 w-full" />
      </div>
    );
  }

  const hostname = host.hostname || "host";
  const agentDown = agent !== undefined && agentState(agent) !== "connected";
  const agentStyle = agent ? agentStateStyle(agent) : undefined;
  const tone: StatusTone = agentDown && agentStyle ? agentStyle.tone : connected ? "healthy" : "warning";
  const status = agentDown && agentStyle ? agentStyle.label : connected ? "Live" : "Offline";
  const live = connected && !agentDown;
  const agentLine = agent
    ? agentDown
      ? agentState(agent) === "never"
        ? status
        : `${status} ${lastSeenText(agent)}`
      : agentChipText(agent)
    : undefined;
  const close = () => isMobile && setOpenMobile(false);

  return (
    <>
      <SidebarMenu className="hidden group-data-[collapsible=icon]:flex">
        <SidebarMenuItem>
          <SidebarMenuButton
            asChild
            tooltip={`${hostname} · ${status} · memory ${formatPct(ratio)}${agent ? ` · ${agentTooltipText(agent)}` : ""}`}
            className="relative"
          >
            <Link to="/host" onClick={close} aria-label={`Host ${hostname}, ${status}. Open host vitals`}>
              <Icon icon={ServerStack01Icon} />
              <StatusDot data-rail-keep="" tone={tone} pulse={live} className="absolute top-1.5 right-1.5" />
            </Link>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>

      <Link
        to="/host"
        onClick={close}
        className="group/host flex flex-col gap-1.5 rounded-card bg-card px-3 py-2.5 shadow-card outline-none transition-colors hover:bg-muted/60 focus-visible:ring-1 focus-visible:ring-ring group-data-[collapsible=icon]:hidden"
        aria-label={`Host ${hostname}, ${status.toLowerCase()}${agent ? `, ${agentTooltipText(agent)}` : ""}. Open host vitals`}
      >
        <div className="flex min-w-0 items-center gap-2">
          <StatusDot tone={tone} pulse={live} />
          <span className="min-w-0 shrink truncate text-control font-medium text-foreground">{hostname}</span>
          <span
            className={cn(
              "ml-auto min-w-0 truncate text-small tabular-nums",
              agentDown ? TONE_TEXT_CLASSES[tone] : "text-muted-foreground",
            )}
          >
            {agentLine ?? status}
          </span>
        </div>
        {agent ? (
          // Agent mode adds a sidebar item, so the card drops the cores/uptime
          // row (they stay on Host vitals) and puts memory on one line.
          <div className="flex min-w-0 items-center gap-2 text-small tabular-nums">
            <MeterBar
              value={ratio}
              hatch
              className="min-w-8 flex-1"
              label="Host memory used"
              valueText={`${formatBytes(memUsed)} of ${formatBytes(memTotal)}`}
            />
            <span className="shrink-0 font-mono text-text-2">
              {formatBytes(memUsed)} / {formatBytes(memTotal)}
            </span>
          </div>
        ) : (
          <>
            <div className="flex items-center gap-1.5 font-mono text-small text-muted-foreground tabular-nums">
              <span>{host.cores} cores</span>
              {host.uptimeSeconds > 0n && (
                <>
                  <span aria-hidden="true" className="text-text-4">
                    ·
                  </span>
                  <span>up {formatUptime(host.uptimeSeconds)}</span>
                </>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between gap-2 text-small tabular-nums">
                <span className="text-muted-foreground">Memory</span>
                <span className="font-mono text-text-2">
                  {formatBytes(memUsed)} / {formatBytes(memTotal)}
                </span>
              </div>
              <MeterBar
                value={ratio}
                hatch
                label="Host memory used"
                valueText={`${formatBytes(memUsed)} of ${formatBytes(memTotal)}`}
              />
            </div>
          </>
        )}
      </Link>
    </>
  );
}
