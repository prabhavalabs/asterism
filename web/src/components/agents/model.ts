/**
 * Display rules for asterism-agent status (AgentAdminService.ListAgents).
 * Type-only imports from the agent descriptors keep them out of the shell
 * chunk; the client itself loads on demand (lib/agentRpc).
 */
import type { StatusTone } from "@/components/common/tones";
import type { AgentStatus, ListAgentsResponse } from "@/gen/asterism/v1/agent_pb";
import { ContainerAction } from "@/gen/asterism/v1/asterism_pb";
import { formatDateTime, formatDurationMs, formatRelativeTime } from "@/lib/format";

export type AgentState = "connected" | "stale" | "offline" | "never";

/** Query key shared by the shell, the Overview banner and Settings. */
export const AGENTS_QUERY_KEY = ["agents"] as const;
/** Status is low-rate data (heartbeat every 10 s), so a 10 s refetch is enough. */
export const AGENTS_REFETCH_MS = 10_000;

/** The hub rewrites timestamps above this skew and the UI warns. */
export const CLOCK_SKEW_WARN_MS = 2_000;
/** Ingest lag above this is shown amber. */
export const LAG_WARN_MS = 5_000;

const STATE_STYLES: Record<AgentState, { label: string; tone: StatusTone }> = {
  connected: { label: "Connected", tone: "healthy" },
  stale: { label: "Stale", tone: "warning" },
  offline: { label: "Offline", tone: "critical" },
  never: { label: "Never connected", tone: "neutral" },
};

export function agentState(agent: Pick<AgentStatus, "state">): AgentState {
  const s = agent.state as AgentState;
  return s in STATE_STYLES ? s : "never";
}

export function agentStateStyle(agent: Pick<AgentStatus, "state">): { label: string; tone: StatusTone } {
  return STATE_STYLES[agentState(agent)];
}

/** The agent whose data the UI shows: the hub's primary host, else the first. */
export function primaryAgent(resp: ListAgentsResponse | undefined): AgentStatus | undefined {
  if (!resp || resp.agents.length === 0) return undefined;
  return resp.agents.find((a) => a.hostId === resp.primaryHostId) ?? resp.agents[0];
}

export function connectedCount(agents: readonly Pick<AgentStatus, "state">[]): number {
  return agents.filter((a) => agentState(a) === "connected").length;
}

/** Ingest lag for chips: "5 ms", "0.4 s", "12 s". */
export function formatLag(ms: number | bigint): string {
  const n = Math.abs(typeof ms === "bigint" ? Number(ms) : ms);
  return formatDurationMs(n);
}

/** Signed clock skew: "+3.2 s" (agent ahead) or "-450 ms". */
export function formatSkew(ms: number | bigint): string {
  const n = typeof ms === "bigint" ? Number(ms) : ms;
  if (n === 0) return "0 ms";
  return `${n > 0 ? "+" : "−"}${formatDurationMs(Math.abs(n))}`;
}

export function skewWarning(agent: Pick<AgentStatus, "clockSkewMs">): boolean {
  return Math.abs(Number(agent.clockSkewMs)) > CLOCK_SKEW_WARN_MS;
}

export function lagWarning(agent: Pick<AgentStatus, "lagMs">): boolean {
  return Number(agent.lagMs) > LAG_WARN_MS;
}

/** "12s ago", or "never" for an agent that has not connected yet. */
export function lastSeenText(agent: Pick<AgentStatus, "lastSeenMs">): string {
  return agent.lastSeenMs > 0n ? formatRelativeTime(agent.lastSeenMs) : "never";
}

const ACTION_LABELS: Partial<Record<ContainerAction, string>> = {
  [ContainerAction.START]: "start",
  [ContainerAction.STOP]: "stop",
  [ContainerAction.RESTART]: "restart",
  [ContainerAction.PAUSE]: "pause",
  [ContainerAction.UNPAUSE]: "unpause",
};

export function actionLabel(action: ContainerAction): string {
  return ACTION_LABELS[action] ?? "unknown";
}

/** Capability chips without the `action:*` entries, which have their own column. */
export function displayCapabilities(caps: readonly string[]): string[] {
  return caps.filter((c) => !c.startsWith("action:"));
}

export interface AgentNotice {
  tone: StatusTone;
  title: string;
  description: string;
}

/**
 * Overview banner for the primary agent, or undefined while it is connected.
 * Stale (reconnecting) is amber; offline is red; never connected is neutral.
 */
