import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * tailwind-merge aware of the Asterism theme (styles/tokens.css). Without
 * these groups a custom size such as `text-body` reads as a text colour and
 * is dropped next to `text-foreground`, `shadow-card` reads as a shadow
 * colour, `h-control` is not recognised as a height and `rounded-card`
 * would not replace `rounded-button`.
 */
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      text: ["display", "title", "kpi", "heading", "body", "control", "small", "mono", "pill"],
      shadow: ["card", "raised", "pop", "focus"],
      spacing: ["control", "control-sm", "pill", "sidebar-item", "topbar"],
      ease: ["drawer"],
      radius: [
        "card",
        "tile",
        "control",
        "control-item",
        "button",
        "button-sm",
        "chip",
        "nav",
        "menu-item",
        "bar",
        "avatar",
      ],
    },
    classGroups: {
      duration: [{ duration: ["press", "menu-in", "menu-out", "tabs"] }],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
