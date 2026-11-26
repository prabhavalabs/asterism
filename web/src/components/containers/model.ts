import { Code, ConnectError } from "@connectrpc/connect";
import {
  PauseIcon,
  PlayIcon,
  RotateClockwiseIcon,
  StopIcon,
} from "@hugeicons/core-free-icons";
import type { IconData } from "@/components/ui/icon";
import type { StatusTone } from "@/components/common";
import {
  ContainerAction,
  type ContainerPort,
  ContainerHealth,
  ContainerState,
  type Capabilities,
  type Container,
} from "@/gen/asterism/v1/asterism_pb";
import { formatRelativeTime, formatUptime } from "@/lib/format";

/** Query keys shared by the containers pages and the action hook. */
export const containerKeys = {
  all: ["containers"] as const,
  list: () => ["containers", "list"] as const,
  /** Unfiltered first page: host-wide counts, memory, engine and capabilities. */
  summary: () => ["containers", "list", "summary"] as const,
  detail: (name: string) => ["containers", "detail", name] as const,
  images: () => ["containers", "images"] as const,
  volumes: () => ["containers", "volumes"] as const,
  networks: () => ["containers", "networks"] as const,
  disk: () => ["containers", "disk"] as const,
};

/** `ctr:mailer` → `mailer` (URL segment and GetContainer reference). */
export function containerName(idOrName: string): string {
  return idOrName.startsWith("ctr:") ? idOrName.slice(4) : idOrName;
}

/** Link props for a container detail page. */
export function containerPath(idOrName: string): { to: "/containers/$containerId"; params: { containerId: string } } {
  return { to: "/containers/$containerId", params: { containerId: containerName(idOrName) } };
}

export function isContainerEntity(entityId: string): boolean {
  return entityId.startsWith("ctr:");
}

export interface StateStyle {
  label: string;
  tone: StatusTone;
}

const STATE_STYLES: Record<ContainerState, StateStyle> = {
  [ContainerState.UNSPECIFIED]: { label: "Unknown", tone: "neutral" },
  [ContainerState.CREATED]: { label: "Created", tone: "neutral" },
  [ContainerState.RUNNING]: { label: "Running", tone: "healthy" },
  [ContainerState.PAUSED]: { label: "Paused", tone: "info" },
  [ContainerState.RESTARTING]: { label: "Restarting", tone: "warning" },
  [ContainerState.REMOVING]: { label: "Removing", tone: "warning" },
  [ContainerState.EXITED]: { label: "Exited", tone: "neutral" },
  [ContainerState.DEAD]: { label: "Dead", tone: "critical" },
};

/** State label and tone; a non-zero or OOM exit turns an exited container red. */
export function stateStyle(c: Pick<Container, "state" | "exitCode" | "oomKilled">): StateStyle {
  const base = STATE_STYLES[c.state] ?? STATE_STYLES[ContainerState.UNSPECIFIED];
  if (c.state === ContainerState.EXITED && (c.exitCode !== 0 || c.oomKilled)) {
    return { label: base.label, tone: "critical" };
  }
  return base;
}

export function stateLabel(state: ContainerState): string {
  return (STATE_STYLES[state] ?? STATE_STYLES[ContainerState.UNSPECIFIED]).label;
}

/** Lowercase state name as used in audit records ("running"). */
export function stateKey(state: ContainerState): string {
  return stateLabel(state).toLowerCase();
}

const HEALTH_STYLES: Partial<Record<ContainerHealth, StateStyle>> = {
  [ContainerHealth.STARTING]: { label: "Starting", tone: "info" },
  [ContainerHealth.HEALTHY]: { label: "Healthy", tone: "healthy" },
  [ContainerHealth.UNHEALTHY]: { label: "Unhealthy", tone: "critical" },
};

/** Healthcheck status, or undefined when the container has no healthcheck. */
export function healthStyle(health: ContainerHealth): StateStyle | undefined {
  return HEALTH_STYLES[health];
}

export function isUp(state: ContainerState): boolean {
  return state === ContainerState.RUNNING || state === ContainerState.PAUSED || state === ContainerState.RESTARTING;
}

