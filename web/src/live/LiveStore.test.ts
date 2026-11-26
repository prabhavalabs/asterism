import { describe, expect, it } from "vitest";
import { LiveStore } from "./LiveStore";

describe("LiveStore", () => {
  it("caps ring buffer capacity and deduplicates same-second timestamps", () => {
    const store = new LiveStore(3);
    store.push("host", "cpu.util", 1000, 0.1);
    store.push("host", "cpu.util", 1500, 0.15);
    store.push("host", "cpu.util", 2000, 0.2);
    store.push("host", "cpu.util", 3000, 0.3);
    store.push("host", "cpu.util", 4000, 0.4);

    const series = store.getSeries("host", "cpu.util");
    expect(series.timestamps).toEqual([2, 3, 4]);
    expect(series.values).toEqual([0.2, 0.3, 0.4]);
    expect(store.getLatest("host", "cpu.util")).toBe(0.4);
  });

  it("merges seeded history with live points in chronological order", () => {
    const store = new LiveStore(5);
    store.seed("host", "cpu.util", [
      { tsMs: 2000, value: 0.2 },
      { tsMs: 1000, value: 0.1 },
    ]);
    store.push("host", "cpu.util", 3000, 0.3);

    const series = store.getSeries("host", "cpu.util");
    expect(series.timestamps).toEqual([1, 2, 3]);
    expect(series.values).toEqual([0.1, 0.2, 0.3]);
  });
});
