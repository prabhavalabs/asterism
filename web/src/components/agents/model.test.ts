import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AgentStatusSchema, ListAgentsResponseSchema } from "@/gen/asterism/v1/agent_pb";
import { ContainerAction } from "@/gen/asterism/v1/asterism_pb";
import {
  actionLabel,
  agentChipText,
  agentNotice,
  agentState,
  agentStateStyle,
  agentTooltipText,
  connectedCount,
  displayCapabilities,
  formatLag,
  formatSkew,
  lagWarning,
  liveIndicator,
  primaryAgent,
  skewWarning,
} from "./model";

const NOW = 1_800_000_000_000;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
});

const agent = (fields: Parameters<typeof create<typeof AgentStatusSchema>>[1] = {}) =>
  create(AgentStatusSchema, { hostId: "prod-1", name: "prod-1", ...fields });

describe("agent state", () => {
  it.each([
    ["connected", "Connected", "healthy"],
    ["stale", "Stale", "warning"],
    ["offline", "Offline", "critical"],
    ["never", "Never connected", "neutral"],
    ["", "Never connected", "neutral"],
    ["exploded", "Never connected", "neutral"],
  ])("%s", (state, label, tone) => {
    expect(agentStateStyle({ state })).toEqual({ label, tone });
  });

  it("counts connected agents only", () => {
    expect(connectedCount([{ state: "connected" }, { state: "stale" }, { state: "connected" }, { state: "never" }])).toBe(2);
  });
});

describe("primaryAgent", () => {
  it("prefers the hub's primary host, then the first agent", () => {
    const a = agent({ hostId: "a", name: "a" });
    const b = agent({ hostId: "b", name: "b" });
    expect(primaryAgent(create(ListAgentsResponseSchema, { agents: [a, b], primaryHostId: "b" }))?.name).toBe("b");
    expect(primaryAgent(create(ListAgentsResponseSchema, { agents: [a, b], primaryHostId: "zzz" }))?.name).toBe("a");
    expect(primaryAgent(create(ListAgentsResponseSchema, {}))).toBeUndefined();
    expect(primaryAgent(undefined)).toBeUndefined();
  });
});

describe("formatting", () => {
  it("formats lag and signed skew", () => {
    expect(formatLag(5n)).toBe("5 ms");
    expect(formatLag(420)).toBe("420 ms");
    expect(formatLag(2_400n)).toBe("2.4 s");
    expect(formatSkew(3_200n)).toBe("+3.2 s");
    expect(formatSkew(-450n)).toBe("−450 ms");
    expect(formatSkew(0n)).toBe("0 ms");
  });

  it("warns above 2 s of skew and 5 s of lag", () => {
    expect(skewWarning({ clockSkewMs: 2_000n })).toBe(false);
    expect(skewWarning({ clockSkewMs: -2_001n })).toBe(true);
    expect(lagWarning({ lagMs: 5_000n })).toBe(false);
    expect(lagWarning({ lagMs: 5_001n })).toBe(true);
  });

  it("names actions and hides action capabilities", () => {
    expect(actionLabel(ContainerAction.RESTART)).toBe("restart");
    expect(actionLabel(ContainerAction.UNSPECIFIED)).toBe("unknown");
    expect(displayCapabilities(["docker", "action:restart", "cgroup_v2"])).toEqual(["docker", "cgroup_v2"]);
  });
});

describe("chip and tooltip text", () => {
  it("shows the lag while connected", () => {
    const a = agent({ state: "connected", lagMs: 5n, transport: "bidi" });
    expect(agentChipText(a)).toBe("via agent prod-1 · 5 ms");
    expect(agentTooltipText(a)).toBe("Agent prod-1: connected · bidi stream · lag 5 ms");
  });

  it("adds the skew to the tooltip when it is large", () => {
    const a = agent({ state: "connected", lagMs: 5n, transport: "unary", clockSkewMs: 3_000n });
    expect(agentTooltipText(a)).toContain("clock skew +3.0 s");
  });

  it("shows when an offline agent was last seen", () => {
    const a = agent({ state: "offline", lastSeenMs: BigInt(NOW - 125_000) });
    expect(agentChipText(a)).toBe("agent prod-1 · offline 2m ago");
    expect(agentTooltipText(a)).toBe("Agent prod-1: offline · last seen 2m ago");
  });

  it("says when an agent never connected", () => {
    expect(agentChipText(agent({ state: "never" }))).toBe("agent prod-1 · not connected");
  });
});

describe("agentNotice", () => {
  it("is quiet while connected or without an agent", () => {
    expect(agentNotice(undefined)).toBeUndefined();
    expect(agentNotice(agent({ state: "connected" }))).toBeUndefined();
  });

  it("is amber while reconnecting and red once offline", () => {
    expect(agentNotice(agent({ state: "stale", lastSeenMs: BigInt(NOW - 12_000) }))).toMatchObject({
      tone: "warning",
      title: "Agent prod-1 is reconnecting",
    });
    const offline = agentNotice(agent({ state: "offline", lastSeenMs: BigInt(NOW - 3_600_000) }));
    expect(offline).toMatchObject({ tone: "critical" });
    expect(offline?.title).toMatch(/^Agent prod-1 offline since /);
    expect(offline?.description).toMatch(/^Last report 1h ago; data is stale/);
  });

  it("explains an agent that never connected", () => {
    expect(agentNotice(agent({ state: "never" }))).toMatchObject({ tone: "neutral" });
    expect(agentState({ state: "never" })).toBe("never");
  });
});

describe("liveIndicator", () => {
  it("is live while streaming without an agent (local and SSH modes)", () => {
    expect(liveIndicator(true, undefined, "vps-1")).toMatchObject({ kind: "live", label: "Live", tone: "healthy", pulse: true });
    expect(liveIndicator(true, undefined, "vps-1").description).toContain("vps-1");
  });

  it("is live while streaming from a connected agent", () => {
    expect(liveIndicator(true, agent({ state: "connected" }))).toMatchObject({ kind: "live", label: "Live" });
  });

  it("says Stale while the agent reconnects", () => {
    const live = liveIndicator(true, agent({ state: "stale", lastSeenMs: BigInt(NOW - 25_000) }));
    expect(live).toMatchObject({ kind: "stale", label: "Stale", tone: "warning", pulse: false });
    expect(live.detail).toBe("agent prod-1 reconnecting, last heard 25s ago");
  });

  it("says No data while the agent is offline or never connected", () => {
    const offline = liveIndicator(true, agent({ state: "offline", lastSeenMs: BigInt(NOW - 300_000) }));
    expect(offline).toMatchObject({ kind: "no-data", label: "No data", tone: "neutral", pulse: false });
    expect(offline.detail).toBe("agent prod-1 offline, last seen 5m ago");
    expect(liveIndicator(true, agent({ state: "never" })).detail).toBe("agent prod-1 not connected yet");
  });

  it("says Reconnecting when the hub stream itself is down, whatever the agent", () => {
    expect(liveIndicator(false, agent({ state: "offline" }))).toMatchObject({ kind: "reconnecting", label: "Reconnecting", tone: "warning" });
    expect(liveIndicator(false)).toMatchObject({ kind: "reconnecting" });
  });
});
