import { describe, expect, it } from "vitest";
import {
  validateAlertsSearch,
  validateContainerDetailSearch,
  validateContainersSearch,
  validateEventsSearch,
  validatePortsSearch,
  validateProjectDetailSearch,
  validateProjectsSearch,
  validateSettingsSearch,
  validateTopologySearch,
} from "./routeSearch";

describe("route search validators", () => {
  it("keeps known container tabs and drops the default and unknown ones", () => {
    expect(validateContainersSearch({ tab: "images" })).toEqual({ tab: "images" });
    expect(validateContainersSearch({ tab: "disk" })).toEqual({ tab: "disk" });
    expect(validateContainersSearch({ tab: "containers" })).toEqual({});
    expect(validateContainersSearch({ tab: "secrets" })).toEqual({});
    expect(validateContainersSearch({ tab: 3 })).toEqual({});
  });

  it("keeps list paging keys only on their own containers tab", () => {
    expect(validateContainersSearch({ tab: "volumes", volPage: 2, volSize: "50", volFilter: "unused", volQ: "db" })).toEqual({
      tab: "volumes",
      volPage: 2,
      volSize: 50,
      volFilter: "unused",
      volQ: "db",
    });
    expect(validateContainersSearch({ tab: "images", imgPage: "3", volPage: 2, imgFilter: "dangling" })).toEqual({
      tab: "images",
      imgPage: 3,
      imgFilter: "dangling",
    });
    expect(validateContainersSearch({ tab: "volumes", volFilter: "secret", volSize: 3 })).toEqual({ tab: "volumes" });
    expect(validateContainersSearch({ volPage: 2 })).toEqual({});
    expect(validateContainersSearch({ tab: "networks", netOrder: "containers desc", netQ: "shop" })).toEqual({
      tab: "networks",
      netOrder: "containers desc",
      netQ: "shop",
    });
  });

  it("keeps the containers list keys on the default tab", () => {
    expect(
      validateContainersSearch({ page: "2", size: 50, q: "redis", order: "cpu desc", state: "attention", project: "proj:shop" }),
    ).toEqual({ page: 2, size: 50, q: "redis", order: "cpu desc", state: "attention", project: "proj:shop" });
    expect(validateContainersSearch({ order: "project, name", state: "all" })).toEqual({});
    expect(validateContainersSearch({ order: "secret desc", state: "zombie" })).toEqual({});
  });

  it("keeps event paging only on the events tab of a container", () => {
    expect(validateContainerDetailSearch({ tab: "events", evPage: 3 })).toEqual({ tab: "events", evPage: 3 });
    expect(validateContainerDetailSearch({ tab: "logs", evPage: 3 })).toEqual({ tab: "logs" });
    expect(validateContainerDetailSearch({ tab: "overview" })).toEqual({});
  });

  it("validates project cards and services paging", () => {
    expect(validateProjectsSearch({ category: "staging", size: 24, order: "health" })).toEqual({
      category: "staging",
      size: 24,
      order: "health",
    });
    expect(validateProjectsSearch({ order: "memory desc", size: 12, category: "all" })).toEqual({});
    expect(validateProjectDetailSearch({ q: "api", order: "memory desc" })).toEqual({ q: "api", order: "memory desc" });
  });

  it("accepts only the ingress tab on /ports, each with its own list keys", () => {
    expect(validatePortsSearch({ tab: "ingress" })).toEqual({ tab: "ingress" });
    expect(validatePortsSearch({ tab: "ports" })).toEqual({});
    expect(validatePortsSearch({})).toEqual({});
    expect(validatePortsSearch({ exposure: "public", q: "5432", page: 2 })).toEqual({ exposure: "public", q: "5432", page: 2 });
    expect(validatePortsSearch({ tab: "ingress", routeView: "cards", routeSize: 48, exposure: "public" })).toEqual({
      tab: "ingress",
      routeView: "cards",
      routeSize: 48,
    });
  });

  it("validates event kind and severity independently", () => {
    expect(validateEventsSearch({ kind: "audit" })).toEqual({ kind: "audit" });
    expect(validateEventsSearch({ kind: "audit", severity: "critical" })).toEqual({ kind: "audit", severity: "critical" });
    expect(validateEventsSearch({ kind: "all", severity: "all" })).toEqual({});
    expect(validateEventsSearch({ kind: "nope", severity: "warning" })).toEqual({ severity: "warning" });
    expect(validateEventsSearch({ kind: "audit", page: "4", order: "ts", q: "restart" })).toEqual({
      kind: "audit",
      page: 4,
      order: "ts",
      q: "restart",
    });
  });

  it("validates settings sections", () => {
    expect(validateSettingsSearch({ section: "mcp" })).toEqual({ section: "mcp" });
    expect(validateSettingsSearch({ section: "billing" })).toEqual({});
    expect(validateSettingsSearch({ section: "agents", agentPage: "2", agentSize: "10" })).toEqual({
      section: "agents",
      agentPage: 2,
    });
  });

  it("keeps the networks filter only when it is not the user-defined default", () => {
    expect(validateContainersSearch({ tab: "networks", netFilter: "all", netQ: "shop" })).toEqual({
      tab: "networks",
      netFilter: "all",
      netQ: "shop",
    });
    expect(validateContainersSearch({ tab: "networks", netFilter: "user-defined" })).toEqual({ tab: "networks" });
    expect(validateContainersSearch({ tab: "networks", netFilter: "builtin" })).toEqual({ tab: "networks" });
  });

  it("keeps the constellation view and a project name", () => {
    expect(validateTopologySearch({ view: "constellation", project: "blog" })).toEqual({
      view: "constellation",
      project: "blog",
    });
    expect(validateTopologySearch({ view: "isometric", project: "" })).toEqual({});
    expect(validateTopologySearch({ view: "3d" })).toEqual({});
  });
  it("validates alerts tabs, filters and deep links", () => {
    expect(validateAlertsSearch({ tab: "rules", state: "resolved", severity: "critical" })).toEqual({ tab: "rules", state: "resolved", severity: "critical" });
    expect(validateAlertsSearch({ tab: "alerts", state: "active", severity: "loud" })).toEqual({});
    expect(validateAlertsSearch({ alert: "disk_usage|host|/" })).toEqual({ alert: "disk_usage|host|/" });
  });
});
