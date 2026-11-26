import { memo } from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { ArrowRight01Icon, Share08Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { HealthStatus, WorkloadRole, type ProjectIcon as ProjectIconMessage, type TopologyNode } from "@/gen/asterism/v1/asterism_pb";
import { DomainChip, MeterBar, PortChip, ProjectIcon, StatusPill } from "@/components/common";
import { Button } from "@/components/ui/button";
import { formatBytes, formatPct, proxyKindLabel, roleLabel } from "@/lib/format";
import { useLiveValue } from "@/live/LiveStore";
import { cn } from "@/lib/utils";
import { IsometricBlockSvg } from "./IsometricBlockSvg";
import { roleMeterTone } from "./roles";

export interface IsometricBlockData extends Record<string, unknown> {
  node: TopologyNode;
  selectedId?: string;
  isExternalContext?: boolean;
  /** Name of the project an external (shared) node belongs to. */
  externalProjectName?: string;
  externalProjectIcon?: ProjectIconMessage;
  onInspect: (workloadId: string) => void;
}

export type IsometricFlowNode = Node<IsometricBlockData, "isometricBlock">;

const MAX_PORTS = 3;

const handleClass = "!size-2 !min-w-0 !min-h-0 !rounded-full !border-2 !border-card !bg-border-strong";

/** Workload block: 280px wide, 56px isometric glyph, 15px title, 13px body. */
export const IsometricBlockNode = memo(function IsometricBlockNode({
  data,
}: NodeProps<IsometricFlowNode>) {
  const { node, selectedId, isExternalContext, externalProjectName, externalProjectIcon, onInspect } = data;
  const isSelected = selectedId === node.id;

  const liveCpu = useLiveValue(node.id, "cpu.util", node.cpuUtil);
  const liveMem = useLiveValue(node.id, "mem.used_bytes", Number(node.memUsedBytes));
  const memLimit = Number(node.memLimitBytes);
  const memRatio = memLimit > 0 ? Math.min(1, liveMem / memLimit) : 0;

  const isDegraded = node.status === HealthStatus.DEGRADED;
  const isDown = node.status === HealthStatus.DOWN;
  const isProxy = node.role === WorkloadRole.PROXY;
  const ports = node.ports.slice(0, MAX_PORTS);
  const extraPorts = node.ports.length - ports.length;

  const subtitle =
    isProxy && node.proxyKind
      ? `${proxyKindLabel(node.proxyKind)}${node.strategy ? ` · ${node.strategy}` : ""}`
      : node.subtitle || node.id;

  return (
    <div
      data-selected={isSelected || undefined}
      className={cn(
        "group relative flex w-[280px] flex-col gap-3 rounded-card bg-card p-4 text-left shadow-card transition-shadow",
        "hover:shadow-raised",
        isExternalContext &&
          "bg-surface-subtle shadow-none outline-1 outline-offset-0 outline-dashed outline-border-strong",
        isDegraded && "ring-1 ring-status-warning",
        isDown && "ring-1 ring-status-critical",
        isSelected && "ring-2 ring-ring",
      )}
    >
      <Handle type="target" position={Position.Left} className={handleClass} />
      <Handle type="source" position={Position.Right} className={handleClass} />

      <div className="flex items-start gap-3">
        <IsometricBlockSvg role={node.role} replicas={node.replicas} size={56} className="shrink-0" />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex min-w-0 items-center gap-2">
            <button
              type="button"
              onClick={() => onInspect(node.id)}
              className="nodrag min-w-0 truncate text-left font-heading text-heading text-foreground outline-none hover:underline hover:underline-offset-4 focus-visible:ring-1 focus-visible:ring-ring"
              title={node.label}
            >
              {node.label}
            </button>
          </div>
          <div className="flex min-w-0 items-center gap-2">
            <StatusPill health={node.status} />
            <span className="truncate text-control text-muted-foreground">
              {roleLabel(node.role)}
              {node.replicas > 1 ? ` · ${node.replicas}×` : ""}
            </span>
          </div>
        </div>
      </div>

      <p className="-mt-1 truncate font-mono text-control text-muted-foreground" title={subtitle}>
        {subtitle}
      </p>

      {isProxy && node.hostnames.length > 0 && (
        <div className="flex min-w-0 items-center gap-1.5">
          <DomainChip domain={node.hostnames[0]} tls className="min-w-0" />
          {node.hostnames.length > 1 && (
            <span className="shrink-0 font-mono text-pill text-muted-foreground tabular-nums">
              +{node.hostnames.length - 1}
            </span>
          )}
        </div>
      )}

      <div className="flex flex-col gap-1.5">
        <div className="flex items-baseline justify-between gap-2 font-mono text-control tabular-nums">
          <span className="text-muted-foreground">
            RAM <span className="font-medium text-foreground">{formatBytes(liveMem)}</span>
          </span>
          <span className="text-muted-foreground">
            CPU <span className="font-medium text-foreground">{formatPct(liveCpu)}</span>
          </span>
        </div>
        <div className="flex items-center gap-2">
          <MeterBar
            className="flex-1"
            value={memLimit > 0 ? memRatio : 0}
            tone={memRatio >= 0.9 ? "critical" : memRatio >= 0.75 ? "warning" : roleMeterTone(node.role)}
            label={`${node.label} memory`}
            valueText={memLimit > 0 ? `${formatBytes(liveMem)} of ${formatBytes(memLimit)}` : formatBytes(liveMem)}
          />
          <span className="shrink-0 font-mono text-small text-muted-foreground tabular-nums">
            {memLimit > 0 ? `of ${formatBytes(memLimit)}` : "no limit"}
          </span>
        </div>
      </div>

      <div className="flex min-h-control-sm items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-1">
          {ports.length === 0 ? (
            <span className="text-small text-muted-foreground">No open ports</span>
          ) : (
            ports.map((pb) => (
              <PortChip
                key={pb.id}
                port={pb.containerPort || pb.hostPort}
                hostPort={pb.hostPort || undefined}
                exposure={pb.exposure}
                protocol={pb.proto}
              />
            ))
          )}
          {extraPorts > 0 && (
            <span className="font-mono text-pill text-muted-foreground tabular-nums">+{extraPorts}</span>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="nodrag -mr-1.5 shrink-0 text-text-2"
          onClick={() => onInspect(node.id)}
          aria-label={`Inspect ${node.label}`}
        >
          Inspect
          <Icon icon={ArrowRight01Icon} data-icon="inline-end" />
        </Button>
      </div>

      {isExternalContext && (
        <div className="-mt-1 flex items-center gap-1.5 text-small text-muted-foreground">
          <Icon icon={Share08Icon} className="size-3.5 shrink-0" />
          <span className="shrink-0">Shared · from</span>
          {externalProjectName ? (
            <span className="flex min-w-0 items-center gap-1.5">
              <ProjectIcon icon={externalProjectIcon} name={externalProjectName} size={16} />
              <span className="truncate text-text-2">{externalProjectName}</span>
            </span>
          ) : (
            <span className="truncate">another project</span>
          )}
        </div>
      )}
    </div>
  );
});
