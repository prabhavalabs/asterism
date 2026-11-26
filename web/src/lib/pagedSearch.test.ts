import { describe, expect, it } from "vitest";
import {
  applyPagedPatch,
  clearPagedSearch,
  pagedKey,
  pagedQueryKey,
  pagedRequest,
  readPagedState,
  validatePagedSearch,
  type PagedSearchConfig,
} from "./pagedSearch";

const VOLUMES = {
  prefix: "vol",
  filters: { filter: { default: "all", values: ["all", "in-use", "unused"] } },
} as const satisfies PagedSearchConfig<"filter">;

const EVENTS = {
  defaultOrder: "ts desc",
  orders: ["ts desc", "ts"],
  defaultPageSize: 50,
  filters: { kind: { default: "all" }, severity: { default: "all", values: ["all", "critical", "warning", "info"] } },
} as const satisfies PagedSearchConfig<"kind" | "severity">;

describe("pagedKey", () => {
  it("namespaces keys with a camel-case prefix", () => {
    expect(pagedKey(undefined, "page")).toBe("page");
    expect(pagedKey("vol", "page")).toBe("volPage");
    expect(pagedKey("vol", "q")).toBe("volQ");
  });
});

describe("readPagedState", () => {
  it("falls back to defaults", () => {
    expect(readPagedState({}, VOLUMES)).toEqual({
      page: 1,
      pageSize: 25,
      query: "",
      order: "",
      filters: { filter: "all" },
    });
    expect(readPagedState({}, EVENTS)).toMatchObject({ pageSize: 50, order: "ts desc", filters: { kind: "all", severity: "all" } });
  });

  it("reads numbers and numeric strings, rejecting invalid values", () => {
    const search = { volPage: "3", volSize: 50, volQ: "data", volFilter: "unused" };
    expect(readPagedState(search, VOLUMES)).toEqual({
      page: 3,
      pageSize: 50,
      query: "data",
      order: "",
      filters: { filter: "unused" },
    });
    const bad = { volPage: 0, volSize: 5000, volFilter: "secret", volQ: { x: 1 } };
    expect(readPagedState(bad, VOLUMES)).toEqual(readPagedState({}, VOLUMES));
    expect(readPagedState({ volPage: "2.5", volSize: "abc" }, VOLUMES)).toMatchObject({ page: 1, pageSize: 25 });
  });

  it("ignores keys of other lists", () => {
    expect(readPagedState({ page: 4, imgPage: 2 }, VOLUMES).page).toBe(1);
  });

  it("accepts only listed orders when the config lists them", () => {
    expect(readPagedState({ order: "ts" }, EVENTS).order).toBe("ts");
    expect(readPagedState({ order: "password" }, EVENTS).order).toBe("ts desc");
  });
});

describe("applyPagedPatch", () => {
  it("sets the page and keeps unrelated params", () => {
    expect(applyPagedPatch({ tab: "volumes", imgPage: 2 }, { page: 3 }, VOLUMES)).toEqual({
      tab: "volumes",
      imgPage: 2,
      volPage: 3,
    });
  });

  it("omits defaults from the URL", () => {
    expect(applyPagedPatch({ tab: "volumes", volPage: 3 }, { page: 1 }, VOLUMES)).toEqual({ tab: "volumes" });
    expect(applyPagedPatch({ volFilter: "unused" }, { filters: { filter: "all" } }, VOLUMES)).toEqual({});
    expect(applyPagedPatch({}, { pageSize: 25 }, VOLUMES)).toEqual({});
  });

  it.each([
    ["query", { query: "db" }, { volQ: "db" }],
    ["page size", { pageSize: 50 }, { volSize: 50 }],
    ["order", { order: "name" }, { volOrder: "name" }],
    ["filter", { filters: { filter: "unused" as const } }, { volFilter: "unused" }],
  ])("resets to page 1 when the %s changes", (_name, patch, expected) => {
    expect(applyPagedPatch({ volPage: 4 }, patch, VOLUMES)).toEqual(expected);
  });

  it("keeps the page when a patch repeats the current values", () => {
    expect(applyPagedPatch({ volPage: 4, volQ: "db" }, { query: "db" }, VOLUMES)).toEqual({ volPage: 4, volQ: "db" });
  });

  it("lets an explicit page win over the reset", () => {
    expect(applyPagedPatch({ volPage: 4 }, { query: "db", page: 2 }, VOLUMES)).toEqual({ volPage: 2, volQ: "db" });
  });

  it("drops invalid values already in the URL when writing", () => {
    expect(applyPagedPatch({ volFilter: "secret", volSize: 9999 }, { page: 2 }, VOLUMES)).toEqual({ volPage: 2 });
  });

  it("clears the whole list", () => {
    expect(clearPagedSearch({ tab: "volumes", volPage: 3, volQ: "x", volFilter: "unused" }, VOLUMES)).toEqual({
      tab: "volumes",
    });
  });
});

describe("validatePagedSearch", () => {
  it("returns only the list's valid, non-default keys", () => {
    expect(
      validatePagedSearch({ tab: "volumes", volPage: "2", volSize: 25, volFilter: "nope", volQ: "x" }, VOLUMES),
    ).toEqual({ volPage: 2, volQ: "x" });
  });
});

describe("query key and request", () => {
  const state = readPagedState({ page: 2, size: 10, q: " web ", order: "ts", severity: "critical" }, EVENTS);

  it("builds a query key that changes with every input", () => {
    expect(pagedQueryKey(["events"], state)).toEqual([
      "events",
      { page: 2, pageSize: 10, query: " web ", order: "ts", filters: { kind: "all", severity: "critical" } },
    ]);
    const next = readPagedState({ page: 3, size: 10, q: " web ", order: "ts", severity: "critical" }, EVENTS);
    expect(pagedQueryKey(["events"], next)).not.toEqual(pagedQueryKey(["events"], state));
  });

  it("maps to the RPC page request with a trimmed query", () => {
    expect(pagedRequest(state)).toEqual({ page: { page: 2, pageSize: 10 }, query: "web", orderBy: "ts" });
  });
});
