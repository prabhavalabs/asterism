/** Shared sizing for DataTableFrame / PaginatedScrollArea. */

/** Default bound for list scroll areas: the page never grows with the list. */
export const LIST_MAX_HEIGHT = "max-h-[min(72vh,880px)]";

/** Header and body cell classes for tables inside DataTableFrame (32px sticky header, 40px rows). */
export const dataTableClasses = {
  table: "w-full table-fixed border-separate border-spacing-0 text-left text-control",
  thead: "text-small text-muted-foreground",
  th: "sticky top-0 z-10 h-8 border-b border-divider bg-surface-subtle px-3 font-medium whitespace-nowrap",
  td: "h-10 border-b border-divider px-3 align-middle",
  tr: "transition-colors hover:bg-muted/50",
} as const;
