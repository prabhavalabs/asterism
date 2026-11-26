import { useCallback, useMemo, useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { Alert02Icon, ArrowDown01Icon, InformationCircleIcon, SquareLock02Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  CategoryBadge,
  PortChip,
  ProjectIcon,
  SortHeader,
  StatusDot,
  StatusPill,
  TONE_TEXT_CLASSES,
  TechIcon,
  projectNameFromId,
} from "@/components/common";
import { LiveSparkline } from "@/components/portal/MicroVisuals";
import { ContainerState, type Container, type ContainerPort, type Project } from "@/gen/asterism/v1/asterism_pb";
import { formatBytes, formatPct } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ContainerRowMenu } from "./ContainerActions";
import { LiveMetricText } from "./live";
import {
  containerName,
  groupContainersByProject,
  healthStyle,
  insightTone,
  isUp,
  lifetimeText,
  portText,
  shortImage,
  stateStyle,
  summarizeContainers,
  worstInsightTone,
  type ContainerGroup,
} from "./model";
import type { ContainerActionsApi } from "./useContainerActions";

export interface ContainersTableProps {
  /** One server page, already sorted (`project, name` when grouped). */
  containers: readonly Container[];
  projects: ReadonlyMap<string, Project>;
  actions: ContainerActionsApi;
  /** The list's `order_by` (column headers show and change it). */
  order: string;
  onOrderChange: (order: string) => void;
  /** Group the page's rows under project headers (the default `project, name` order). */
  groupByProject: boolean;
  /** Containers per project across all pages (ListContainers.project_counts). */
  projectTotals?: ReadonlyMap<string, number>;
}

const fmtCpu = (v: number) => formatPct(v);
const fmtMem = (v: number) => formatBytes(v);
const MAX_ROW_PORTS = 1;

function cpuOf(c: Container): number {
  return c.stats?.cpuUtil ?? 0;
}
function memOf(c: Container): number {
  return Number(c.stats?.memUsedBytes ?? 0n);
}
function memLimitOf(c: Container): number {
  return Number(c.stats?.memLimitBytes || c.resources?.memLimitBytes || 0n);
}

function projectLabel(projectId: string, projects: ReadonlyMap<string, Project>): string {
  return projects.get(projectId)?.name ?? (projectNameFromId(projectId) || "standalone");
}

/**
 * The containers data table for one server page (40px rows, sticky header,
 * server-sorted columns, optional project groups). Render it inside a
 * DataTableFrame / PaginatedScrollArea, which supplies the scroll area.
 */
