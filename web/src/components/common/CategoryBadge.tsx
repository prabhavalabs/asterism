import { Badge, type BadgeProps } from "@/components/ui/badge";
import { categoryLabel } from "./labels";

export interface CategoryBadgeProps extends Omit<BadgeProps, "children" | "variant"> {
  category: string;
}

/** Project category as a sentence-case outline badge (22px). */
export function CategoryBadge({ category, ...props }: CategoryBadgeProps) {
  return (
    <Badge variant="outline" data-slot="category-badge" {...props}>
      {categoryLabel(category)}
    </Badge>
  );
}
