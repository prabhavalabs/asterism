import { GitForkIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { cn } from "@/lib/utils";
import { EdgeKind } from "@/gen/asterism/v1/asterism_pb";
import { ROLE_DOT_CLASSES, edgeColorVar, type RoleFamily } from "./roles";

const ROLES: { family: RoleFamily; label: string }[] = [
  { family: "proxy", label: "Ingress / LB" },
  { family: "app", label: "App / API" },
  { family: "worker", label: "Worker" },
  { family: "db", label: "Database" },
  { family: "cache", label: "Cache / queue" },
];

const EDGES: { kind: EdgeKind; label: string }[] = [
  { kind: EdgeKind.INGRESS, label: "Ingress" },
  { kind: EdgeKind.APP, label: "Service" },
  { kind: EdgeKind.DATABASE, label: "DB" },
  { kind: EdgeKind.CACHE, label: "Cache" },
];

function LineSample({ color, dashed = false }: { color: string; dashed?: boolean }) {
  return (
    <svg aria-hidden="true" width="24" height="8" viewBox="0 0 24 8" className="shrink-0">
      <line
        x1="1"
        y1="4"
        x2="23"
        y2="4"
        stroke={color}
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray={dashed ? "4 4" : undefined}
      />
    </svg>
  );
}

export interface MapLegendProps {
  className?: string;
  /** Hide the load-balancing entry (the project overview has no edge labels). */
  compact?: boolean;
}

/** Role colours and edge styles used on the map. */
export function MapLegend({ className, compact = false }: MapLegendProps) {
  return (
    <div
      data-slot="map-legend"
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-divider bg-surface-subtle px-4 py-2 text-small text-muted-foreground",
        className,
      )}
    >
      <ul className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5" aria-label="Workload roles">
        {ROLES.map((r) => (
          <li key={r.family} className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className={cn("size-2.5 rounded-xs", ROLE_DOT_CLASSES[r.family])} />
            {r.label}
          </li>
        ))}
      </ul>
      <span aria-hidden="true" className="hidden h-4 w-px bg-divider sm:block" />
      <ul className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5" aria-label="Connection types">
        {EDGES.map((e) => (
          <li key={e.label} className="inline-flex items-center gap-1.5">
            <LineSample color={edgeColorVar(e.kind)} />
            {e.label}
          </li>
        ))}
        <li className="inline-flex items-center gap-1.5">
          <LineSample color="var(--muted-foreground)" dashed />
          Inferred
        </li>
        {!compact && (
          <li className="inline-flex items-center gap-1.5">
            <Icon icon={GitForkIcon} className="size-3.5 rotate-90 text-role-proxy" />
            Load-balanced
          </li>
        )}
      </ul>
    </div>
  );
}
