import type * as React from "react";
import { HugeiconsIcon, type HugeiconsIconProps, type IconSvgElement } from "@hugeicons/react";

/** Icon data from `@hugeicons/core-free-icons` (for example `CpuIcon`). */
export type IconData = IconSvgElement;

export interface IconProps extends Omit<HugeiconsIconProps, "icon" | "ref"> {
  icon: IconData;
  ref?: React.Ref<SVGSVGElement>;
}

/**
 * Hugeicons glyph in the current text colour. Size comes from the parent
 * (`[&_svg]:size-4` in controls) or a `size-*` class; stroke 2 matches the
 * preset. Decorative by default: pass `aria-hidden={false}` plus a label to
 * expose it.
 */
function Icon({ icon, strokeWidth = 2, ...props }: IconProps) {
  return (
    <HugeiconsIcon
      data-slot="icon"
      aria-hidden="true"
      focusable="false"
      icon={icon}
      strokeWidth={strokeWidth}
      {...props}
    />
  );
}

export { Icon };
