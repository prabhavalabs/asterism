import { memo } from "react";
import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  getStraightPath,
  type Edge,
  type EdgeProps,
} from "@xyflow/react";
import { GitForkIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { EdgeKind, EdgeSource } from "@/gen/asterism/v1/asterism_pb";
import { cn } from "@/lib/utils";
import { LABEL_TARGET_CLEARANCE, labelCountSuffix } from "./layout";
import { edgeColorVar } from "./roles";

export interface TrafficEdgeData extends Record<string, unknown> {
  /** Primary label text (strategy for load-balanced edges). */
  label?: string;
  /** Edges merged into this label; shown as `×N` when above 1. */
  labelCount?: number;
  /** Second label line, e.g. `2 replicas`. */
  detail?: string;
  loadBalanced?: boolean;
  showLabel?: boolean;
  edgeKind: EdgeKind;
  source: EdgeSource;
  connCount: number;
  bytesPerSec: number;
  /** Travelling dash animation (off above ~150 nodes and under reduced motion). */
  animated?: boolean;
  /** `orthogonal` (isometric map) or `straight` (constellation). */
  pathStyle?: "orthogonal" | "straight";
  /** Shift of the vertical segment so parallel edges do not overlap. */
  laneOffset?: number;
  /** Vertical segment x relative to the source handle (wins over laneOffset). */
  laneX?: number;
  /** Entry point on the target relative to its handle centre. */
  targetOffsetY?: number;
  /** `target`: label ends just before the target, on the final segment. */
  labelAnchor?: "center" | "target";
  /** Vertical label shift from collision avoidance. */
  labelOffsetY?: number;
  /** Column gap in px; edges spanning several columns turn inside the first gap. */
  columnGap?: number;
}

export type TrafficFlowEdge = Edge<TrafficEdgeData, "trafficEdge">;

export const TrafficEdge = memo(function TrafficEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
}: EdgeProps<TrafficFlowEdge>) {
  const kind = data?.edgeKind ?? EdgeKind.APP;
  const color = edgeColorVar(kind);
  const isStatic = data?.source === EdgeSource.STATIC;
  const heavy = (data?.bytesPerSec ?? 0) > 800_000;
  const strokeWidth = heavy ? 2.25 : 1.6;
  const speed = heavy ? "1.1s" : "1.8s";

  let edgePath: string;
  let labelX: number;
  let labelY: number;
  let labelAtTarget = false;

  if (data?.pathStyle === "straight") {
    [edgePath, labelX, labelY] = getStraightPath({ sourceX, sourceY, targetX, targetY });
  } else {
    const gap = data?.columnGap ?? 0;
    const forward = targetX > sourceX;
    const endY = forward ? targetY + (data?.targetOffsetY ?? 0) : targetY;
    let centerX: number;
    if (forward && data?.laneX !== undefined) {
      centerX = sourceX + data.laneX;
    } else {
      const spansColumns = forward && gap > 0 && targetX - sourceX > gap * 1.6;
      const baseCenter = spansColumns ? sourceX + gap / 2 : (sourceX + targetX) / 2;
      centerX = baseCenter + (data?.laneOffset ?? 0);
    }
    [edgePath, labelX, labelY] = getSmoothStepPath({
      sourceX,
      sourceY,
      sourcePosition,
      targetX,
      targetY: endY,
      targetPosition,
      borderRadius: 10,
      offset: 20,
      centerX: forward ? centerX : undefined,
    });
    if (forward && data?.labelAnchor === "target") {
      labelAtTarget = true;
      labelX = targetX - LABEL_TARGET_CLEARANCE;
      labelY = endY;
    } else if (forward) {
      labelX = centerX;
    }
  }

  const label = data?.label;
  const showLabel = Boolean(data?.showLabel && label);
  const countSuffix = labelCountSuffix(data?.labelCount ?? 1);
  const labelTransform = labelAtTarget
    ? `translate(-100%, -50%) translate(${labelX}px, ${labelY}px)`
    : `translate(-50%, -50%) translate(${labelX}px, ${labelY + (data?.labelOffsetY ?? 0)}px)`;

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: color,
          strokeWidth,
          opacity: isStatic ? 0.55 : 0.85,
          strokeDasharray: isStatic ? "5 5" : undefined,
        }}
      />
      {!isStatic && data?.animated && (
        <path
          d={edgePath}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth + 0.5}
          className="asterism-edge-animated pointer-events-none"
          style={{ ["--edge-speed" as string]: speed }}
        />
      )}
      {showLabel && (
        <EdgeLabelRenderer>
          <div
            className={cn(
              "nodrag nopan pointer-events-none absolute flex flex-col items-center whitespace-nowrap rounded-chip bg-card px-2 py-0.5 text-center shadow-raised",
              data?.loadBalanced && "py-1",
            )}
            style={{ transform: labelTransform }}
          >
            <span className="flex items-center gap-1 font-mono text-pill leading-[18px] text-foreground tabular-nums">
              {data?.loadBalanced && (
                <Icon icon={GitForkIcon} className="size-3 shrink-0 rotate-90" style={{ color }} />
              )}
              <span>{label}</span>
              {countSuffix && <span className="text-muted-foreground">{countSuffix}</span>}
            </span>
            {data?.detail && (
              <span className="text-pill leading-[18px] text-muted-foreground tabular-nums">{data.detail}</span>
            )}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
});
