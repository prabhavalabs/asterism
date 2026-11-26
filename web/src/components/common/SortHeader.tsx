import type { ReactNode } from "react";
import { ArrowDown01Icon, ArrowUp01Icon, ArrowUpDownIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { ariaSortFor, toggleOrder } from "@/lib/order";
import { cn } from "@/lib/utils";
import { dataTableClasses } from "./dataTable";

export interface SortHeaderProps {
  label: ReactNode;
  /** The server `order_by` field this column sorts by. */
  field: string;
  /** The list's current order string (`paged.order`). */
  order: string;
  onOrderChange: (order: string) => void;
  /** Start descending when this column becomes the sort (numbers, sizes, dates). */
  defaultDesc?: boolean;
  align?: "left" | "right";
  className?: string;
}

/**
 * A sticky 32px table header cell whose label is a button that sorts the
 * list server-side by `field`; clicking again flips the direction.
 * `aria-sort` follows the current order.
 */
export function SortHeader({ label, field, order, onOrderChange, defaultDesc = false, align = "left", className }: SortHeaderProps) {
  const sort = ariaSortFor(order, field);
  const active = sort !== "none";
  return (
    <th scope="col" aria-sort={sort} className={cn(dataTableClasses.th, align === "right" && "text-right", className)}>
      <button
        type="button"
        onClick={() => onOrderChange(toggleOrder(order, field, defaultDesc))}
        className={cn(
          "-mx-1 inline-flex h-6 items-center gap-1 rounded-sm px-1 outline-none transition-colors hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring",
          active && "text-foreground",
        )}
      >
        {label}
        <Icon
          icon={active ? (sort === "descending" ? ArrowDown01Icon : ArrowUp01Icon) : ArrowUpDownIcon}
          className={cn("size-3.5", !active && "text-text-4")}
        />
      </button>
    </th>
  );
}
