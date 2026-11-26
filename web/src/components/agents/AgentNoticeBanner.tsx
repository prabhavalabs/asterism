import { Link } from "@tanstack/react-router";
import { Alert02Icon, InformationCircleIcon, WifiDisconnected01Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { cn } from "@/lib/utils";
import { agentNotice } from "./model";

const TONE_CLASSES = {
  warning: { box: "bg-status-warning-surface ring-status-warning/25", icon: "text-status-warning", glyph: WifiDisconnected01Icon },
  critical: { box: "bg-status-critical-surface ring-status-critical/25", icon: "text-status-critical", glyph: Alert02Icon },
  neutral: { box: "bg-surface-subtle ring-border", icon: "text-text-4", glyph: InformationCircleIcon },
} as const;

/**
 * Overview strip when the primary agent is not connected: amber while it
 * reconnects, red once offline (the data shown is its last report), neutral
 * before it ever connects. Renders nothing while connected.
 */
export function AgentNoticeBanner({ agent, className }: { agent: AgentStatus | undefined; className?: string }) {
  const notice = agentNotice(agent);
  if (!notice) return null;
  const tone = notice.tone === "critical" || notice.tone === "warning" ? notice.tone : "neutral";
  const style = TONE_CLASSES[tone];

  return (
    <div
      role={tone === "neutral" ? "status" : "alert"}
      className={cn("flex flex-wrap items-center gap-x-3 gap-y-2 rounded-tile px-4 py-3 text-control ring-1 ring-inset", style.box, className)}
    >
      <Icon icon={style.glyph} className={cn("mt-0.5 size-4 shrink-0 self-start", style.icon)} />
      <div className="flex min-w-0 flex-1 basis-64 flex-col gap-0.5">
        <span className="font-medium text-foreground tabular-nums">{notice.title}</span>
        <span className="text-small text-text-2 text-pretty">{notice.description}</span>
      </div>
      <Button variant="outline" size="sm" asChild className="bg-card">
        <Link to="/settings" search={{ section: "agents" }}>
          Agent status
        </Link>
      </Button>
    </div>
  );
}
