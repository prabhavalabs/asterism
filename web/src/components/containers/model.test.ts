import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  CapabilitiesSchema,
  ContainerAction,
  ContainerHealth,
  ContainerSchema,
  ContainerState,
} from "@/gen/asterism/v1/asterism_pb";
import {
  actionBlockedReason,
  actionErrorText,
  containerName,
  matchesStateFilter,
  shortImage,
  stateStyle,
  validActions,
} from "./model";

const ALL_ACTIONS = [
  ContainerAction.START,
  ContainerAction.STOP,
  ContainerAction.RESTART,
  ContainerAction.PAUSE,
  ContainerAction.UNPAUSE,
];

describe("validActions", () => {
  it.each([
    [ContainerState.RUNNING, [ContainerAction.RESTART, ContainerAction.STOP, ContainerAction.PAUSE]],
    [ContainerState.PAUSED, [ContainerAction.UNPAUSE, ContainerAction.RESTART, ContainerAction.STOP]],
    [ContainerState.RESTARTING, [ContainerAction.RESTART, ContainerAction.STOP]],
    [ContainerState.EXITED, [ContainerAction.START]],
    [ContainerState.CREATED, [ContainerAction.START]],
    [ContainerState.DEAD, [ContainerAction.START]],
    [ContainerState.REMOVING, []],
  ])("state %s offers %j", (state, want) => {
    expect(validActions(state)).toEqual(want);
  });
});

describe("actionBlockedReason", () => {
  const ctr = create(ContainerSchema, { name: "mailer" });
  const enabled = create(CapabilitiesSchema, { canPerformActions: true, containerActions: ALL_ACTIONS, sourceMode: "ssh" });

  it("explains the server reason when actions are disabled", () => {
    const caps = create(CapabilitiesSchema, { actionsDisabledReason: "set [actions] allow_actions = true" });
    expect(actionBlockedReason(ctr, caps)).toBe("set [actions] allow_actions = true");
  });
  it("explains protection", () => {
    const prot = create(ContainerSchema, { protected: true, protectedReason: "labelled asterism.protected=true" });
    expect(actionBlockedReason(prot, enabled)).toBe("Protected: labelled asterism.protected=true.");
  });
  it("blocks actions the collector does not implement", () => {
    const caps = create(CapabilitiesSchema, { canPerformActions: true, containerActions: [ContainerAction.START], sourceMode: "ssh" });
    expect(actionBlockedReason(ctr, caps, ContainerAction.PAUSE)).toMatch(/Pause is not available/);
    expect(actionBlockedReason(ctr, caps, ContainerAction.START)).toBeUndefined();
  });
  it("names the agent allow-list in agent mode", () => {
    const caps = create(CapabilitiesSchema, { canPerformActions: true, containerActions: [ContainerAction.RESTART], sourceMode: "agent" });
    expect(actionBlockedReason(ctr, caps, ContainerAction.STOP)).toMatch(/not allowed by the agent/);
    expect(actionBlockedReason(ctr, caps, ContainerAction.RESTART)).toBeUndefined();
  });
  it("passes the hub's agent reason through when actions are unavailable", () => {
    const caps = create(CapabilitiesSchema, { sourceMode: "agent", actionsDisabledReason: "agent demo is offline" });
    expect(actionBlockedReason(ctr, caps, ContainerAction.RESTART)).toBe("agent demo is offline");
  });
  it("allows everything when enabled and unprotected", () => {
    expect(actionBlockedReason(ctr, enabled, ContainerAction.RESTART)).toBeUndefined();
  });
  it("waits for capabilities", () => {
    expect(actionBlockedReason(ctr, undefined)).toMatch(/Checking/);
  });
});

describe("actionErrorText", () => {
  it.each([
    [Code.FailedPrecondition, /Can't restart mailer/],
    [Code.PermissionDenied, /Not allowed to restart mailer/],
    [Code.Aborted, /mailer is busy/],
    [Code.NotFound, /no longer exists/],
    [Code.Unavailable, /Docker couldn't restart mailer/],
  ])("maps %s", (code, title) => {
    const text = actionErrorText(new ConnectError("daemon says no", code), ContainerAction.RESTART, "mailer");
    expect(text.title).toMatch(title);
    expect(text.description.length).toBeGreaterThan(0);
  });
  it("keeps the daemon message for precondition failures", () => {
    const text = actionErrorText(new ConnectError("container is already stopped", Code.FailedPrecondition), ContainerAction.STOP, "web");
    expect(text.description).toBe("container is already stopped");
  });
});

describe("stateStyle and filters", () => {
  it("marks non-zero exits critical", () => {
    expect(stateStyle(create(ContainerSchema, { state: ContainerState.EXITED, exitCode: 137 })).tone).toBe("critical");
    expect(stateStyle(create(ContainerSchema, { state: ContainerState.EXITED, exitCode: 0 })).tone).toBe("neutral");
    expect(stateStyle(create(ContainerSchema, { state: ContainerState.RUNNING })).label).toBe("Running");
  });
  it("filters by state", () => {
    const running = create(ContainerSchema, { state: ContainerState.RUNNING });
    const unhealthy = create(ContainerSchema, { state: ContainerState.RUNNING, health: ContainerHealth.UNHEALTHY });
    const exited = create(ContainerSchema, { state: ContainerState.EXITED });
    const looping = create(ContainerSchema, { state: ContainerState.RESTARTING });
    expect(matchesStateFilter(running, "running")).toBe(true);
    expect(matchesStateFilter(exited, "running")).toBe(false);
    expect(matchesStateFilter(exited, "stopped")).toBe(true);
    expect(matchesStateFilter(unhealthy, "unhealthy")).toBe(true);
    expect(matchesStateFilter(looping, "unhealthy")).toBe(true);
    expect(matchesStateFilter(running, "unhealthy")).toBe(false);
  });
});

describe("names and images", () => {
  it("strips the ctr: prefix", () => {
    expect(containerName("ctr:mailer")).toBe("mailer");
    expect(containerName("mailer")).toBe("mailer");
  });
  it("trims registry hosts", () => {
    expect(shortImage({ image: "", imageRepo: "ghcr.io/acme/api", imageTag: "v1" })).toBe("acme/api:v1");
    expect(shortImage({ image: "", imageRepo: "postgres", imageTag: "17" })).toBe("postgres:17");
  });
});
