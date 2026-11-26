import { useMemo } from "react";
import { ArrowRight01Icon, ArrowRight02Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import type { Graph, TopologyNode } from "@/gen/asterism/v1/asterism_pb";
import { DomainChip, EmptyState, PortChip, ProjectIcon, StatusPill } from "@/components/common";
import { Button } from "@/components/ui/button";
import { formatBytes, roleLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { IsometricBlockSvg } from "./IsometricBlockSvg";
import { domainsByProject } from "./buildGraph";
import { rankNodes } from "./layout";

export interface TopologyListViewProps {
  graph: Graph;
  /** Show only this project (drill-down). */
  projectId?: string | null;
  selectedWorkloadId?: string;
  onSelectWorkload: (id: string) => void;
  /** Shows an "Open diagram" button per project group. */
  onOpenProject?: (projectId: string) => void;
  className?: string;
}

const MAX_PORTS = 3;

/**
 * Accessible list fallback for the map on small screens: workloads grouped
 * by project in traffic order (ingress, compute, data) with role glyph,
 * health, ports and memory.
 */
export function TopologyListView({
  graph,
  projectId,
  selectedWorkloadId,
  onSelectWorkload,
  onOpenProject,
  className,
}: TopologyListViewProps) {
  const domains = useMemo(() => domainsByProject(graph), [graph]);

  const groups = useMemo(() => {
    const projects = projectId ? graph.projects.filter((p) => p.id === projectId) : graph.projects;
    return projects.map((p) => {
      const nodes = graph.nodes.filter((n) => n.projectId === p.id);
      const rank = rankNodes(
        nodes.map((n) => ({ id: n.id, role: n.role })),
        graph.edges,
      );
      const sorted = [...nodes].sort(
        (a, b) => (rank.get(a.id) ?? 0) - (rank.get(b.id) ?? 0) || a.label.localeCompare(b.label),
      );
      return { project: p, nodes: sorted };
    });
  }, [graph, projectId]);

  if (groups.length === 0 || groups.every((g) => g.nodes.length === 0)) {
    return (
      <EmptyState
        className={className}
        title="No workloads"
        description="Nothing is running in this view yet."
        size="compact"
      />
    );
  }

  return (
    <div className={cn("flex flex-col gap-4 p-4", className)}>
      {groups.map(({ project, nodes }) => {
        const projectDomains = domains.get(project.id) ?? [];
        return (
          <section
            key={project.id}
            aria-labelledby={`topology-list-${project.id}`}
            className="overflow-hidden rounded-card bg-card shadow-card"
          >
            <header className="flex flex-col gap-2 border-b border-divider px-4 py-3">
              <div className="flex min-w-0 items-center gap-2">
                <ProjectIcon icon={project.icon} name={project.name} size={24} />
                <h3 id={`topology-list-${project.id}`} className="min-w-0 flex-1 truncate font-heading text-heading text-foreground">
                  {project.name}
                </h3>
                <StatusPill health={project.status} />
              </div>
              <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5 text-small text-muted-foreground tabular-nums">
                <span>
                  {nodes.length} {nodes.length === 1 ? "service" : "services"}
                </span>
                <span className="font-mono">{formatBytes(project.memUsedBytes)}</span>
                {projectDomains[0] && <DomainChip domain={projectDomains[0]} tls className="min-w-0" />}
                {projectDomains.length > 1 && (
                  <span className="font-mono text-pill">+{projectDomains.length - 1}</span>
                )}
              </div>
              {onOpenProject && (
                <Button
                  variant="outline"
                  size="sm"
                  className="self-start"
                  onClick={() => onOpenProject(project.id)}
                >
                  Open diagram
                  <Icon icon={ArrowRight02Icon} data-icon="inline-end" />
                </Button>
              )}
            </header>
            <ul className="divide-y divide-divider">
              {nodes.map((n) => (
                <li key={n.id}>
                  <WorkloadRow
                    node={n}
                    selected={selectedWorkloadId === n.id}
                    onSelect={() => onSelectWorkload(n.id)}
                  />
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

function WorkloadRow({
  node,
  selected,
  onSelect,
}: {
  node: TopologyNode;
  selected: boolean;
  onSelect: () => void;
}) {
  const ports = node.ports.slice(0, MAX_PORTS);
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected || undefined}
      className={cn(
        "flex min-h-14 w-full items-start gap-3 px-4 py-3 text-left outline-none transition-colors",
        "hover:bg-secondary focus-visible:bg-secondary focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-inset",
        selected && "bg-secondary",
      )}
    >
      <IsometricBlockSvg role={node.role} replicas={node.replicas} size={36} className="mt-0.5 shrink-0" />
      <span className="flex min-w-0 flex-1 flex-col gap-1.5">
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-body font-medium text-foreground">{node.label}</span>
          <span className="shrink-0 font-mono text-small text-muted-foreground tabular-nums">
            {formatBytes(node.memUsedBytes)}
          </span>
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-1.5">
          <StatusPill health={node.status}>{roleLabel(node.role)}</StatusPill>
          {ports.map((pb) => (
            <PortChip
              key={pb.id}
              port={pb.containerPort || pb.hostPort}
              hostPort={pb.hostPort || undefined}
              exposure={pb.exposure}
              protocol={pb.proto}
            />
          ))}
          {node.ports.length > MAX_PORTS && (
            <span className="font-mono text-pill text-muted-foreground">+{node.ports.length - MAX_PORTS}</span>
          )}
        </span>
      </span>
      <Icon icon={ArrowRight01Icon} className="mt-1 size-4 shrink-0 text-text-4" />
    </button>
  );
}
