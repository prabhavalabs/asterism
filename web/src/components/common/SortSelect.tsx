import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface SortOption {
  /** An `order_by` string, e.g. "memory desc". */
  value: string;
  label: string;
}

export interface SortSelectProps {
  value: string;
  onValueChange: (order: string) => void;
  options: readonly SortOption[];
  /** `sm` for card headers where every control is sm. */
  size?: "default" | "sm";
  /** Shown before the value inside the trigger ("Sort: Memory"). */
  prefix?: string;
  className?: string;
}

/**
 * Server-side sort for lists without column headers (card grids, phone
 * layouts). An order that is not in `options` (a column header picked it)
 * is still shown, as "Custom".
 */
export function SortSelect({ value, onValueChange, options, size = "default", prefix = "Sort", className }: SortSelectProps) {
  const current = options.find((o) => o.value === value);
  return (
    <Select value={value} onValueChange={onValueChange}>
      <SelectTrigger size={size} aria-label={`${prefix}: ${current?.label ?? "Custom"}`} className={cn("min-w-40 justify-start [&>svg:last-child]:ml-auto", className)}>
        <span aria-hidden="true" className="text-muted-foreground">
          {prefix}
        </span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
        {!current && value && (
          <SelectItem value={value} className="hidden">
            Custom
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  );
}
