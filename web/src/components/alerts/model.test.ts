import { describe, expect, it } from "vitest";
import { AlertRule, AlertRuleKind, AlertSeverity } from "@/gen/asterism/v1/alerts_pb";
import { create } from "@bufbuild/protobuf";
import { AlertRuleSchema } from "@/gen/asterism/v1/alerts_pb";
import { deviceLabel, entityHref, formatSeconds, parseList, ruleSummary, urlBase64ToUint8Array } from "./model";

describe("alerts model", () => {
  it("formats durations", () => {
    expect(formatSeconds(0)).toBe("at once");
    expect(formatSeconds(45)).toBe("45 s");
    expect(formatSeconds(300)).toBe("5 min");
    expect(formatSeconds(5400)).toBe("1 h 30 min");
    expect(formatSeconds(172800)).toBe("2 d");
  });

  it("summarises rules", () => {
    const r: AlertRule = create(AlertRuleSchema, { kind: AlertRuleKind.THRESHOLD, warningThreshold: 85, criticalThreshold: 95, forSeconds: 300 });
    expect(ruleSummary(r)).toBe("warning ≥ 85%, critical ≥ 95% for 5 min");
    const c = create(AlertRuleSchema, { kind: AlertRuleKind.COUNT, count: 3, windowSeconds: 600, severity: AlertSeverity.WARNING });
    expect(ruleSummary(c)).toBe("warning at 3 in 10 min");
  });

  it("parses lists and links entities", () => {
    expect(parseList("a, b\n a ,, c")).toEqual(["a", "b", "c"]);
    expect(entityHref("ctr:shop-api-1")).toBe("/containers/shop-api-1");
    expect(entityHref("agent:x")).toBeUndefined();
  });

  it("labels devices and decodes keys", () => {
    expect(deviceLabel("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) Gecko/20100101 Firefox/132.0")).toBe("Firefox on macOS");
    expect(Array.from(urlBase64ToUint8Array("AQID_-8"))).toEqual([1, 2, 3, 255, 239]);
  });
});
