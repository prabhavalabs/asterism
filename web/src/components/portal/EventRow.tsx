import type { ActionAudit, Event } from "@/gen/asterism/v1/asterism_pb";
import { ListRow, StatusDot, StatusPill, severityTone } from "@/components/common";
import { Badge, type BadgeVariant } from "@/components/ui/badge";
import { formatDurationMs, formatRelativeTime } from "@/lib/format";

export interface EventRowProps {
  event: Event;
  /** Opens the workload inspector; rows for the host itself are not clickable. */
  onSelectWorkload?: (workloadId: string) => void;
  /** `compact`: dot, one-line message (overview). `full`: severity pill, kind, two-line message. */
  variant?: "compact" | "full";
}

function isWorkloadEntity(entityId: string): boolean {
  return entityId !== "" && entityId !== "host";
}

/** One event in a list: entity, message, severity and relative time. */
export function EventRow({ event, onSelectWorkload, variant = "full" }: EventRowProps) {
  const clickable = Boolean(onSelectWorkload) && isWorkloadEntity(event.entityId);
  const time = (
    <time
      dateTime={new Date(Number(event.tsMs)).toISOString()}
      title={new Date(Number(event.tsMs)).toLocaleString()}
      className="w-16 text-right font-mono text-small text-muted-foreground tabular-nums"
    >
      {formatRelativeTime(event.tsMs)}
    </time>
  );

  const row =
    variant === "compact" ? (
      <ListRow
        asChild={clickable}
        leading={<StatusDot tone={severityTone(event.severity)} className="mt-0.5" />}
        title={event.entityName || event.entityId}
        description={event.message}
        trailing={time}
      >
        {clickable ? <button type="button" onClick={() => onSelectWorkload?.(event.entityId)} /> : undefined}
      </ListRow>
    ) : (
      <ListRow
        asChild={clickable}
        className="min-h-14 items-start py-3"
        trailing={
          <span className="flex items-center gap-3">
            <StatusPill severity={event.severity} />
            {time}
          </span>
        }
      >
        {clickable ? (
          <button type="button" onClick={() => onSelectWorkload?.(event.entityId)}>
            <EventBody event={event} />
          </button>
        ) : (
          <EventBody event={event} />
        )}
      </ListRow>
    );

  return row;
}

const RESULT_STYLE: Record<string, { label: string; variant: BadgeVariant }> = {
  success: { label: "Succeeded", variant: "healthy" },
  failure: { label: "Failed", variant: "critical" },
  denied: { label: "Denied", variant: "warning" },
  not_found: { label: "Not found", variant: "warning" },
  invalid: { label: "Invalid", variant: "warning" },
};

const PAST: Record<string, string> = {
  start: "started",
  stop: "stopped",
  restart: "restarted",
  pause: "paused",
  unpause: "unpaused",
};

/** Context worth showing from lifecycle events (exit code, restart count, health). */
const ATTR_LABELS: Record<string, string> = {
  exit_code: "exit",
  restart_count: "restarts",
  health: "health",
};

function AuditBody({ audit: a, entityName }: { audit: ActionAudit; entityName: string }) {
  const result = RESULT_STYLE[a.result] ?? { label: a.result || "Unknown", variant: "neutral" as const };
  const ok = a.result === "success";
  const target = a.targetName || entityName || a.targetId;
  return (
    <span className="flex min-w-0 flex-1 flex-col gap-1">
      <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
        <span className="truncate text-body font-medium text-foreground">{a.actor || "unknown actor"}</span>
        <span className="text-body text-text-2">{ok ? (PAST[a.action] ?? a.action) : `tried to ${a.action}`}</span>
        <span className="truncate font-mono text-body font-medium text-foreground">{target}</span>
      </span>
      <span className="flex min-w-0 flex-wrap items-center gap-1.5">
        <Badge variant={result.variant}>{result.label}</Badge>
        <Badge variant="outline" mono>
          audit.{a.action}
        </Badge>
        {a.stateBefore && (
          <Badge variant="secondary" mono>
            {a.stateBefore} → {a.stateAfter || "?"}
          </Badge>
        )}
        <span className="font-mono text-small text-muted-foreground tabular-nums">{formatDurationMs(a.durationMs)}</span>
        <span className="text-small text-muted-foreground">
          {a.actorKind === "token" ? "API token" : "Web session"}
          {a.containerId ? ` · ${a.containerId}` : ""}
        </span>
      </span>
      {a.error && <span className="line-clamp-2 text-small text-status-critical text-pretty">{a.error}</span>}
    </span>
  );
}

function EventBody({ event }: { event: Event }) {
  if (event.audit) return <AuditBody audit={event.audit} entityName={event.entityName} />;
  const attrs = Object.entries(event.attrs).filter(([k]) => k in ATTR_LABELS);
  return (
    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
      <span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
        <span className="truncate text-body font-medium text-foreground">
          {event.entityName || event.entityId}
        </span>
        {event.kind && <span className="font-mono text-small text-muted-foreground">{event.kind}</span>}
      </span>
      <span className="line-clamp-2 text-small text-muted-foreground text-pretty">{event.message}</span>
      {attrs.length > 0 && (
        <span className="mt-0.5 flex flex-wrap gap-1.5">
          {attrs.map(([k, v]) => (
            <Badge key={k} variant="outline" mono>
              {ATTR_LABELS[k]} {v}
            </Badge>
          ))}
        </span>
      )}
    </span>
  );
}
