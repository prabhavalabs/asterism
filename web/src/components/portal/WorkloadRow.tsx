import { Link } from "@tanstack/react-router";
import { ContainerIcon } from "@hugeicons/core-free-icons";
import { HealthStatus, WorkloadKind, type Workload } from "@/gen/asterism/v1/asterism_pb";
import { ListRow, PortChip, StatusPill } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { IsometricBlockSvg } from "@/components/topology/IsometricBlockSvg";
import { formatBytes, formatPct, roleLabel } from "@/lib/format";
import { cn } from "@/lib/utils";

export interface WorkloadRowProps {
  workload: Workload;
  onSelect: (workloadId: string) => void;
  /** Show the image under the name instead of the role (detail views). */
  showImage?: boolean;
  /** Port chips shown from `sm` up. Default 2. */
  maxPorts?: number;
  /** Show the health pill even when healthy. */
  alwaysShowHealth?: boolean;
  /** Add a link to the container detail page for container workloads (default true). */
  containerLink?: boolean;
  className?: string;
}

/**
 * One workload as a 52px list row: isometric role block, name, role or
 * image, port chips, health and live memory/CPU. Opens the inspector.
 */
export function WorkloadRow({
  workload: w,
  onSelect,
  showImage = false,
  maxPorts = 2,
  alwaysShowHealth = false,
  containerLink = true,
  className,
}: WorkloadRowProps) {
  const ports = w.ports.slice(0, maxPorts);
  const extraPorts = w.ports.length - ports.length;
  const showHealth = alwaysShowHealth || w.health !== HealthStatus.HEALTHY;

  const linked = containerLink && w.kind === WorkloadKind.CONTAINER && w.id.startsWith("ctr:");

  const row = (
    <ListRow
      asChild
      className={cn("min-h-13 gap-3.5", linked && "pr-1", className)}
      leading={<IsometricBlockSvg role={w.role} replicas={w.replicas} size={32} />}
      title={w.name}
      description={
        showImage ? (
          <span className="font-mono text-mono">{w.image || roleLabel(w.role)}</span>
        ) : (
          <>
            {roleLabel(w.role)}
            {w.replicas > 1 && <span className="tabular-nums"> · {w.replicas} replicas</span>}
          </>
        )
      }
      trailing={
        <>
          {ports.length > 0 && (
            <span className="hidden items-center gap-1.5 md:flex">
              {ports.map((p) => (
                <PortChip
                  key={p.id}
                  port={p.containerPort || p.hostPort}
                  hostPort={p.hostPort}
                  exposure={p.exposure}
                  protocol={p.proto}
                />
              ))}
              {extraPorts > 0 && (
                <span className="text-small text-muted-foreground tabular-nums">+{extraPorts}</span>
              )}
            </span>
          )}
          {showHealth && <StatusPill health={w.health} className="hidden sm:inline-flex" />}
          <span className="flex w-20 flex-col items-end font-mono tabular-nums">
            <span className="text-mono font-medium text-foreground">{formatBytes(w.memUsedBytes)}</span>
            <span className="text-small text-muted-foreground">cpu {formatPct(w.cpuUtil)}</span>
          </span>
        </>
      }
    >
      <button type="button" title={`Inspect ${w.name}`} onClick={() => onSelect(w.id)} />
    </ListRow>
  );

  if (!linked) return row;
  return (
    <div className="flex min-w-0 items-center">
      <div className="min-w-0 flex-1">{row}</div>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button variant="ghost" size="icon-sm" className="mr-3 shrink-0" asChild>
            <Link to="/containers/$containerId" params={{ containerId: w.id.slice(4) }} aria-label={`Open container ${w.name}`}>
              <Icon icon={ContainerIcon} />
            </Link>
          </Button>
        </TooltipTrigger>
        <TooltipContent side="left">Container details and actions</TooltipContent>
      </Tooltip>
    </div>
  );
}
