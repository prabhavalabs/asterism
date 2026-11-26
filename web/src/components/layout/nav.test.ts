import { describe, expect, it } from "vitest";
import { NAV_ITEMS, NAV_SECTIONS, isNavItemActive, navItemsFor, type NavSearch } from "./nav";

function activeIds(pathname: string, search: NavSearch = {}): string[] {
  return NAV_ITEMS.filter((item) => isNavItemActive(item, pathname, search)).map((item) => item.id);
}

describe("isNavItemActive", () => {
  it.each<[string, string, NavSearch, string[]]>([
    ["dashboard only on the root", "/", {}, ["dashboard"]],
    ["topology map", "/topology", { view: "constellation" }, ["topology"]],
    ["containers default tab", "/containers", {}, ["containers"]],
    ["containers explicit default tab", "/containers", { tab: "containers" }, ["containers"]],
    ["images tab", "/containers", { tab: "images" }, ["images"]],
    ["volumes tab", "/containers", { tab: "volumes" }, ["volumes"]],
    ["networks tab", "/containers", { tab: "networks" }, ["networks"]],
    ["disk usage tab", "/containers", { tab: "disk" }, ["disk"]],
    ["container detail keeps Containers active", "/containers/web-1", { tab: "logs" }, ["containers"]],
    ["ports default tab", "/ports", {}, ["ports"]],
    ["ingress tab", "/ports", { tab: "ingress" }, ["ingress"]],
    ["events", "/events", {}, ["events"]],
    ["events by severity", "/events", { severity: "warning" }, ["events"]],
    ["events of another kind", "/events", { kind: "lifecycle" }, ["events"]],
    ["audit log", "/events", { kind: "audit", severity: "critical" }, ["audit"]],
    ["settings", "/settings", {}, ["settings"]],
    ["settings section without its own item", "/settings", { section: "appearance" }, ["settings"]],
    ["API & MCP", "/settings", { section: "mcp" }, ["mcp"]],
    ["agents", "/settings", { section: "agents" }, ["agents"]],
    ["project detail", "/projects/blog", {}, ["projects"]],
    ["host vitals", "/host", {}, ["host"]],
    ["alerts", "/alerts", {}, ["alerts"]],
    ["alerts rules tab", "/alerts", { tab: "rules" }, ["alerts"]],
    ["notifications is not settings", "/settings/notifications", {}, ["notifications"]],
  ])("%s", (_name, pathname, search, expected) => {
    expect(activeIds(pathname, search)).toEqual(expected);
  });
});

describe("NAV_ITEMS", () => {
  it("places every item in a known section", () => {
    const sections = new Set(NAV_SECTIONS.map((s) => s.id));
    for (const item of NAV_ITEMS) expect(sections.has(item.section)).toBe(true);
  });

  it("has unique ids and unique destinations", () => {
    const ids = NAV_ITEMS.map((i) => i.id);
    const destinations = NAV_ITEMS.map((i) => `${i.to}?${new URLSearchParams(i.search).toString()}`);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(destinations).size).toBe(destinations.length);
  });
});

describe("navItemsFor", () => {
  it("shows Agents only in agent mode", () => {
    expect(navItemsFor("agent").some((i) => i.id === "agents")).toBe(true);
    expect(navItemsFor("showcase").some((i) => i.id === "agents")).toBe(false);
    expect(navItemsFor(undefined).some((i) => i.id === "agents")).toBe(false);
  });

  it("falls back to Settings for the agents section when Agents is hidden", () => {
    const items = navItemsFor("ssh");
    const active = items.filter((item) => isNavItemActive(item, "/settings", { section: "agents" }, items)).map((i) => i.id);
    expect(active).toEqual(["settings"]);
  });
});