export function isStopped(state: ContainerState): boolean {
  return state === ContainerState.EXITED || state === ContainerState.CREATED || state === ContainerState.DEAD;
}

/** Unhealthy, crash-looping, dead or killed for memory: worth a look. */
export function needsAttention(c: Container): boolean {
  return (
    c.health === ContainerHealth.UNHEALTHY ||
    c.state === ContainerState.RESTARTING ||
    c.state === ContainerState.DEAD ||
    (c.state === ContainerState.EXITED && c.oomKilled)
  );
}

export type StateFilter = "all" | "running" | "stopped" | "unhealthy";

/** ListContainers `states` for the state chips (attention uses `attention_only`). */
export const STATE_FILTER_STATES: Record<"all" | "running" | "stopped" | "attention", ContainerState[]> = {
  all: [],
  running: [ContainerState.RUNNING, ContainerState.PAUSED, ContainerState.RESTARTING],
  stopped: [ContainerState.EXITED, ContainerState.CREATED, ContainerState.DEAD],
  attention: [],
};

export function matchesStateFilter(c: Container, filter: StateFilter): boolean {
  switch (filter) {
    case "running":
      return isUp(c.state);
    case "stopped":
      return isStopped(c.state);
    case "unhealthy":
      return needsAttention(c);
    default:
      return true;
  }
}

/** "3h 12m" while up, "2h ago" once stopped. */
export function lifetimeText(c: Container): string {
  if (c.state === ContainerState.RESTARTING) {
    return c.finishedAtMs > 0n ? `down ${formatRelativeTime(c.finishedAtMs).replace(/ ago$/, "")}` : "—";
  }
  if (isUp(c.state)) {
    if (c.uptimeSeconds > 0n) return formatUptime(c.uptimeSeconds);
    if (c.startedAtMs > 0n) return formatUptime(Math.max(0, (Date.now() - Number(c.startedAtMs)) / 1000));
    return "—";
  }
  if (c.finishedAtMs > 0n) return formatRelativeTime(c.finishedAtMs);
  if (c.createdAtMs > 0n) return `created ${formatRelativeTime(c.createdAtMs)}`;
  return "—";
}

export interface ActionMeta {
  label: string;
  /** "Restarting…" */
  progress: string;
  /** "Restarted" */
  past: string;
  icon: IconData;
  /** Stop and restart interrupt traffic: ask first. */
  confirm: boolean;
}

export const ACTION_META: Record<Exclude<ContainerAction, ContainerAction.UNSPECIFIED>, ActionMeta> = {
  [ContainerAction.START]: { label: "Start", progress: "Starting", past: "Started", icon: PlayIcon, confirm: false },
  [ContainerAction.STOP]: { label: "Stop", progress: "Stopping", past: "Stopped", icon: StopIcon, confirm: true },
  [ContainerAction.RESTART]: { label: "Restart", progress: "Restarting", past: "Restarted", icon: RotateClockwiseIcon, confirm: true },
  [ContainerAction.PAUSE]: { label: "Pause", progress: "Pausing", past: "Paused", icon: PauseIcon, confirm: false },
  [ContainerAction.UNPAUSE]: { label: "Unpause", progress: "Unpausing", past: "Unpaused", icon: PlayIcon, confirm: false },
};

export type ContainerActionKind = keyof typeof ACTION_META;

/** Display order of action buttons and menu items. */
export const ACTION_ORDER: readonly ContainerActionKind[] = [
  ContainerAction.START,
  ContainerAction.UNPAUSE,
  ContainerAction.RESTART,
  ContainerAction.STOP,
  ContainerAction.PAUSE,
];

/** Actions that make sense for a container in this state. */
export function validActions(state: ContainerState): ContainerActionKind[] {
  switch (state) {
    case ContainerState.RUNNING:
      return [ContainerAction.RESTART, ContainerAction.STOP, ContainerAction.PAUSE];
    case ContainerState.PAUSED:
      return [ContainerAction.UNPAUSE, ContainerAction.RESTART, ContainerAction.STOP];
    case ContainerState.RESTARTING:
      return [ContainerAction.RESTART, ContainerAction.STOP];
    case ContainerState.EXITED:
    case ContainerState.CREATED:
    case ContainerState.DEAD:
      return [ContainerAction.START];
    default:
      return [];
  }
}

