import { memo } from "react";
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { ArrowRight02Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { HealthStatus, type Project } from "@/gen/asterism/v1/asterism_pb";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DomainChip, MeterBar, ProjectIcon, StatusPill } from "@/components/common";
import { formatBytes, formatPct } from "@/lib/format";
import { useLiveValue } from "@/live/LiveStore";
import { cn } from "@/lib/utils";
import { IsometricBlockSvg } from "./IsometricBlockSvg";
import { ROLE_FAMILY_LABEL, projectPrimaryRole, projectRoleCounts } from "./layout";
import { ROLE_DOT_CLASSES, roleMeterTone } from "./roles";

export interface ProjectOverviewNodeData extends Record<string, unknown> {
  project: Project;
  domains: string[];
  onExpandProject: (projectId: string) => void;
}

export type ProjectOverviewFlowNode = Node<ProjectOverviewNodeData, "projectOverview">;

const handleClass = "!size-2 !min-w-0 !min-h-0 !rounded-full !border-2 !border-card !bg-border-strong";

function capitalise(value: string): string {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : value;
}

/** Project tile on the overview map (296px). Icon and role mix come from the workloads. */
export const ProjectOverviewNode = memo(function ProjectOverviewNode({
  data,
}: NodeProps<ProjectOverviewFlowNode>) {
  const { project, domains, onExpandProject } = data;
  const liveMem = useLiveValue(project.id, "mem.used_bytes", Number(project.memUsedBytes));
  const liveCpu = useLiveValue(project.id, "cpu.util", project.cpuUtil);

  const primaryRole = projectPrimaryRole(project);
  const roleCounts = projectRoleCounts(project);
  const sharePct = project.memSharePct;
  const isDegraded = project.status === HealthStatus.DEGRADED;
  const isDown = project.status === HealthStatus.DOWN;

  return (
    <div
      className={cn(
        "group relative flex w-[296px] flex-col gap-2.5 rounded-card bg-card p-4 text-left shadow-card transition-shadow hover:shadow-raised",
        isDegraded && "ring-1 ring-status-warning",
        isDown && "ring-1 ring-status-critical",
      )}
    >
      <Handle type="target" position={Position.Left} className={handleClass} />
      <Handle type="source" position={Position.Right} className={handleClass} />

      <div className="flex items-start gap-3">
        <IsometricBlockSvg
          role={primaryRole}
          replicas={project.workloads.length > 1 ? 2 : 1}
          size={48}
          className="shrink-0"
        />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="flex min-w-0 items-center gap-2">
            <ProjectIcon icon={project.icon} name={project.name} size={20} />
            <button
              type="button"
              onClick={() => onExpandProject(project.id)}
              className="nodrag min-w-0 truncate text-left font-heading text-heading text-foreground outline-none hover:underline hover:underline-offset-4 focus-visible:ring-1 focus-visible:ring-ring"
              title={project.name}
            >
              {project.name}
            </button>
          </span>
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <StatusPill health={project.status} />
            {project.category && <Badge variant="outline">{capitalise(project.category)}</Badge>}
          </div>
        </div>
      </div>

      {roleCounts.length > 0 && (
        <ul
          className="flex min-w-0 items-center gap-x-2.5 overflow-hidden whitespace-nowrap text-control text-text-2"
          aria-label="Workloads by role"
        >
          {roleCounts.map(({ family, count }) => (
            <li key={family} className="inline-flex shrink-0 items-center gap-1 tabular-nums">
              <span aria-hidden="true" className={cn("size-2 rounded-xs", ROLE_DOT_CLASSES[family])} />
              {count} {count === 1 ? ROLE_FAMILY_LABEL[family].one : ROLE_FAMILY_LABEL[family].many}
            </li>
          ))}
        </ul>
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
            value={sharePct / 100}
            tone={roleMeterTone(primaryRole)}
            label={`${project.name} share of host memory`}
            valueText={`${sharePct.toFixed(1)}% of host memory`}
          />
          <span className="shrink-0 font-mono text-small text-muted-foreground tabular-nums">
            {sharePct.toFixed(1)}% VPS
          </span>
        </div>
      </div>

      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1.5">
          {domains.length > 0 ? (
            <>
              <DomainChip domain={domains[0]} tls className="min-w-0" />
              {domains.length > 1 && (
                <span className="shrink-0 font-mono text-pill text-muted-foreground tabular-nums">
                  +{domains.length - 1}
                </span>
              )}
            </>
          ) : (
            <span className="truncate text-small text-muted-foreground">No public domain</span>
          )}
        </div>
        <Button
          variant="outline"
          size="sm"
          className="nodrag shrink-0"
          onClick={() => onExpandProject(project.id)}
          aria-label={`Open ${project.name} diagram`}
        >
          Diagram
          <Icon icon={ArrowRight02Icon} data-icon="inline-end" />
        </Button>
      </div>
    </div>
  );
});
