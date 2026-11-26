import { memo } from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { HealthStatus, type TopologyNode } from "@/gen/asterism/v1/asterism_pb";
import { formatBytes, roleLabel } from "@/lib/format";
import { useLiveValue } from "@/live/LiveStore";
import { cn } from "@/lib/utils";
import { roleColorVar } from "./roles";

export interface ConstellationNodeData extends Record<string, unknown> {
  node: TopologyNode;
  selectedId?: string;
  isExternalContext?: boolean;
  onInspect: (workloadId: string) => void;
}

export type ConstellationFlowNode = Node<ConstellationNodeData, "constellationStar">;

/** Star core size grows with memory (log scale), 18–36px. */
function starCoreSize(memBytes: number): number {
  const steps = Math.log2(Math.max(1, memBytes / (32 * 1024 * 1024)));
  return Math.min(36, Math.max(18, 16 + Math.round(steps * 4)));
}

// Both handles sit on the star's centre so straight edges meet the core.
const centeredHandle = "!top-[var(--star-center)] !left-1/2 !size-1 !min-w-0 !min-h-0 !border-0 !opacity-0";

export const ConstellationNode = memo(function ConstellationNode({
  data,
}: NodeProps<ConstellationFlowNode>) {
  const { node, selectedId, isExternalContext, onInspect } = data;
  const isSelected = selectedId === node.id;
  const liveMem = useLiveValue(node.id, "mem.used_bytes", Number(node.memUsedBytes));
  const color = roleColorVar(node.role);

  const haloColor =
    node.status === HealthStatus.DOWN
      ? "var(--status-critical)"
      : node.status === HealthStatus.DEGRADED
        ? "var(--status-warning)"
        : color;

  const core = starCoreSize(liveMem);
  const halo = core + 18;

  return (
    <button
      type="button"
      onClick={() => onInspect(node.id)}
      aria-label={`${node.label}, ${roleLabel(node.role)}, ${formatBytes(liveMem)}`}
      className={cn(
        "nodrag group flex w-48 flex-col items-center gap-2 rounded-card p-2 outline-none transition-colors",
        "hover:bg-card focus-visible:ring-1 focus-visible:ring-ring",
        isSelected && "bg-card ring-2 ring-ring",
        isExternalContext && "opacity-80",
      )}
      style={{ ["--star-center" as string]: `${8 + halo / 2}px` }}
    >
      <Handle type="target" position={Position.Left} className={centeredHandle} />
      <Handle type="source" position={Position.Right} className={centeredHandle} />
      <span
        aria-hidden="true"
        className="flex items-center justify-center rounded-full"
        style={{
          width: halo,
          height: halo,
          backgroundColor: `color-mix(in oklch, ${haloColor} 18%, transparent)`,
          boxShadow: `0 0 24px color-mix(in oklch, ${haloColor} 40%, transparent)`,
        }}
      >
        <span
          className="rounded-full ring-2 ring-[var(--iso-highlight-strong)]"
          style={{ width: core, height: core, backgroundColor: color }}
        />
      </span>
      <span className="flex max-w-full flex-col items-center rounded-tile bg-background/90 px-2 py-0.5 text-center group-hover:bg-transparent">
        <span className="max-w-full truncate text-body font-semibold text-foreground">{node.label}</span>
        <span className="max-w-full truncate whitespace-nowrap text-small text-muted-foreground tabular-nums">
          {roleLabel(node.role)} · {formatBytes(liveMem)}
        </span>
      </span>
    </button>
  );
});
