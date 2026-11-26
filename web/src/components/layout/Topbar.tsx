import { StatusPill } from "@/components/common";
import { AgentChip } from "@/components/agents/AgentChip";
import { liveIndicator } from "@/components/agents/model";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useLiveConnected } from "@/live/LiveStore";
import { Breadcrumbs } from "./Breadcrumbs";

/**
 * Live data state (see liveIndicator): green "Live" while the metrics stream
 * is connected and, in agent mode, the agent is reporting; amber "Stale"
 * while the agent reconnects; neutral "No data" while it is offline.
 */
function LiveStatus({ hostname, agent }: { hostname?: string; agent?: AgentStatus }) {
  const live = liveIndicator(useLiveConnected(), agent, hostname);

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="shrink-0 outline-none focus-visible:ring-1 focus-visible:ring-ring">
          <StatusPill tone={live.tone} pulse={live.pulse} aria-label={live.description}>
            {live.label}
          </StatusPill>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" align="end">
        {live.description}
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * The 48px bar above every page: sidebar toggle, breadcrumbs, the agent
 * chip (agent mode) and live status.
 */
export function Topbar({ hostname, agent }: { hostname?: string; agent?: AgentStatus }) {
  return (
    <header className="sticky top-0 z-20 flex h-topbar shrink-0 items-center gap-2 border-b border-border bg-background px-2 md:px-3">
      <SidebarTrigger />
      <Separator orientation="vertical" className="mr-1 h-4 self-center! bg-border" />
      <Breadcrumbs />
      <div className="flex shrink-0 items-center gap-2">
        {agent && <AgentChip agent={agent} />}
        <LiveStatus hostname={hostname} agent={agent} />
      </div>
    </header>
  );
}
