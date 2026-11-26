import { describe, expect, it } from "vitest";
import { ariaSortFor, formatOrder, parseOrder, toggleOrder } from "./order";

describe("order_by helpers", () => {
  it("reads the first term and its direction", () => {
    expect(parseOrder("memory desc")).toEqual({ field: "memory", desc: true });
    expect(parseOrder("project, name")).toEqual({ field: "project", desc: false });
    expect(parseOrder("  cpu   DESC , name")).toEqual({ field: "cpu", desc: true });
    expect(parseOrder("")).toEqual({ field: "", desc: false });
  });

  it("formats a term", () => {
    expect(formatOrder({ field: "size", desc: true })).toBe("size desc");
    expect(formatOrder({ field: "name", desc: false })).toBe("name");
  });

  it("flips the active field and starts others in their natural direction", () => {
    expect(toggleOrder("name", "name")).toBe("name desc");
    expect(toggleOrder("name desc", "name")).toBe("name");
    expect(toggleOrder("name", "cpu", true)).toBe("cpu desc");
    expect(toggleOrder("project, name", "project")).toBe("project desc");
    expect(toggleOrder("", "port")).toBe("port");
  });

  it("maps to aria-sort", () => {
    expect(ariaSortFor("cpu desc", "cpu")).toBe("descending");
    expect(ariaSortFor("cpu", "cpu")).toBe("ascending");
    expect(ariaSortFor("cpu", "name")).toBe("none");
  });
});
