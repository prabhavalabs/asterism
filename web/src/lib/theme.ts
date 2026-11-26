import { useCallback, useSyncExternalStore } from "react";

/**
 * Light, dark or system theme preference, stored in localStorage and applied
 * as the `dark` class on <html>. With nothing stored the OS preference
 * (prefers-color-scheme) wins. Theme changes are never animated: transitions
 * are switched off for the frame in which the class changes.
 */

export const THEME_PREFERENCES = ["light", "dark", "system"] as const;
export type ThemePreference = (typeof THEME_PREFERENCES)[number];
export type ResolvedTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "asterism-theme";

const DARK_QUERY = "(prefers-color-scheme: dark)";

export function isThemePreference(value: unknown): value is ThemePreference {
  return typeof value === "string" && (THEME_PREFERENCES as readonly string[]).includes(value);
}

function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    // Storage can throw when site data is blocked.
    return null;
  }
}

function systemPrefersDark(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.(DARK_QUERY).matches === true;
}

export function readThemePreference(): ThemePreference {
  try {
    const stored = storage()?.getItem(THEME_STORAGE_KEY);
    return isThemePreference(stored) ? stored : "system";
  } catch {
    return "system";
  }
}

export function resolveTheme(preference: ThemePreference, prefersDark: boolean): ResolvedTheme {
  if (preference === "system") return prefersDark ? "dark" : "light";
  return preference;
}

/** Sets the `dark` class and `color-scheme` on the root without animating anything. */
export function applyTheme(theme: ResolvedTheme, root: HTMLElement = document.documentElement): void {
  const isDark = theme === "dark";
  if (root.classList.contains("dark") === isDark && root.style.colorScheme === theme) return;

  const doc = root.ownerDocument;
  const pause = doc.createElement("style");
  pause.dataset.themeSwitch = "";
  pause.textContent =
    "*,*::before,*::after{-webkit-transition:none!important;transition:none!important}";
  doc.head.appendChild(pause);

  root.classList.toggle("dark", isDark);
  root.style.colorScheme = theme;

  // Flush styles with transitions off, then re-enable them after the frame.
  void doc.defaultView?.getComputedStyle(root).color;
  doc.defaultView?.setTimeout(() => pause.remove(), 1);
}

interface Snapshot {
  preference: ThemePreference;
  resolvedTheme: ResolvedTheme;
}

const listeners = new Set<() => void>();
let snapshot: Snapshot | null = null;

function computeSnapshot(): Snapshot {
  const preference = readThemePreference();
  return { preference, resolvedTheme: resolveTheme(preference, systemPrefersDark()) };
}

function commit(next: Snapshot): void {
  const changed =
    snapshot?.preference !== next.preference || snapshot?.resolvedTheme !== next.resolvedTheme;
  snapshot = next;
  applyTheme(next.resolvedTheme);
  if (changed) for (const listener of listeners) listener();
}

function refresh(): void {
  commit(computeSnapshot());
}

function getSnapshot(): Snapshot {
  snapshot ??= computeSnapshot();
  return snapshot;
}

const serverSnapshot: Snapshot = { preference: "system", resolvedTheme: "dark" };

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  applyTheme(getSnapshot().resolvedTheme);

  // Follow the OS while the preference is "system", and other tabs' changes.
  const media = window.matchMedia?.(DARK_QUERY);
  const onStorage = (event: StorageEvent) => {
    if (event.key === THEME_STORAGE_KEY) refresh();
  };
  media?.addEventListener("change", refresh);
  window.addEventListener("storage", onStorage);

  return () => {
    listeners.delete(listener);
    media?.removeEventListener("change", refresh);
    window.removeEventListener("storage", onStorage);
  };
}

/** Stores the preference, applies it and notifies every `useTheme` consumer. */
export function setThemePreference(preference: ThemePreference): void {
  try {
    if (preference === "system") storage()?.removeItem(THEME_STORAGE_KEY);
    else storage()?.setItem(THEME_STORAGE_KEY, preference);
  } catch {
    // Without storage the preference still applies for this page.
  }
  commit({ preference, resolvedTheme: resolveTheme(preference, systemPrefersDark()) });
}

/** Applies the stored preference. Call once before the first render. */
export function initTheme(): void {
  refresh();
}

export interface UseThemeResult {
  preference: ThemePreference;
  resolvedTheme: ResolvedTheme;
  setPreference: (next: ThemePreference) => void;
  /** Flips between light and dark (leaves "system"). */
  toggle: () => void;
}

export function useTheme(): UseThemeResult {
  const { preference, resolvedTheme } = useSyncExternalStore(
    subscribe,
    getSnapshot,
    () => serverSnapshot,
  );
  const setPreference = useCallback((next: ThemePreference) => setThemePreference(next), []);
  const toggle = useCallback(
    () => setThemePreference(getSnapshot().resolvedTheme === "dark" ? "light" : "dark"),
    [],
  );
  return { preference, resolvedTheme, setPreference, toggle };
}
