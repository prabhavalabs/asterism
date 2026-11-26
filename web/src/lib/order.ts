/**
 * AIP-132 `order_by` strings as the List RPCs take them: a comma-separated
 * list of fields, each optionally followed by `desc` (`"memory desc"`,
 * `"project, name"`). The UI only ever sets one field; the first field
 * decides which column header shows as sorted.
 */

export interface OrderTerm {
  field: string;
  desc: boolean;
}

/** The first term of an order string; `{ field: "", desc: false }` when empty. */
export function parseOrder(order: string): OrderTerm {
  const first = order.split(",")[0]?.trim() ?? "";
  const [field = "", dir = ""] = first.split(/\s+/);
  return { field, desc: dir.toLowerCase() === "desc" };
}

export function formatOrder({ field, desc }: OrderTerm): string {
  return desc ? `${field} desc` : field;
}

/**
 * The order after clicking a column header: the same field flips direction,
 * another field starts in its natural direction (`defaultDesc` for numbers
 * where the biggest value is the interesting one).
 */
export function toggleOrder(current: string, field: string, defaultDesc = false): string {
  const term = parseOrder(current);
  if (term.field === field) return formatOrder({ field, desc: !term.desc });
  return formatOrder({ field, desc: defaultDesc });
}

/** `aria-sort` for a header given the list's current order. */
export function ariaSortFor(current: string, field: string): "ascending" | "descending" | "none" {
  const term = parseOrder(current);
  if (term.field !== field) return "none";
  return term.desc ? "descending" : "ascending";
}