export function ContainersTable({
  containers,
  projects,
  actions,
  order,
  onOrderChange,
  groupByProject,
  projectTotals,
}: ContainersTableProps) {
  const groups = useMemo(() => groupContainersByProject(containers, groupByProject), [containers, groupByProject]);
  const sort = { order, onOrderChange };
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const toggleGroup = useCallback((key: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  return (
    <table
      className={cn(
        "w-full table-fixed border-separate border-spacing-0 text-left text-control",
        groupByProject ? "min-w-[1120px]" : "min-w-[1260px]",
      )}
    >
      <thead className="text-small text-muted-foreground">
        <tr className="h-8">
          <SortHeader
            label="State"
            field="state"
            {...sort}
            className={groupByProject ? "w-[8.5rem] pl-10" : "w-[7.5rem] pl-4"}
          />
          <SortHeader label="Container" field="name" {...sort} />
          {!groupByProject && <SortHeader label="Project" field="project" {...sort} className="w-40" />}
          <Th className="w-[6.75rem]">Health</Th>
          <SortHeader label="CPU" field="cpu" {...sort} className="w-[8.5rem]" defaultDesc />
          <SortHeader label="Memory" field="memory" {...sort} className="w-[9.75rem]" defaultDesc />
          <Th className="w-[8.25rem]">Ports</Th>
          <SortHeader label="Uptime" field="uptime" {...sort} className="w-[6.75rem]" defaultDesc />
          <SortHeader label="Restarts" field="restarts" {...sort} className="w-[5.5rem]" align="right" defaultDesc />
          <Th className="w-[5rem] pr-3 text-right">
            <span className="sr-only">Insights and actions</span>
          </Th>
        </tr>
      </thead>
      {groupByProject ? (
        groups.map((g, i) => {
          const key = `${g.projectId || "standalone"}:${i}`;
          return (
            <ProjectGroup
              key={key}
              group={g}
              first={i === 0}
              projects={projects}
              actions={actions}
              total={projectTotals?.get(g.projectId)}
              expanded={!collapsed.has(g.projectId || "standalone")}
              onToggle={() => toggleGroup(g.projectId || "standalone")}
            />
          );
        })
      ) : (
        <tbody>
          {containers.map((c) => (
            <ContainerTableRow key={c.id} container={c} project={projects.get(c.projectId)} actions={actions} showProject />
          ))}
        </tbody>
      )}
    </table>
  );
}

/** Phone layout of the same page: one card per container, under project headers when grouped. */
export function ContainerCardList({
  containers,
  projects,
  actions,
  groupByProject = false,
  projectTotals,
}: Pick<ContainersTableProps, "containers" | "projects" | "actions" | "projectTotals"> & { groupByProject?: boolean }) {
  if (!groupByProject) {
    return (
      <ul className="flex flex-col divide-y divide-divider">
        {containers.map((c) => (
          <ContainerCard key={c.id} container={c} project={projects.get(c.projectId)} actions={actions} />
        ))}
      </ul>
    );
  }
  const groups = groupContainersByProject(containers);
  return (
    <ul className="flex flex-col">
      {groups.map((g, i) => {
        const project = projects.get(g.projectId);
        const name = projectLabel(g.projectId, projects);
        const summary = summarizeContainers(g.rows);
        const count = projectTotals?.get(g.projectId) ?? g.rows.length;
        return (
          <li key={`${g.projectId || "standalone"}:${i}`} className={cn(i > 0 && "border-t-4 border-background")}>
            <div className="flex min-w-0 items-center gap-2.5 border-b border-divider bg-surface-subtle px-4 py-2.5">
              <ProjectIcon icon={project?.icon} name={name} size={24} />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-heading text-body font-semibold text-foreground">{name}</span>
                <span className="text-small text-muted-foreground tabular-nums">
                  {count} {count === 1 ? "container" : "containers"} · {summary.up}/{summary.total} up
                </span>
              </span>
              {project?.category && <CategoryBadge category={project.category} className="shrink-0" />}
            </div>
            <ul className="flex flex-col divide-y divide-divider">
              {g.rows.map((c) => (
                <ContainerCard key={c.id} container={c} project={project} actions={actions} />
              ))}
            </ul>
          </li>
        );
      })}
    </ul>
  );
}

const thBase =
  "sticky top-0 z-10 h-8 border-b border-divider bg-surface-subtle px-3 font-medium whitespace-nowrap";

function Th({ className, children }: { className?: string; children?: ReactNode }) {
  return (
    <th scope="col" className={cn(thBase, className)}>
      {children}
    </th>
  );
}

const GROUPED_COLUMNS = 9;

/*
 * Tree guides for grouped rows, drawn with pseudo-elements in the first cell:
 * a vertical rail under the group's toggle (24px from the left) and a short
 * elbow into each row; the last row's elbow curves and ends the rail.
 */
const RAIL = "before:absolute before:left-6 before:-top-px before:-bottom-px before:w-px before:bg-border-strong";
const ELBOW = "after:absolute after:left-6 after:top-1/2 after:h-px after:w-3 after:bg-border-strong";
const LAST_ELBOW =
  "after:absolute after:left-6 after:-top-px after:h-[calc(50%+1px)] after:w-3 after:rounded-bl-md after:border-b after:border-l after:border-border-strong";
const HEADER_RAIL =
  "after:absolute after:left-6 after:top-[calc(50%+0.75rem)] after:-bottom-px after:w-px after:bg-border-strong";

function rowDomId(c: Container): string {
  return `container-row-${c.id.replace(/[^A-Za-z0-9_-]/g, "-")}`;
}

/**
 * One project as a parent row with its containers threaded beneath it. The
 * group is its own <tbody>; the header cell is a rowgroup header, and the
 * disclosure button collapses the children.
 */
function ProjectGroup({
  group,
  first,
  projects,
  actions,
  total,
  expanded,
  onToggle,
}: {
  group: ContainerGroup;
  first: boolean;
  projects: ReadonlyMap<string, Project>;
  actions: ContainerActionsApi;
  total?: number;
  expanded: boolean;
  onToggle: () => void;
}) {
  const { projectId, rows } = group;
  const project = projects.get(projectId);
  const name = projectLabel(projectId, projects);
  const summary = summarizeContainers(rows);
  const split = total !== undefined && total > rows.length;
  const count = total ?? rows.length;
  const upTone = summary.up === summary.total ? "healthy" : summary.up === 0 ? "critical" : "warning";
  const headerCell = "h-14 border-b border-divider bg-surface-subtle px-3 align-middle";

  return (
    <tbody data-project={projectId || "standalone"}>
      {!first && (
        <tr aria-hidden="true">
          <td colSpan={GROUPED_COLUMNS} className="h-4 border-b border-divider bg-background p-0" />
        </tr>
      )}
      <tr className="group/head">
        <th
          scope="rowgroup"
          colSpan={3}
          className={cn(headerCell, "relative text-left font-normal", expanded && HEADER_RAIL)}
        >
          <span className="flex min-w-0 items-center gap-2.5">
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={expanded}
              aria-controls={rows.map(rowDomId).join(" ")}
              aria-label={`${expanded ? "Collapse" : "Expand"} ${name}`}
              className="inline-flex size-6 shrink-0 items-center justify-center rounded-button-sm text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
            >
              <Icon
                icon={ArrowDown01Icon}
                className={cn(
                  "size-4 transition-transform duration-150 ease-out motion-reduce:transition-none",
                  !expanded && "-rotate-90",
                )}
              />
            </button>
            <ProjectIcon icon={project?.icon} name={name} size={32} />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="flex min-w-0 items-center gap-2">
                {project ? (
                  <Link
                    to="/projects/$projectId"
                    params={{ projectId: project.name }}
                    className="truncate font-heading text-body font-semibold text-foreground outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    {name}
                  </Link>
                ) : (
                  <span className="truncate font-heading text-body font-semibold text-foreground">{name}</span>
                )}
                {project?.category && <CategoryBadge category={project.category} className="shrink-0" />}
              </span>
              <span className="flex min-w-0 items-center gap-1.5 whitespace-nowrap text-small text-muted-foreground tabular-nums">
                <span>
                  {count} {count === 1 ? "container" : "containers"}
                </span>
                <span aria-hidden="true">·</span>
                <StatusDot tone={upTone} />
                <span className="text-text-2">{summary.up}/{summary.total} up</span>
              </span>
            </span>
          </span>
        </th>
        <td className={headerCell}>
          <GroupTotal value={summary.up > 0 ? formatPct(summary.cpuUtil) : "—"} label="CPU" />
        </td>
        <td className={headerCell}>
          <GroupTotal value={summary.up > 0 ? formatBytes(summary.memUsedBytes) : "—"} label="memory" />
        </td>
        <td colSpan={4} className={cn(headerCell, "pr-3 text-right text-small text-muted-foreground tabular-nums")}>
          {split && `${rows.length} of ${total} on this page`}
          {summary.attention > 0 && (
            <span className={cn("ml-3 inline-flex items-center gap-1", TONE_TEXT_CLASSES.warning)}>
              <Icon icon={Alert02Icon} className="size-3.5" />
              {summary.attention} need{summary.attention === 1 ? "s" : ""} attention
            </span>
          )}
        </td>
      </tr>
      {expanded &&
        rows.map((c, i) => (
          <ContainerTableRow
            key={c.id}
            id={rowDomId(c)}
            container={c}
            project={project}
            actions={actions}
            showProject={false}
            tree={i === rows.length - 1 ? "last" : "middle"}
          />
        ))}
    </tbody>
  );
}

function GroupTotal({ value, label }: { value: string; label: string }) {
  return (
    <span className="flex items-baseline gap-1.5 whitespace-nowrap" title={`Total ${label} of this group's containers on this page`}>
      <span className="font-mono text-mono font-semibold text-foreground tabular-nums">{value}</span>
      <span className="text-small text-muted-foreground">total</span>
    </span>
  );
}

const td = "h-11 border-b border-divider px-3 align-middle";

function ContainerTableRow({
  id,
  container: c,
  project,
  actions,
  showProject,
  tree,
}: {
  id?: string;
  container: Container;
  project?: Project;
  actions: ContainerActionsApi;
  showProject: boolean;
  /** Position under a project group: draws the thread guide in the first cell. */
  tree?: "middle" | "last";
}) {
  const state = stateStyle(c);
  const health = healthStyle(c.health);
  const running = c.state === ContainerState.RUNNING;
  const name = containerName(c.id);
  const projectName = project?.name ?? (projectNameFromId(c.projectId) || "standalone");
  const memLimit = memLimitOf(c);
  const pending = actions.pending[c.id];

  return (
    <tr
      id={id}
      data-state={state.tone}
      aria-busy={pending ? true : undefined}
      className={cn("group/row transition-colors hover:bg-muted/50", pending && "bg-muted/40")}
    >
      <td
        className={cn(
          td,
          tree ? cn("relative pl-10", tree === "last" ? LAST_ELBOW : cn(RAIL, ELBOW)) : "pl-4",
        )}
      >
        <span className="flex items-center gap-2 whitespace-nowrap">
          <StatusDot tone={state.tone} pulse={c.state === ContainerState.RESTARTING} />
          <span className={cn(state.tone === "critical" ? TONE_TEXT_CLASSES.critical : "text-foreground")}>
            {state.label}
          </span>
        </span>
      </td>
      <td className={td}>
        <Link
          to="/containers/$containerId"
          params={{ containerId: name }}
          className="group/name flex min-w-0 items-center gap-2.5 outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <TechIcon slug={c.tech} role={c.role} image={c.image} size={24} />
          <span className="flex min-w-0 flex-col leading-tight">
            <span className="truncate font-medium text-foreground group-hover/name:underline">{c.name}</span>
            <span className="truncate font-mono text-small text-muted-foreground" title={c.image}>
              {shortImage(c)}
            </span>
          </span>
          {c.protected && (
            <Icon
              icon={SquareLock02Icon}
              className="ml-auto size-4 shrink-0 text-text-4"
              aria-hidden={false}
              aria-label={`Protected: ${c.protectedReason}`}
            />
          )}
        </Link>
      </td>
      {showProject && (
        <td className={td}>
          <span className="flex min-w-0 items-center gap-2">
            <ProjectIcon icon={project?.icon} name={projectName} size={16} />
            {project ? (
              <Link
                to="/projects/$projectId"
                params={{ projectId: project.name }}
                className="truncate text-text-2 outline-none hover:text-foreground hover:underline focus-visible:ring-1 focus-visible:ring-ring"
              >
                {projectName}
              </Link>
            ) : (
              <span className="truncate text-muted-foreground">{projectName}</span>
            )}
          </span>
        </td>
      )}
      <td className={td}>{health ? <StatusPill tone={health.tone}>{health.label}</StatusPill> : <span className="text-text-4">—</span>}</td>
      <td className={td}>
        {running ? (
          <span className="flex items-center gap-2">
            <LiveMetricText
              entityId={c.id}
              metric="cpu.util"
              fallback={cpuOf(c)}
              format={fmtCpu}
              className="w-[3.25rem] shrink-0 font-mono text-mono text-foreground"
            />
            <LiveSparkline entityId={c.id} metric="cpu.util" tone="chart-1" windowSec={120} className="h-5 w-11" />
          </span>
        ) : (
          <span className="text-text-4">—</span>
        )}
      </td>
      <td className={td}>
        {running || c.state === ContainerState.PAUSED ? (
          <span className="flex items-center gap-2" title={memLimit > 0 ? `Limit ${formatBytes(memLimit)}` : "No memory limit"}>
            <LiveMetricText
              entityId={c.id}
              metric="mem.used_bytes"
              fallback={memOf(c)}
              format={fmtMem}
              live={running}
              className="w-[4.5rem] shrink-0 font-mono text-mono text-foreground"
            />
            {running && <LiveSparkline entityId={c.id} metric="mem.used_bytes" tone="chart-5" windowSec={120} className="h-5 w-11" />}
          </span>
        ) : (
          <span className="text-text-4">—</span>
        )}
      </td>
      <td className={td}>
        <RowPorts ports={c.ports} />
      </td>
      <td className={cn(td, "whitespace-nowrap")}>
        {isUp(c.state) ? (
          <span className="font-mono text-mono text-text-2 tabular-nums">{lifetimeText(c)}</span>
        ) : (
          <span className="flex flex-col leading-tight">
            <span className="text-text-2 tabular-nums">{lifetimeText(c)}</span>
            {c.state === ContainerState.EXITED && (
              <span className={cn("font-mono text-small tabular-nums", c.exitCode !== 0 ? TONE_TEXT_CLASSES.critical : "text-muted-foreground")}>
                exit {c.exitCode}
              </span>
            )}
          </span>
        )}
      </td>
      <td className={cn(td, "text-right")}>
        <RestartCell container={c} />
      </td>
      <td className={cn(td, "pr-3")}>
        <span className="flex items-center justify-end gap-1">
          <InsightIndicator container={c} />
          <ContainerRowMenu container={c} actions={actions} />
        </span>
      </td>
    </tr>
  );
}

function RowPorts({
  ports,
  max = MAX_ROW_PORTS,
  hideEmpty = false,
}: {
  ports: readonly ContainerPort[];
  max?: number;
  hideEmpty?: boolean;
}) {
  const published = ports.filter((p) => p.published);
  const list = published.length > 0 ? published : ports;
  if (list.length === 0) return hideEmpty ? null : <span className="text-text-4">—</span>;
  const shown = list.slice(0, max);
  const extra = list.length - shown.length;
  return (
    <span className="flex items-center gap-1.5" title={list.map(portText).join(", ")}>
      {shown.map((p) => (
        <PortChip
          key={`${p.hostIp}:${p.hostPort}:${p.containerPort}/${p.proto}`}
          port={p.containerPort}
          hostPort={p.hostPort}
          exposure={p.exposure}
          protocol={p.proto}
        />
      ))}
      {extra > 0 && <span className="text-small text-muted-foreground tabular-nums">+{extra}</span>}
    </span>
  );
}

function RestartCell({ container: c }: { container: Container }) {
  return (
    <span className="inline-flex items-center justify-end gap-1.5">
      {c.oomKilled && (
        <Badge variant="critical" title="Last exit was an out-of-memory kill">
          OOM
        </Badge>
      )}
      <span
        className={cn(
          "font-mono text-mono tabular-nums",
          c.restartCount >= 5 ? TONE_TEXT_CLASSES.critical : c.restartCount > 0 ? TONE_TEXT_CLASSES.warning : "text-text-4",
        )}
      >
        {c.restartCount}
      </span>
    </span>
  );
}

function InsightIndicator({ container: c }: { container: Container }) {
  const tone = worstInsightTone(c);
  if (!tone) return <span className="size-control-sm" aria-hidden="true" />;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Link
          to="/containers/$containerId"
          params={{ containerId: containerName(c.id) }}
          aria-label={`${c.insights.length} ${c.insights.length === 1 ? "insight" : "insights"} for ${c.name}`}
          className={cn(
            "inline-flex size-control-sm items-center justify-center gap-0.5 rounded-button-sm outline-none hover:bg-muted focus-visible:ring-1 focus-visible:ring-ring",
            TONE_TEXT_CLASSES[tone],
          )}
        >
          <Icon icon={tone === "info" ? InformationCircleIcon : Alert02Icon} className="size-4" />
        </Link>
      </TooltipTrigger>
      <TooltipContent side="left" className="max-w-80 flex-col items-start gap-1">
        {c.insights.slice(0, 4).map((i) => (
          <span key={i.code} className="flex gap-1.5">
            <StatusDot tone={insightTone(i.severity)} className="mt-1.5" />
            {i.message}
          </span>
        ))}
        {c.insights.length > 4 && <span className="text-background/70">+{c.insights.length - 4} more</span>}
      </TooltipContent>
    </Tooltip>
  );
}

function ContainerCard({ container: c, project, actions }: { container: Container; project?: Project; actions: ContainerActionsApi }) {
  const state = stateStyle(c);
  const health = healthStyle(c.health);
  const running = c.state === ContainerState.RUNNING;
  const projectName = project?.name ?? (projectNameFromId(c.projectId) || "standalone");
  return (
    <li className="flex flex-col gap-2.5 px-4 py-3">
      <div className="flex min-w-0 items-center gap-3">
        <TechIcon slug={c.tech} role={c.role} image={c.image} size={32} />
        <Link
          to="/containers/$containerId"
          params={{ containerId: containerName(c.id) }}
          className="flex min-w-0 flex-1 flex-col outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <span className="truncate text-body font-medium text-foreground">{c.name}</span>
          <span className="truncate font-mono text-small text-muted-foreground">{shortImage(c)}</span>
        </Link>
        <InsightIndicator container={c} />
        <ContainerRowMenu container={c} actions={actions} />
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusPill tone={state.tone} pulse={c.state === ContainerState.RESTARTING}>
          {state.label}
          {c.state === ContainerState.EXITED ? ` · ${c.exitCode}` : ""}
        </StatusPill>
        {health && <StatusPill tone={health.tone}>{health.label}</StatusPill>}
        {c.oomKilled && <Badge variant="critical">OOM</Badge>}
        {c.protected && <Badge variant="outline">Protected</Badge>}
        <RowPorts ports={c.ports} hideEmpty />
      </div>
      <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-small text-muted-foreground tabular-nums">
        <span className="flex min-w-0 items-center gap-1.5">
          <ProjectIcon icon={project?.icon} name={projectName} size={16} />
          <span className="truncate text-text-2">{projectName}</span>
        </span>
        <span className="text-right">
          {c.state === ContainerState.RESTARTING ? "" : isUp(c.state) ? "Up " : "Stopped "}
          {lifetimeText(c)}
        </span>
        <span>
          CPU{" "}
          {running ? (
            <LiveMetricText entityId={c.id} metric="cpu.util" fallback={cpuOf(c)} format={fmtCpu} className="font-mono text-text-2" />
          ) : (
            "—"
          )}
        </span>
        <span className="text-right">
          Mem{" "}
          {running ? (
            <LiveMetricText entityId={c.id} metric="mem.used_bytes" fallback={memOf(c)} format={fmtMem} className="font-mono text-text-2" />
          ) : (
            "—"
          )}
          {c.restartCount > 0 && <span className={TONE_TEXT_CLASSES.warning}> · {c.restartCount} restarts</span>}
        </span>
      </div>
    </li>
  );
}
