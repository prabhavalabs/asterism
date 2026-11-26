import { Link } from "@tanstack/react-router";
import { RamMemoryIcon } from "@hugeicons/core-free-icons";
import type { Project } from "@/gen/asterism/v1/asterism_pb";
import { EmptyState, ListRow, MeterBar, SectionCard, type MeterSegment } from "@/components/common";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

type ProjectTone = "chart-1" | "chart-2" | "chart-3" | "chart-4" | "chart-5";

const TONES: readonly ProjectTone[] = ["chart-1", "chart-3", "chart-5", "chart-4", "chart-2"];

const DOT: Record<ProjectTone | "neutral", string> = {
  "chart-1": "bg-chart-1",
  "chart-2": "bg-chart-2",
  "chart-3": "bg-chart-3",
  "chart-4": "bg-chart-4",
  "chart-5": "bg-chart-5",
  neutral: "bg-status-neutral",
};

const MAX_ROWS = 5;

interface MemoryAllocationCardProps {
  projects: readonly Project[];
  className?: string;
}

/**
 * Container memory by project: one stacked bar sized by each project's
 * share of host memory, then the projects ranked by resident memory.
 */
export function MemoryAllocationCard({ projects, className }: MemoryAllocationCardProps) {
  const sorted = [...projects].sort((a, b) => Number(b.memUsedBytes) - Number(a.memUsedBytes));
  const totalBytes = sorted.reduce((acc, p) => acc + Number(p.memUsedBytes), 0);
  const totalShare = sorted.reduce((acc, p) => acc + p.memSharePct, 0);
  const top = sorted.slice(0, MAX_ROWS);
  const rest = sorted.slice(MAX_ROWS);
  const restBytes = rest.reduce((acc, p) => acc + Number(p.memUsedBytes), 0);
  const restShare = rest.reduce((acc, p) => acc + p.memSharePct, 0);

  const segments: MeterSegment[] = top.map((p, i) => ({
    value: p.memSharePct / 100,
    tone: TONES[i % TONES.length],
    label: `${p.name}: ${formatBytes(p.memUsedBytes)}`,
  }));
  if (rest.length > 0) {
    segments.push({ value: restShare / 100, tone: "neutral", label: `Other projects: ${formatBytes(restBytes)}` });
  }

  return (
    <SectionCard
      title="Memory allocation"
      icon={RamMemoryIcon}
      description="Resident container memory by project, as a share of host memory"
      className={className}
      flush
      actions={
        <span className="font-mono text-mono font-medium text-foreground tabular-nums">
          {formatBytes(totalBytes)}
        </span>
      }
    >
      {sorted.length === 0 ? (
        <EmptyState size="compact" icon={RamMemoryIcon} title="No projects yet" description="Compose stacks appear here once containers run." />
      ) : (
        <>
          <div className="flex flex-col gap-2 px-4 pt-4 pb-4">
            <MeterBar
              size="lg"
              segments={segments}
              label="Container memory by project"
              valueText={`${totalShare.toFixed(1)}% of host memory in containers`}
            />
            <p className="text-small text-muted-foreground tabular-nums">
              {totalShare.toFixed(1)}% of host memory held by {sorted.length}{" "}
              {sorted.length === 1 ? "project" : "projects"}
            </p>
          </div>
          <div className="divide-y divide-divider border-t border-divider">
            {top.map((p, i) => (
              <MemoryRow
                key={p.id}
                project={p}
                dotClass={DOT[TONES[i % TONES.length]]}
              />
            ))}
            {rest.length > 0 && (
              <ListRow
                leading={<span aria-hidden="true" className={cn("size-2.5 rounded-xs", DOT.neutral)} />}
                title={`${rest.length} more ${rest.length === 1 ? "project" : "projects"}`}
                trailing={<MemoryValue bytes={restBytes} share={restShare} />}
              />
            )}
          </div>
        </>
      )}
    </SectionCard>
  );
}

function MemoryRow({ project, dotClass }: { project: Project; dotClass: string }) {
  return (
    <ListRow
      asChild
      leading={<span aria-hidden="true" className={cn("size-2.5 rounded-xs", dotClass)} />}
      title={project.name}
      description={`${project.workloads.length} ${project.workloads.length === 1 ? "service" : "services"}`}
      trailing={<MemoryValue bytes={Number(project.memUsedBytes)} share={project.memSharePct} />}
    >
      <Link to="/projects/$projectId" params={{ projectId: project.name }} />
    </ListRow>
  );
}

function MemoryValue({ bytes, share }: { bytes: number; share: number }) {
  return (
    <span className="flex items-baseline gap-3 font-mono tabular-nums">
      <span className="text-mono font-medium text-foreground">{formatBytes(bytes)}</span>
      <span className="w-12 text-right text-small text-muted-foreground">{share.toFixed(1)}%</span>
    </span>
  );
}
