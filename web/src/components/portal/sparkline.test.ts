import { describe, expect, it } from "vitest";
import { SPARK_VIEW_H, SPARK_VIEW_W, sparklinePaths } from "./sparkline";

describe("sparklinePaths", () => {
  it("returns null with fewer than two points", () => {
    expect(sparklinePaths([], [], 60)).toBeNull();
    expect(sparklinePaths([10], [1], 60)).toBeNull();
  });

  it("spans the full width and closes the area path", () => {
    const paths = sparklinePaths([0, 1, 2], [0, 0.5, 1], 60, [0, 1]);
    expect(paths).not.toBeNull();
    expect(paths?.line.startsWith("M0.00,")).toBe(true);
    expect(paths?.line).toContain(`L${SPARK_VIEW_W.toFixed(2)},`);
    expect(paths?.area.endsWith(`L${SPARK_VIEW_W},${SPARK_VIEW_H}L0,${SPARK_VIEW_H}Z`)).toBe(true);
  });

  it("only draws points inside the window", () => {
    const ts = [0, 100, 200, 290, 300];
    const paths = sparklinePaths(ts, [1, 2, 3, 4, 5], 20);
    // 290 and 300 are inside the last 20 seconds: one move and one line command.
    expect(paths?.line.match(/[ML]/g)).toHaveLength(2);
  });

  it("clamps values to a fixed range", () => {
    const paths = sparklinePaths([0, 1], [-5, 5], 60, [0, 1]);
    expect(paths?.line).toBe(`M0.00,${(SPARK_VIEW_H - 1).toFixed(2)}L100.00,1.00`);
  });
});
