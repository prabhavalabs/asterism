import { describe, expect, it } from "vitest";
import { parseColor, rgba } from "./chartColors";
import { autoRange, formatTimeTicks } from "./chartScales";

describe("formatTimeTicks", () => {
  const ts = new Date(2026, 0, 2, 9, 5, 7).getTime() / 1000;

  it("renders single-line HH:MM ticks for minute increments", () => {
    expect(formatTimeTicks([ts], 60)).toEqual(["09:05"]);
  });

  it("adds seconds when ticks are under a minute apart", () => {
    expect(formatTimeTicks([ts], 10)).toEqual(["09:05:07"]);
  });
});

describe("autoRange", () => {
  it("anchors non-negative data at zero with headroom", () => {
    const [lo, hi] = autoRange(10, 100);
    expect(lo).toBe(0);
    expect(hi).toBeCloseTo(115);
  });

  it("gives empty or flat-zero data a unit range", () => {
    expect(autoRange(null, null)).toEqual([0, 1]);
    expect(autoRange(0, 0)).toEqual([0, 1]);
  });

  it("pads negative ranges on both sides", () => {
    const [lo, hi] = autoRange(-10, 10);
    expect(lo).toBeLessThan(-10);
    expect(hi).toBeGreaterThan(10);
  });
});

describe("parseColor", () => {
  it("parses rgb() and rgba() computed values", () => {
    expect(parseColor("rgb(10, 20, 30)")).toEqual([10, 20, 30, 1]);
    expect(parseColor("rgba(10, 20, 30, 0.5)")).toEqual([10, 20, 30, 0.5]);
    expect(parseColor("rgb(10 20 30 / 25%)")).toEqual([10, 20, 30, 0.25]);
  });

  it("formats with a new alpha", () => {
    expect(rgba([1, 2, 3, 1], 0.2)).toBe("rgba(1, 2, 3, 0.2)");
  });
});
