import { describe, expect, it } from "vitest";
import { HealthStatus, PortExposure } from "@/gen/asterism/v1/asterism_pb";
import { exposureKind, healthStyle, resolveMeterTone, severityStyle } from "./tones";

describe("healthStyle", () => {
  it.each([
    [HealthStatus.HEALTHY, "healthy", "Healthy"],
    [HealthStatus.DEGRADED, "warning", "Degraded"],
    [HealthStatus.DOWN, "critical", "Down"],
    [HealthStatus.UNSPECIFIED, "neutral", "Unknown"],
  ])("maps %s to %s", (status, tone, label) => {
    expect(healthStyle(status)).toEqual({ tone, label });
  });
});

describe("severityStyle", () => {
  it.each([
    ["info", "info"],
    ["warning", "warning"],
    ["critical", "critical"],
    ["error", "critical"],
    ["WARNING", "warning"],
    ["debug", "neutral"],
  ])("maps %s to %s", (severity, tone) => {
    expect(severityStyle(severity).tone).toBe(tone);
  });
});

describe("exposureKind", () => {
  it.each([
    [PortExposure.PUBLIC, "public"],
    [PortExposure.LOOPBACK, "loopback"],
    [PortExposure.INTERNAL, "internal"],
    [PortExposure.UNSPECIFIED, "internal"],
    ["public", "public"],
  ] as const)("maps %s to %s", (exposure, kind) => {
    expect(exposureKind(exposure)).toBe(kind);
  });
});

describe("resolveMeterTone", () => {
  it.each([
    [0.2, "chart-1"],
    [0.75, "warning"],
    [0.95, "critical"],
  ] as const)("auto at %s is %s", (value, tone) => {
    expect(resolveMeterTone("auto", value)).toBe(tone);
  });

  it("keeps an explicit tone", () => {
    expect(resolveMeterTone("role-db", 0.99)).toBe("role-db");
  });
});
