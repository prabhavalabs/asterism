/**
 * URL search-param state for one paginated list: page, page size, search
 * query, sort order and named filters. Pure functions so the route
 * validators, the `usePagedSearch` hook and the tests share one definition.
 *
 * Keys are optionally namespaced so a page can hold several lists: with
 * `prefix: "vol"` the keys are `volPage`, `volSize`, `volQ`, `volOrder` and
 * `volFilter` (for a filter named `filter`). Defaults are never written, so a
 * list in its default state adds nothing to the URL.
 */
import { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE, MIN_PAGE_SIZE } from "./pagination";

export interface PagedFilterSpec {
  /** Value meaning "no filter"; omitted from the URL. */
  default: string;
  /** Allowed values. Anything else in the URL falls back to the default. */
  values?: readonly string[];
}

export interface PagedSearchConfig<F extends string = string> {
  /** Namespace for the keys when a page has more than one list. */
  prefix?: string;
  defaultPageSize?: number;
  /** Server order_by used when the URL has none (AIP-132 style, e.g. "size desc"). */
  defaultOrder?: string;
  /** Orders the list accepts; others fall back to the default. Omit to accept any string. */
  orders?: readonly string[];
  /** Page sizes the footer offers; the URL accepts any size in the server's 10..200 range. */
  pageSizeOptions?: readonly number[];
  filters?: Readonly<Record<F, PagedFilterSpec>>;
}

export interface PagedState<F extends string = string> {
  page: number;
  pageSize: number;
  query: string;
  order: string;
  filters: Record<F, string>;
}

export type PagedPatch<F extends string = string> = Partial<Omit<PagedState<F>, "filters">> & {
  filters?: Partial<Record<F, string>>;
};

type Search = Record<string, unknown>;

/** The URL key for one field: `page` alone, or `volPage` with prefix "vol". */
export function pagedKey(prefix: string | undefined, name: string): string {
  if (!prefix) return name;
  return prefix + name.charAt(0).toUpperCase() + name.slice(1);
}

function keys<F extends string>(config: PagedSearchConfig<F>) {
  return {
    page: pagedKey(config.prefix, "page"),
    pageSize: pagedKey(config.prefix, "size"),
    query: pagedKey(config.prefix, "q"),
    order: pagedKey(config.prefix, "order"),
    filter: (name: string) => pagedKey(config.prefix, name),
  };
}

function filterEntries<F extends string>(config: PagedSearchConfig<F>): [F, PagedFilterSpec][] {
  return Object.entries(config.filters ?? {}) as [F, PagedFilterSpec][];
}

function toInt(value: unknown): number | undefined {
  const n = typeof value === "number" ? value : typeof value === "string" && value.trim() !== "" ? Number(value) : NaN;
  return Number.isInteger(n) ? n : undefined;
}

function toStr(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return undefined;
}

export function defaultPagedState<F extends string>(config: PagedSearchConfig<F>): PagedState<F> {
  const filters = {} as Record<F, string>;
  for (const [name, spec] of filterEntries(config)) filters[name] = spec.default;
  return {
    page: 1,
    pageSize: config.defaultPageSize ?? DEFAULT_PAGE_SIZE,
    query: "",
    order: config.defaultOrder ?? "",
    filters,
  };
}

/** Reads the list state from a search object, falling back to defaults for missing or invalid values. */
export function readPagedState<F extends string>(search: Search, config: PagedSearchConfig<F>): PagedState<F> {
  const k = keys(config);
  const state = defaultPagedState(config);

  const page = toInt(search[k.page]);
  if (page !== undefined && page >= 1) state.page = page;

  const size = toInt(search[k.pageSize]);
  if (size !== undefined && size >= MIN_PAGE_SIZE && size <= MAX_PAGE_SIZE) state.pageSize = size;

  const query = toStr(search[k.query]);
  if (query !== undefined) state.query = query;

  const order = toStr(search[k.order]);
  if (order !== undefined && order.trim() !== "" && (!config.orders || config.orders.includes(order))) {
    state.order = order;
  }

  for (const [name, spec] of filterEntries(config)) {
    const value = toStr(search[k.filter(name)]);
    if (value !== undefined && (!spec.values || spec.values.includes(value))) state.filters[name] = value;
  }
  return state;
}

