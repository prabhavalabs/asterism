/**
 * Page arithmetic shared by the pagination UI, the URL state and the
 * client-side fallback. Pages are 1-based, matching the server's PageRequest
 * and PageInfo (docs: pagination contract).
 */

/** Page sizes offered by the footer select. */
export const PAGE_SIZE_OPTIONS = [10, 25, 50, 100] as const;
export const DEFAULT_PAGE_SIZE = 25;
/** The server clamps page_size to this range; the URL parser does the same. */
export const MIN_PAGE_SIZE = 10;
export const MAX_PAGE_SIZE = 200;

/** Same shape as the server's PageInfo, so client and server pages are interchangeable. */
export interface PageInfo {
  page: number;
  pageSize: number;
  /** Items matching the filters, across all pages. */
  totalSize: number;
  /** Always at least 1, even when there are no items. */
  totalPages: number;
}

export function totalPagesFor(totalSize: number, pageSize: number): number {
  if (!Number.isFinite(totalSize) || totalSize <= 0 || pageSize <= 0) return 1;
  return Math.ceil(totalSize / pageSize);
}

export function clampPage(page: number, totalPages: number): number {
  if (!Number.isFinite(page)) return 1;
  return Math.min(Math.max(1, Math.trunc(page)), Math.max(1, totalPages));
}

/** 1-based inclusive item range shown on a page; `{ from: 0, to: 0 }` when the page is empty. */
export function pageRange(info: Pick<PageInfo, "page" | "pageSize" | "totalSize">): { from: number; to: number } {
  const from = (info.page - 1) * info.pageSize + 1;
  if (info.totalSize <= 0 || from > info.totalSize) return { from: 0, to: 0 };
  return { from, to: Math.min(info.page * info.pageSize, info.totalSize) };
}

const countFormat = new Intl.NumberFormat("en-US");

/** A plural noun, or `[singular, plural]` when the singular is not the plural minus "s". */
export type CountNoun = string | readonly [one: string, other: string];

function nounFor(noun: CountNoun | undefined, count: number): string {
  if (!noun) return "";
  if (typeof noun !== "string") return ` ${count === 1 ? noun[0] : noun[1]}`;
  return ` ${count === 1 && noun.endsWith("s") ? noun.slice(0, -1) : noun}`;
}

/** "Showing 26–50 of 137 volumes" (en dash), or "No volumes" when nothing matches. */
export function showingLabel(info: Pick<PageInfo, "page" | "pageSize" | "totalSize">, noun?: CountNoun): string {
  const { from, to } = pageRange(info);
  if (info.totalSize <= 0) return noun ? `No${nounFor(noun, 0)}` : "No results";
  const total = countFormat.format(info.totalSize);
  const suffix = nounFor(noun, info.totalSize);
  if (from === 0) return `Showing 0 of ${total}${suffix}`;
  return `Showing ${countFormat.format(from)}–${countFormat.format(to)} of ${total}${suffix}`;
}

export type PageToken = number | "ellipsis-start" | "ellipsis-end";

/**
 * Page buttons to draw: the first and last `boundaries` pages, `siblings`
 * pages either side of the current one, and an ellipsis for each gap. Once
 * there are more pages than slots the list always has the same length
 * (2 × boundaries + 2 × siblings + 3), so the controls never change width
 * while paging. A gap of exactly one page shows that page instead of an
 * ellipsis.
 *
 *   pageTokens(1, 10)  → 1 2 3 4 5 … 10
 *   pageTokens(5, 10)  → 1 … 4 5 6 … 10
 *   pageTokens(10, 10) → 1 … 6 7 8 9 10
 */
export function pageTokens(page: number, totalPages: number, siblings = 1, boundaries = 1): PageToken[] {
  const total = Math.max(1, Math.trunc(totalPages));
  const current = clampPage(page, total);
  const slots = boundaries * 2 + siblings * 2 + 3;
  if (total <= slots) return range(1, total);

  const startPages = range(1, boundaries);
  const endPages = range(total - boundaries + 1, total);
  const siblingsStart = Math.max(
    Math.min(current - siblings, total - boundaries - siblings * 2 - 1),
    boundaries + 2,
  );
  const siblingsEnd = Math.min(
    Math.max(current + siblings, boundaries + siblings * 2 + 2),
    total - boundaries - 1,
  );

  return [
    ...startPages,
    siblingsStart > boundaries + 2 ? "ellipsis-start" : boundaries + 1,
    ...range(siblingsStart, siblingsEnd),
    siblingsEnd < total - boundaries - 1 ? "ellipsis-end" : total - boundaries,
    ...endPages,
  ];
}

function range(start: number, end: number): number[] {
  const out: number[] = [];
  for (let i = start; i <= end; i++) out.push(i);
  return out;
}

/**
 * Slices an already-loaded list into one page, clamping a page past the end
 * to the last page. Stand-in for server paging on lists the API still returns
 * whole; the result has the same PageInfo shape the server will return.
 */
export function paginateLocal<T>(
  items: readonly T[],
  page: number,
  pageSize: number,
): { items: T[]; pageInfo: PageInfo } {
  const totalPages = totalPagesFor(items.length, pageSize);
  const current = clampPage(page, totalPages);
  const start = (current - 1) * pageSize;
  return {
    items: items.slice(start, start + pageSize),
    pageInfo: { page: current, pageSize, totalSize: items.length, totalPages },
  };
}
