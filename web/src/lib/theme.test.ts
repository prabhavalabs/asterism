import { describe, expect, it } from "vitest";
import { isThemePreference, resolveTheme } from "./theme";

describe("resolveTheme", () => {
  it.each([
    ["system", true, "dark"],
    ["system", false, "light"],
    ["light", true, "light"],
    ["dark", false, "dark"],
  ] as const)("%s with prefersDark=%s resolves to %s", (preference, prefersDark, expected) => {
    expect(resolveTheme(preference, prefersDark)).toBe(expected);
  });
});

describe("isThemePreference", () => {
  it("accepts the three preferences and rejects anything else", () => {
    expect(isThemePreference("light")).toBe(true);
    expect(isThemePreference("dark")).toBe(true);
    expect(isThemePreference("system")).toBe(true);
    expect(isThemePreference("sepia")).toBe(false);
    expect(isThemePreference(null)).toBe(false);
  });
});
