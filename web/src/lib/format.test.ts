import { describe, expect, it } from "vitest";
import { ProxyKind } from "@/gen/asterism/v1/asterism_pb";
import { TECH } from "@/components/common/tech";
import { formatBps, formatBytes, formatPct, formatUptime, proxyKindLabel, proxyKindTech } from "./format";

describe("formatters", () => {
  it("formats bytes cleanly", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024 * 1024 * 256)).toBe("256 MiB");
    expect(formatBytes(BigInt(1024 * 1024 * 1024 * 4))).toBe("4.00 GiB");
  });

  it("formats percentages, bps, and uptime", () => {
    expect(formatPct(0.145)).toBe("14.5%");
    expect(formatBps(1024 * 512)).toBe("512 KiB/s");
    expect(formatUptime(90061)).toBe("1d 1h");
    expect(formatUptime(3660)).toBe("1h 1m");
  });
});

describe("proxy kinds", () => {
  it("labels every proxy kind, including HAProxy", () => {
    expect(proxyKindLabel(ProxyKind.HAPROXY)).toBe("HAProxy");
    expect(proxyKindLabel(ProxyKind.CLOUDFLARED)).toBe("Cloudflare Tunnel");
    expect(proxyKindLabel(ProxyKind.UNSPECIFIED)).toBe("Proxy");
    expect(proxyKindLabel(99 as ProxyKind)).toBe("Proxy");
  });

  it("maps every known proxy kind to a tech glyph", () => {
    const kinds = Object.values(ProxyKind).filter((v): v is ProxyKind => typeof v === "number");
    for (const kind of kinds) {
      const slug = proxyKindTech(kind);
      if (kind === ProxyKind.UNSPECIFIED) {
        expect(slug).toBeUndefined();
      } else {
        expect(slug && TECH[slug]?.category).toBe("proxy");
      }
    }
    expect(proxyKindTech(ProxyKind.HAPROXY)).toBe("haproxy");
  });
});
