import { describe, expect, it } from "vitest";
import { ICON_TONE_CLASSES, TECH, isIconColorToken, monogramFor, techMeta } from "./tech";
import { TECH_GLYPHS } from "./techGlyphs";

describe("monogramFor", () => {
  it.each([
    ["staging-lab", "SL"],
    ["shop", "SH"],
    ["x", "X"],
    ["--", "?"],
    ["élan-vital", "ÉV"],
  ])("%s → %s", (name, want) => {
    expect(monogramFor(name)).toBe(want);
  });
});

describe("tech tables", () => {
  it("knows labels for every glyph", () => {
    for (const slug of Object.keys(TECH_GLYPHS)) {
      expect(techMeta(slug), slug).toBeDefined();
      expect(TECH_GLYPHS[slug].length, slug).toBeGreaterThan(20);
    }
  });
  it("returns undefined for unknown slugs", () => {
    expect(techMeta("")).toBeUndefined();
    expect(techMeta("toString")).toBeUndefined();
    expect(techMeta("postgres")?.label).toBe("PostgreSQL");
  });
  it("only accepts palette tokens", () => {
    expect(isIconColorToken("role-db")).toBe(true);
    expect(isIconColorToken("#ff0000")).toBe(false);
    expect(Object.keys(ICON_TONE_CLASSES)).toHaveLength(11);
    expect(Object.keys(TECH).length).toBeGreaterThan(60);
  });
});
