import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { validatePagedSearch, type PagedSearchConfig } from "@/lib/pagedSearch";
import { usePagedSearch, type PagedSearch } from "./use-paged-search";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}

const LIST = {
  prefix: "vol",
  filters: { filter: { default: "all", values: ["all", "unused"] } },
} as const satisfies PagedSearchConfig<"filter">;

let host: HTMLDivElement;
let root: Root;
let latest: PagedSearch<"filter"> | undefined;

function Probe() {
  latest = usePagedSearch(LIST);
  return null;
}

async function mount(url: string) {
  const rootRoute = createRootRoute();
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/list",
    component: Probe,
    validateSearch: (search: Record<string, unknown>) => ({
      ...(typeof search.tab === "string" ? { tab: search.tab } : {}),
      ...validatePagedSearch(search, LIST),
    }),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([listRoute]),
    history: createMemoryHistory({ initialEntries: [url] }),
  });
  await act(async () => {
    await router.load();
    root.render(<RouterProvider router={router} />);
  });
  return router;
}

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  // jsdom has no scrolling; the router restores scroll after navigation.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  latest = undefined;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("usePagedSearch", () => {
  it("reads the list state from the URL", async () => {
    await mount("/list?tab=volumes&volPage=3&volSize=50&volFilter=unused");
    expect(latest).toMatchObject({ page: 3, pageSize: 50, query: "", filters: { filter: "unused" } });
    expect(latest?.request).toEqual({ page: { page: 3, pageSize: 50 }, query: "", orderBy: "" });
    expect(latest?.queryKey(["volumes"])).toEqual([
      "volumes",
      { page: 3, pageSize: 50, query: "", order: "", filters: { filter: "unused" } },
    ]);
  });

  it("writes changes to the URL, resetting the page when the query changes", async () => {
    const router = await mount("/list?tab=volumes&volPage=3");
    await act(async () => latest?.setPage(4));
    expect(router.state.location.search).toEqual({ tab: "volumes", volPage: 4 });
    expect(latest?.page).toBe(4);

    await act(async () => latest?.setQuery("db"));
    expect(router.state.location.search).toEqual({ tab: "volumes", volQ: "db" });
    expect(latest).toMatchObject({ page: 1, query: "db" });

    await act(async () => latest?.reset());
    expect(router.state.location.search).toEqual({ tab: "volumes" });
  });
});
