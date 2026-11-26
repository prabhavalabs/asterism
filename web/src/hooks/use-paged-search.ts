import { useCallback, useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  applyPagedPatch,
  clearPagedSearch,
  pagedQueryKey,
  pagedRequest,
  readPagedState,
  type PagedPatch,
  type PagedSearchConfig,
  type PagedState,
} from "@/lib/pagedSearch";
import { PAGE_SIZE_OPTIONS, type CountNoun, type PageInfo } from "@/lib/pagination";

export interface PagedSearch<F extends string = string> extends PagedState<F> {
  /** Page sizes for the footer select. */
  pageSizeOptions: readonly number[];
  setPage: (page: number) => void;
  setPageSize: (pageSize: number) => void;
  /** Replaces the history entry instead of pushing one, so typing does not flood back/forward. */
  setQuery: (query: string) => void;
  setOrder: (order: string) => void;
  setFilter: (name: F, value: string) => void;
  /** Several fields at once; page resets to 1 when the result set changes. */
  update: (patch: PagedPatch<F>, options?: { replace?: boolean }) => void;
  /** Back to defaults: clears every key of this list from the URL. */
  reset: () => void;
  /** TanStack Query key for the current page: `[...base, { page, pageSize, query, order, filters }]`. */
  queryKey: (base: readonly unknown[]) => readonly unknown[];
  /** `{ page: { page, pageSize }, query, orderBy }` for the paginated List RPCs. */
  request: ReturnType<typeof pagedRequest>;
}

/**
 * Page, page size, query, order and filters of one list, kept in the URL
 * search params so links, reload and back/forward restore the view.
 *
 * Pass a module-level `config` (its identity is a memo dependency). The
 * route's `validateSearch` must keep the keys: spread
 * `validatePagedSearch(search, config)` into it.
 *
 * ```tsx
 * const VOLUMES = { prefix: "vol", defaultOrder: "name", filters: { filter: { default: "all" } } } as const;
 * const paged = usePagedSearch(VOLUMES);
 * const query = useQuery({
 *   queryKey: paged.queryKey(containerKeys.volumes()),
 *   queryFn: () => dockerClient.listVolumes({ ...paged.request, unusedOnly: paged.filters.filter === "unused" }),
 *   placeholderData: keepPreviousData,
 * });
 * ```
 */
export function usePagedSearch<F extends string = never>(config: PagedSearchConfig<F>): PagedSearch<F> {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const state = useMemo(() => readPagedState(search, config), [search, config]);

  const update = useCallback(
    (patch: PagedPatch<F>, options?: { replace?: boolean }) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => applyPagedPatch(prev, patch, config),
        replace: options?.replace ?? false,
        resetScroll: false,
      });
    },
    [navigate, config],
  );

  const reset = useCallback(() => {
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => clearPagedSearch(prev, config),
      resetScroll: false,
    });
  }, [navigate, config]);

  return useMemo(
    () => ({
      ...state,
      pageSizeOptions: config.pageSizeOptions ?? PAGE_SIZE_OPTIONS,
      setPage: (page: number) => update({ page }),
      setPageSize: (pageSize: number) => update({ pageSize }),
      setQuery: (query: string) => update({ query }, { replace: true }),
      setOrder: (order: string) => update({ order }),
      setFilter: (name: F, value: string) => update({ filters: { [name]: value } as Partial<Record<F, string>> }),
      update,
      reset,
      queryKey: (base: readonly unknown[]) => pagedQueryKey(base, state),
      request: pagedRequest(state),
    }),
    [state, config.pageSizeOptions, update, reset],
  );
}

/** PageInfo as the RPCs return it (every field optional on the wire). */
export interface WirePageInfo {
  page: number;
  pageSize: number;
  totalSize: number;
  totalPages: number;
}

/** Footer props for DataTableFrame / PaginatedScrollArea `pagination`. */
export interface PagerProps extends PageInfo {
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  pageSizeOptions: readonly number[];
  noun?: CountNoun;
}

/**
 * Footer props for a server page, from the response's PageInfo (falling back
 * to the URL state while the first page loads). When the list shrinks under
 * the current page (rows deleted, a stale link), it moves to the last page
 * that exists, replacing the history entry.
 */
export function usePager<F extends string>(
  paged: PagedSearch<F>,
  pageInfo: WirePageInfo | undefined,
  noun?: CountNoun,
): PagerProps {
  const totalPages = pageInfo ? Math.max(1, pageInfo.totalPages) : 1;
  const stale = pageInfo !== undefined && pageInfo.totalSize > 0 && paged.page > totalPages;
  const { update } = paged;
  useEffect(() => {
    if (stale) update({ page: totalPages }, { replace: true });
  }, [stale, totalPages, update]);

  return {
    page: pageInfo?.page || paged.page,
    pageSize: pageInfo?.pageSize || paged.pageSize,
    totalSize: pageInfo?.totalSize ?? 0,
    totalPages,
    onPageChange: paged.setPage,
    onPageSizeChange: paged.setPageSize,
    pageSizeOptions: paged.pageSizeOptions,
    noun,
  };
}
