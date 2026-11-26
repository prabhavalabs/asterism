import { Link } from "@tanstack/react-router";
import { StatusPill } from "@/components/common/StatusPill";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { cn } from "@/lib/utils";
import { agentChipText, agentState, agentStateStyle, agentTooltipText } from "./model";

/**
 * Topbar chip in agent mode: "via agent prod-1 · 5 ms" while connected,
 * amber while stale and red while offline. Links to Settings → Agents.
 */
export function AgentChip({ agent, className }: { agent: AgentStatus; className?: string }) {
  const { tone } = agentStateStyle(agent);
  const state = agentState(agent);
  const connected = state === "connected";
  const short = connected ? "Agent" : state === "never" ? "No agent" : `Agent ${state}`;
  const description = agentTooltipText(agent);

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Link
          to="/settings"
          search={{ section: "agents" }}
          aria-label={`${description}. Open agent settings`}
          className={cn("shrink-0 rounded-chip outline-none focus-visible:ring-1 focus-visible:ring-ring", className)}
        >
          <StatusPill tone={tone} className="tabular-nums">
            <span className="sm:hidden">{short}</span>
            <span className="hidden max-w-64 truncate sm:inline">{agentChipText(agent)}</span>
          </StatusPill>
        </Link>
      </TooltipTrigger>
      <TooltipContent side="bottom" align="end" className="max-w-80">
        {description}
      </TooltipContent>
    </Tooltip>
  );
}
