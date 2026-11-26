import { describe, expect, it } from "vitest";
import { categoryLabel, projectNameFromId } from "./labels";

describe("categoryLabel", () => {
  it.each([
    ["production", "Production"],
    ["INFRA", "Infra"],
    ["edge-gateway", "Edge gateway"],
    ["  ", "Uncategorised"],
  ])("%s → %s", (input, expected) => {
    expect(categoryLabel(input)).toBe(expected);
  });
});

describe("projectNameFromId", () => {
  it("strips the proj: prefix", () => {
    expect(projectNameFromId("proj:analytics-hub")).toBe("analytics-hub");
    expect(projectNameFromId("analytics-hub")).toBe("analytics-hub");
    expect(projectNameFromId("")).toBe("");
  });
});
