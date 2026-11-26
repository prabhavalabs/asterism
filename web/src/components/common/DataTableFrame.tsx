import { useEffect, useRef, type ReactNode } from "react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { DataPagination, type DataPaginationProps } from "./DataPagination";
import { LIST_MAX_HEIGHT, dataTableClasses } from "./dataTable";
import { ErrorState, LoadingState } from "./States";

/** The query fields the frame reads; a TanStack Query result fits as is. */
export interface DataFrameStatus {
  /** The loaded data; an error with no data shows the error state. */
  data?: unknown;
  /** True until the first data arrives. */
  isPending: boolean;
  isError: boolean;
  error?: unknown;
  /** True while any fetch runs, including a page change on placeholder data. */
  isFetching?: boolean;
  /** True while showing the previous page (`placeholderData: keepPreviousData`). */
  isPlaceholderData?: boolean;
  refetch?: () => unknown;
}

export interface PaginatedScrollAreaProps {
  children: ReactNode;
  /** Query state: drives the loading and error states. Omit for static data. */
  status?: DataFrameStatus;
  /** Whether the loaded page has no items; shows `empty` instead of children. */
  isEmpty?: boolean;
  /** Empty (or no-matches) state, typically an EmptyState with size="compact". */
  empty?: ReactNode;
  /** Custom loading placeholder; defaults to six skeleton rows. */
  loading?: ReactNode;
  /** Footer pagination. Omit for lists that are not paginated. */
  pagination?: DataPaginationProps;
  /** `both` (default) for wide tables, `vertical` for lists that wrap. */
  orientation?: "vertical" | "both";
  /** Height bound on the scrolling viewport. */
  maxHeightClassName?: string;
  /** Accessible name for the keyboard-focusable scroll region. */
  "aria-label"?: string;
  className?: string;
  viewportClassName?: string;
}

/**
 * A bounded ScrollArea with loading, error and empty states and an optional
 * DataPagination footer. The viewport scrolls back to the top whenever the
 * page, page size or total changes, and dims slightly while the next page
 * loads on top of the previous one.
 */
export function PaginatedScrollArea({
  children,
  status,
  isEmpty = false,
  empty,
  loading,
  pagination,
  orientation = "both",
  maxHeightClassName = LIST_MAX_HEIGHT,
  "aria-label": ariaLabel,
  className,
  viewportClassName,
}: PaginatedScrollAreaProps) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const pageKey = pagination ? `${pagination.page}:${pagination.pageSize}` : "";

  useEffect(() => {
    viewportRef.current?.scrollTo({ top: 0 });
  }, [pageKey]);

  let body: ReactNode;
  if (status?.isError && status.data === undefined) {
    body = (
      <ErrorState
        size="compact"
        error={status.error}
        onRetry={status.refetch ? () => void status.refetch?.() : undefined}
        retrying={status.isFetching}
      />
    );
  } else if (status?.isPending) {
    body = loading ?? <LoadingState variant="rows" count={6} />;
  } else if (isEmpty) {
    body = empty ?? null;
  } else {
    const refreshing = Boolean(status?.isPlaceholderData && status.isFetching);
    body = (
      <ScrollArea
        orientation={orientation}
        viewportRef={viewportRef}
        viewportClassName={cn(maxHeightClassName, viewportClassName)}
        viewportProps={{ tabIndex: 0, "aria-label": ariaLabel }}
        aria-busy={refreshing || undefined}
        className={cn("transition-opacity duration-tabs motion-reduce:transition-none", refreshing && "opacity-60")}
      >
        {children}
      </ScrollArea>
    );
  }

  const showFooter = pagination && !status?.isPending && pagination.totalSize > 0;

  return (
    <div data-slot="paginated-scroll-area" className={cn("flex min-w-0 flex-col", className)}>
      {body}
      {showFooter && <DataPagination {...pagination} />}
    </div>
  );
}

export interface DataTableFrameProps extends Omit<PaginatedScrollAreaProps, "orientation"> {
  /** Minimum table width before it scrolls sideways, e.g. "min-w-[860px]". */
  minWidthClassName?: string;
  tableClassName?: string;
  /** The table's `<thead>` and `<tbody>`; use `dataTableClasses` for sticky 32px headers and 40px rows. */
  children: ReactNode;
}

/**
 * A data table in a bounded two-axis ScrollArea, with the header row sticky
 * inside it, query states and the pagination footer. Put it in a
 * `SectionCard flush` so the card supplies the frame and the title.
 *
 * ```tsx
 * <DataTableFrame
 *   status={query}
 *   isEmpty={rows.length === 0}
 *   empty={<EmptyState size="compact" icon={DatabaseIcon} title="No volumes" />}
 *   minWidthClassName="min-w-[860px]"
 *   aria-label="Volumes"
 *   pagination={{ ...pageInfo, onPageChange: paged.setPage, onPageSizeChange: paged.setPageSize, noun: "volumes" }}
 * >
 *   <thead className={dataTableClasses.thead}>…</thead>
 *   <tbody>…</tbody>
 * </DataTableFrame>
 * ```
 */
export function DataTableFrame({ minWidthClassName, tableClassName, children, ...props }: DataTableFrameProps) {
  return (
    <PaginatedScrollArea orientation="both" {...props}>
      <table className={cn(dataTableClasses.table, minWidthClassName, tableClassName)}>{children}</table>
    </PaginatedScrollArea>
  );
}
