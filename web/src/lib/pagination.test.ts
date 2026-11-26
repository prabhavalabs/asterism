import { describe, expect, it } from "vitest";
import { clampPage, pageRange, pageTokens, paginateLocal, showingLabel, totalPagesFor } from "./pagination";

describe("pageTokens", () => {
  it.each([
    [1, 1, [1]],
    [1, 5, [1, 2, 3, 4, 5]],
    [4, 7, [1, 2, 3, 4, 5, 6, 7]],
    [1, 10, [1, 2, 3, 4, 5, "ellipsis-end", 10]],
    [4, 10, [1, 2, 3, 4, 5, "ellipsis-end", 10]],
    [5, 10, [1, "ellipsis-start", 4, 5, 6, "ellipsis-end", 10]],
    [7, 10, [1, "ellipsis-start", 6, 7, 8, 9, 10]],
    [10, 10, [1, "ellipsis-start", 6, 7, 8, 9, 10]],
    [50, 100, [1, "ellipsis-start", 49, 50, 51, "ellipsis-end", 100]],
  ] as const)("page %i of %i", (page, total, expected) => {
    expect(pageTokens(page, total)).toEqual(expected);
  });

  it("keeps a constant length once pages exceed the slots", () => {
    for (let page = 1; page <= 30; page++) expect(pageTokens(page, 30)).toHaveLength(7);
    for (let page = 1; page <= 30; page++) expect(pageTokens(page, 30, 2)).toHaveLength(9);
  });

  it("shows a single-page gap as the page, never an ellipsis", () => {
    expect(pageTokens(4, 8)).toEqual([1, 2, 3, 4, 5, "ellipsis-end", 8]);
    expect(pageTokens(5, 8)).toEqual([1, "ellipsis-start", 4, 5, 6, 7, 8]);
  });

  it("clamps out-of-range pages", () => {
    expect(pageTokens(0, 10)).toEqual(pageTokens(1, 10));
    expect(pageTokens(99, 10)).toEqual(pageTokens(10, 10));
    expect(pageTokens(1, 0)).toEqual([1]);
  });

  it("honours wider sibling and boundary counts", () => {
    expect(pageTokens(10, 20, 2, 2)).toEqual([1, 2, "ellipsis-start", 8, 9, 10, 11, 12, "ellipsis-end", 19, 20]);
  });
});

describe("page arithmetic", () => {
  it("counts pages, never fewer than one", () => {
    expect(totalPagesFor(0, 25)).toBe(1);
    expect(totalPagesFor(25, 25)).toBe(1);
    expect(totalPagesFor(26, 25)).toBe(2);
    expect(totalPagesFor(137, 25)).toBe(6);
  });

  it("clamps pages", () => {
    expect(clampPage(0, 5)).toBe(1);
    expect(clampPage(7, 5)).toBe(5);
    expect(clampPage(2.7, 5)).toBe(2);
    expect(clampPage(Number.NaN, 5)).toBe(1);
  });

  it("computes the visible range", () => {
    expect(pageRange({ page: 2, pageSize: 25, totalSize: 137 })).toEqual({ from: 26, to: 50 });
    expect(pageRange({ page: 6, pageSize: 25, totalSize: 137 })).toEqual({ from: 126, to: 137 });
    expect(pageRange({ page: 1, pageSize: 25, totalSize: 0 })).toEqual({ from: 0, to: 0 });
    expect(pageRange({ page: 9, pageSize: 25, totalSize: 137 })).toEqual({ from: 0, to: 0 });
  });

  it("formats the summary", () => {
    expect(showingLabel({ page: 2, pageSize: 25, totalSize: 137 })).toBe("Showing 26–50 of 137");
    expect(showingLabel({ page: 1, pageSize: 25, totalSize: 1 }, "volumes")).toBe("Showing 1–1 of 1 volume");
    expect(showingLabel({ page: 1, pageSize: 100, totalSize: 1200 }, "events")).toBe("Showing 1–100 of 1,200 events");
    expect(showingLabel({ page: 1, pageSize: 25, totalSize: 0 }, "volumes")).toBe("No volumes");
    expect(showingLabel({ page: 1, pageSize: 25, totalSize: 0 })).toBe("No results");
    expect(showingLabel({ page: 1, pageSize: 25, totalSize: 1 }, ["entry", "entries"])).toBe("Showing 1–1 of 1 entry");
  });
});

describe("paginateLocal", () => {
  const items = Array.from({ length: 23 }, (_, i) => i + 1);

  it("slices a page and reports PageInfo", () => {
    expect(paginateLocal(items, 2, 10)).toEqual({
      items: [11, 12, 13, 14, 15, 16, 17, 18, 19, 20],
      pageInfo: { page: 2, pageSize: 10, totalSize: 23, totalPages: 3 },
    });
  });

  it("clamps a page past the end to the last page", () => {
    const { items: last, pageInfo } = paginateLocal(items, 9, 10);
    expect(last).toEqual([21, 22, 23]);
    expect(pageInfo.page).toBe(3);
  });

  it("handles an empty list", () => {
    expect(paginateLocal([], 3, 10)).toEqual({
      items: [],
      pageInfo: { page: 1, pageSize: 10, totalSize: 0, totalPages: 1 },
    });
  });
});