/** The search entries for a state, leaving out every value that equals its default. */
export function pagedSearchEntries<F extends string>(
  state: PagedState<F>,
  config: PagedSearchConfig<F>,
): Record<string, string | number> {
  const k = keys(config);
  const defaults = defaultPagedState(config);
  const out: Record<string, string | number> = {};
  if (state.page > 1) out[k.page] = state.page;
  if (state.pageSize !== defaults.pageSize) out[k.pageSize] = state.pageSize;
  if (state.query !== "") out[k.query] = state.query;
  if (state.order !== defaults.order) out[k.order] = state.order;
  for (const [name, spec] of filterEntries(config)) {
    if (state.filters[name] !== spec.default) out[k.filter(name)] = state.filters[name];
  }
  return out;
}

/**
 * For a route's `validateSearch`: the list's valid, non-default keys. Spread
 * it into the route's own validated search so the keys survive validation.
 */
export function validatePagedSearch<F extends string>(
  search: Search,
  config: PagedSearchConfig<F>,
): Record<string, string | number> {
  return pagedSearchEntries(readPagedState(search, config), config);
}

/** Whether a patch changes what the list matches or how it is cut into pages. */
function changesResultSet<F extends string>(current: PagedState<F>, patch: PagedPatch<F>): boolean {
  if (patch.pageSize !== undefined && patch.pageSize !== current.pageSize) return true;
  if (patch.query !== undefined && patch.query !== current.query) return true;
  if (patch.order !== undefined && patch.order !== current.order) return true;
  for (const [name, value] of Object.entries(patch.filters ?? {}) as [F, string | undefined][]) {
    if (value !== undefined && value !== current.filters[name]) return true;
  }
  return false;
}

/**
 * Applies a patch to a search object and returns the new one. Keys of other
 * lists and unrelated params (such as `tab`) are kept. Changing the page
 * size, query, order or any filter goes back to page 1 unless the patch sets
 * a page itself.
 */
export function applyPagedPatch<F extends string>(search: Search, patch: PagedPatch<F>, config: PagedSearchConfig<F>): Search {
  const current = readPagedState(search, config);
  const next: PagedState<F> = {
    page: patch.page ?? (changesResultSet(current, patch) ? 1 : current.page),
    pageSize: patch.pageSize ?? current.pageSize,
    query: patch.query ?? current.query,
    order: patch.order ?? current.order,
    filters: { ...current.filters },
  };
  for (const [name, value] of Object.entries(patch.filters ?? {}) as [F, string | undefined][]) {
    if (value !== undefined) next.filters[name] = value;
  }
  next.page = Math.max(1, Math.trunc(next.page));

  const out: Search = { ...search };
  const k = keys(config);
  for (const key of [k.page, k.pageSize, k.query, k.order, ...filterEntries(config).map(([name]) => k.filter(name))]) {
    delete out[key];
  }
  return { ...out, ...pagedSearchEntries(next, config) };
}

/** Removes every key of the list, returning it to its default state. */
export function clearPagedSearch<F extends string>(search: Search, config: PagedSearchConfig<F>): Search {
  return applyPagedPatch(search, defaultPagedState(config) as PagedPatch<F>, config);
}

/**
 * TanStack Query key for one page: the base key plus everything that shapes
 * the result. Keys of different pages differ, so `keepPreviousData` can show
 * the last page while the next one loads.
 */
export function pagedQueryKey<F extends string>(base: readonly unknown[], state: PagedState<F>): readonly unknown[] {
  return [
    ...base,
    {
      page: state.page,
      pageSize: state.pageSize,
      query: state.query,
      order: state.order,
      filters: state.filters,
    },
  ];
}

/**
 * The request fields the paginated List RPCs take (pagination contract):
 * `page { page, page_size }`, `query` and `order_by`. Filters stay with the
 * caller, which maps them to each RPC's own filter fields.
 */
export function pagedRequest<F extends string>(state: PagedState<F>): {
  page: { page: number; pageSize: number };
  query: string;
  orderBy: string;
} {
  return { page: { page: state.page, pageSize: state.pageSize }, query: state.query.trim(), orderBy: state.order };
}