/**
 * Why an action button is disabled, or undefined when it can run. The daemon
 * enforces the same rules; this only explains them up front.
 */
export function actionBlockedReason(
  c: Pick<Container, "protected" | "protectedReason">,
  caps: Capabilities | undefined,
  action?: ContainerActionKind,
): string | undefined {
  if (!caps) return "Checking whether actions are enabled…";
  if (!caps.canPerformActions) return caps.actionsDisabledReason || "Container actions are disabled on this server.";
  if (c.protected) return `Protected: ${c.protectedReason || "this container cannot be changed from Asterism"}.`;
  if (action !== undefined && caps.containerActions.length > 0 && !caps.containerActions.includes(action)) {
    if (caps.sourceMode === "agent") {
      return `${ACTION_META[action].label} is not allowed by the agent on this host (actions.allow in agent.yaml).`;
    }
    return `${ACTION_META[action].label} is not available in ${caps.sourceMode || "this"} mode.`;
  }
  return undefined;
}

export interface ActionErrorText {
  title: string;
  description: string;
}

/** Maps ActionsService errors to a short title and the daemon's explanation. */
export function actionErrorText(error: unknown, action: ContainerActionKind, name: string): ActionErrorText {
  const verb = ACTION_META[action].label.toLowerCase();
  if (error instanceof ConnectError) {
    const detail = error.rawMessage || error.message;
    switch (error.code) {
      case Code.FailedPrecondition:
        return { title: `Can't ${verb} ${name} right now`, description: detail };
      case Code.PermissionDenied:
        return { title: `Not allowed to ${verb} ${name}`, description: detail };
      case Code.Aborted:
        return { title: `${name} is busy`, description: "Another action on this container is still running. Try again when it finishes." };
      case Code.NotFound:
        return { title: `${name} no longer exists`, description: detail };
      case Code.InvalidArgument:
        return { title: `Invalid ${verb} request`, description: detail };
      case Code.Unavailable:
        return { title: `Docker couldn't ${verb} ${name}`, description: detail };
      case Code.Unauthenticated:
        return { title: "Your session has expired", description: "Sign in again to manage containers." };
      default:
        return { title: `Couldn't ${verb} ${name}`, description: detail };
    }
  }
  return {
    title: `Couldn't ${verb} ${name}`,
    description: error instanceof Error ? error.message : "The request failed. Check your connection and try again.",
  };
}

/** Graceful stop timeouts offered in the confirmation dialog (seconds). */
export const STOP_TIMEOUTS = [5, 10, 30, 60, 120] as const;
export const DEFAULT_STOP_TIMEOUT = 10;

/** Severity → tone for insights (critical / warning / info). */
export function insightTone(severity: string): StatusTone {
  switch (severity) {
    case "critical":
      return "critical";
    case "warning":
      return "warning";
    default:
      return "info";
  }
}

/** Highest severity tone among a container's insights. */
export function worstInsightTone(c: Container): StatusTone | undefined {
  if (c.insights.some((i) => i.severity === "critical")) return "critical";
  if (c.insights.some((i) => i.severity === "warning")) return "warning";
  if (c.insights.length > 0) return "info";
  return undefined;
}

/** Image as "repo:tag" with registry host trimmed for display. */
export function shortImage(c: Pick<Container, "image" | "imageRepo" | "imageTag">): string {
  const repo = c.imageRepo || c.image;
  const trimmed = repo.replace(/^[^/]+\.[^/]+\//, "").replace(/^docker\.io\/(library\/)?/, "");
  return c.imageTag ? `${trimmed}:${c.imageTag}` : trimmed;
}

/** "0.0.0.0:8080→80/tcp" or "5432/tcp (not published)". */
export function portText(p: ContainerPort): string {
  return p.published
    ? `${p.hostIp || "0.0.0.0"}:${p.hostPort}→${p.containerPort}/${p.proto}`
    : `${p.containerPort}/${p.proto} (not published)`;
}
