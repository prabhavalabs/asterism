import { useSyncExternalStore } from "react";

function subscribeTo(query: string) {
  return (onChange: () => void) => {
    if (typeof window === "undefined" || !window.matchMedia) return () => {};
    const mql = window.matchMedia(query);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  };
}

const subscribers = new Map<string, (onChange: () => void) => () => void>();

/** Live `matchMedia` result, correct on the first render. */
export function useMediaQuery(query: string): boolean {
  let subscribe = subscribers.get(query);
  if (!subscribe) {
    subscribe = subscribeTo(query);
    subscribers.set(query, subscribe);
  }
  return useSyncExternalStore(
    subscribe,
    () => (typeof window !== "undefined" && window.matchMedia ? window.matchMedia(query).matches : false),
    () => false,
  );
}

export function usePrefersReducedMotion(): boolean {
  return useMediaQuery("(prefers-reduced-motion: reduce)");
}

/** Tailwind `lg` (1024px) and up. */
export function useIsLargeScreen(): boolean {
  return useMediaQuery("(min-width: 1024px)");
}