export function agentNotice(agent: AgentStatus | undefined): AgentNotice | undefined {
  if (!agent) return undefined;
  const name = agent.name || agent.hostId;
  switch (agentState(agent)) {
    case "connected":
      return undefined;
    case "stale":
      return {
        tone: "warning",
        title: `Agent ${name} is reconnecting`,
        description: `Last heard from ${lastSeenText(agent)}. Figures may lag until it is back.`,
      };
    case "offline":
      return {
        tone: "critical",
        title: agent.lastSeenMs > 0n ? `Agent ${name} offline since ${formatDateTime(agent.lastSeenMs)}` : `Agent ${name} offline`,
        description: `Last report ${lastSeenText(agent)}; data is stale. This page shows the last state the agent reported, and container actions are unavailable until it reconnects.`,
      };
    case "never":
      return {
        tone: "neutral",
        title: `Agent ${name} has not connected yet`,
        description: "Install asterism-agent on the host and point it at this hub. Until then there is nothing to show.",
      };
  }
}

/** Short status for the host card and topbar chip: "via agent prod-1 · 5 ms". */
export function agentChipText(agent: AgentStatus): string {
  const name = agent.name || agent.hostId;
  const state = agentState(agent);
  if (state === "connected") return `via agent ${name} · ${formatLag(agent.lagMs)}`;
  if (state === "never") return `agent ${name} · not connected`;
  return `agent ${name} · ${state} ${lastSeenText(agent)}`;
}

/** One-line tooltip body shared by the chip and the host card. */
export function agentTooltipText(agent: AgentStatus): string {
  const { label } = agentStateStyle(agent);
  const parts = [`Agent ${agent.name || agent.hostId}: ${label.toLowerCase()}`];
  if (agentState(agent) === "connected") {
    parts.push(`${agent.transport || "bidi"} stream`, `lag ${formatLag(agent.lagMs)}`);
    if (skewWarning(agent)) parts.push(`clock skew ${formatSkew(agent.clockSkewMs)}`);
  } else if (agent.lastSeenMs > 0n) {
    parts.push(`last seen ${lastSeenText(agent)}`);
  }
  return parts.join(" · ");
}

export type LiveIndicatorKind = "live" | "stale" | "no-data" | "reconnecting";

export interface LiveIndicator {
  kind: LiveIndicatorKind;
  /** Pill text: "Live", "Stale", "No data", "Reconnecting". */
  label: string;
  tone: StatusTone;
  pulse: boolean;
  /** Full sentence for tooltips and screen readers. */
  description: string;
  /** Short reason for cards when the data is not live ("agent prod-1 offline, last seen 5m ago"). */
  detail?: string;
}

/**
 * Freshness of the live data, shared by the Topbar pill and the Overview
 * "Host telemetry" card so both always agree. `agent` is the primary agent
 * and is passed only in agent mode: a connected hub stream with a stale
 * agent carries nothing new ("Stale"), and with an offline or never
 * connected agent it carries nothing at all ("No data").
 */
export function liveIndicator(streaming: boolean, agent?: AgentStatus, hostname?: string): LiveIndicator {
  if (!streaming) {
    return {
      kind: "reconnecting",
      label: "Reconnecting",
      tone: "warning",
      pulse: false,
      description: "Live stream disconnected. Reconnecting automatically.",
    };
  }
  if (agent) {
    const name = agent.name || agent.hostId;
    const state = agentState(agent);
    if (state === "stale") {
      return {
        kind: "stale",
        label: "Stale",
        tone: "warning",
        pulse: false,
        description: `Connected to the hub, but agent ${name} is reconnecting; last heard from ${lastSeenText(agent)}.`,
        detail: `agent ${name} reconnecting, last heard ${lastSeenText(agent)}`,
      };
    }
    if (state === "offline" || state === "never") {
      const detail =
        state === "never" ? `agent ${name} not connected yet` : `agent ${name} offline, last seen ${lastSeenText(agent)}`;
      return {
        kind: "no-data",
        label: "No data",
        tone: "neutral",
        pulse: false,
        description: `Connected to the hub, but ${detail}; it is not sending data.`,
        detail,
      };
    }
  }
  return {
    kind: "live",
    label: "Live",
    tone: "healthy",
    pulse: true,
    description: `Streaming live metrics${hostname ? ` from ${hostname}` : ""}.`,
  };
}
