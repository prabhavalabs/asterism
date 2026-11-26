import { useId } from "react";
import {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationFirst,
  PaginationItem,
  PaginationLast,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from "@/components/ui/pagination";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PAGE_SIZE_OPTIONS, clampPage, pageTokens, showingLabel, totalPagesFor, type CountNoun } from "@/lib/pagination";
import { cn } from "@/lib/utils";

export interface DataPaginationProps {
  /** 1-based current page. */
  page: number;
  pageSize: number;
  /** Items matching the filters across all pages (PageInfo.total_size). */
  totalSize: number;
  /** PageInfo.total_pages; derived from totalSize and pageSize when omitted. */
  totalPages?: number;
  onPageChange: (page: number) => void;
  /** Shows the "Rows per page" select when given. */
  onPageSizeChange?: (pageSize: number) => void;
  pageSizeOptions?: readonly number[];
  /** Noun for the summary: "Showing 1–25 of 137 volumes" (plural, or [singular, plural]). */
  noun?: CountNoun;
  /** Page numbers either side of the current page (default 1). */
  siblings?: number;
  /** Disables every control, for example while the first page loads. */
  disabled?: boolean;
  className?: string;
}

/**
 * Table footer: "Showing 26–50 of 137", a rows-per-page select and
 * first / previous / numbered pages with ellipses / next / last. Every
 * control is --control-h-sm (28px). Below `sm` the numbers collapse to a
 * "2 / 6" readout so the row fits a phone. Keyboard: every control is a
 * real button in tab order; the current page carries aria-current.
 */
export function DataPagination({
  page,
  pageSize,
  totalSize,
  totalPages: totalPagesProp,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = PAGE_SIZE_OPTIONS,
  noun,
  siblings = 1,
  disabled = false,
  className,
}: DataPaginationProps) {
  const sizeLabelId = useId();
  const totalPages = Math.max(1, totalPagesProp ?? totalPagesFor(totalSize, pageSize));
  const current = clampPage(page, totalPages);
  const atStart = current <= 1;
  const atEnd = current >= totalPages;
  const go = (next: number) => {
    const target = clampPage(next, totalPages);
    if (target !== current) onPageChange(target);
  };
  const sizes = pageSizeOptions.includes(pageSize)
    ? pageSizeOptions
    : [...pageSizeOptions, pageSize].sort((a, b) => a - b);

  return (
    <div
      data-slot="data-pagination"
      className={cn(
        "flex min-h-11 flex-wrap items-center gap-x-4 gap-y-2 border-t border-divider px-4 py-2 text-small text-muted-foreground",
        className,
      )}
    >
      <p aria-live="polite" className="min-w-0 truncate tabular-nums">
        {showingLabel({ page: current, pageSize, totalSize }, noun)}
      </p>

      <div className="ml-auto flex flex-wrap items-center gap-x-4 gap-y-2">
        {onPageSizeChange && (
          <div className="flex items-center gap-2">
            <span id={sizeLabelId} className="hidden whitespace-nowrap sm:inline">
              Rows per page
            </span>
            <Select
              value={String(pageSize)}
              onValueChange={(value) => onPageSizeChange(Number(value))}
              disabled={disabled}
            >
              <SelectTrigger size="sm" aria-labelledby={sizeLabelId} className="min-w-16">
                <SelectValue />
              </SelectTrigger>
              <SelectContent align="end">
                {sizes.map((size) => (
                  <SelectItem key={size} value={String(size)} className="tabular-nums">
                    {size}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <Pagination>
          <PaginationContent>
            <PaginationItem>
              <PaginationFirst disabled={disabled || atStart} onClick={() => go(1)} />
            </PaginationItem>
            <PaginationItem>
              <PaginationPrevious disabled={disabled || atStart} onClick={() => go(current - 1)} />
            </PaginationItem>
            {pageTokens(current, totalPages, siblings).map((token) =>
              typeof token === "number" ? (
                <PaginationItem key={token} className="hidden sm:block">
                  <PaginationLink
                    isActive={token === current}
                    disabled={disabled}
                    aria-label={`Page ${token}`}
                    onClick={() => go(token)}
                  >
                    {token}
                  </PaginationLink>
                </PaginationItem>
              ) : (
                <PaginationItem key={token} className="hidden sm:block">
                  <PaginationEllipsis />
                </PaginationItem>
              ),
            )}
            <PaginationItem className="flex h-control-sm items-center px-2 text-foreground tabular-nums sm:hidden">
              <span className="sr-only">Page </span>
              {current}
              <span className="text-text-4">&nbsp;/&nbsp;</span>
              <span className="sr-only">of </span>
              {totalPages}
            </PaginationItem>
            <PaginationItem>
              <PaginationNext disabled={disabled || atEnd} onClick={() => go(current + 1)} />
            </PaginationItem>
            <PaginationItem>
              <PaginationLast disabled={disabled || atEnd} onClick={() => go(totalPages)} />
            </PaginationItem>
          </PaginationContent>
        </Pagination>
      </div>
    </div>
  );
}
