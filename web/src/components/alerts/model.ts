import {
  AlertRuleKind,
  AlertSeverity,
  AlertState,
  ChannelType,
  type Alert,
  type AlertRule,
} from "@/gen/asterism/v1/alerts_pb";
import type { StatusTone } from "@/components/common";

export const SEVERITY_NAMES: Record<AlertSeverity, "info" | "warning" | "critical"> = {
  [AlertSeverity.UNSPECIFIED]: "info",
  [AlertSeverity.INFO]: "info",
  [AlertSeverity.WARNING]: "warning",
  [AlertSeverity.CRITICAL]: "critical",
};

/** Severity string for StatusPill (`severity` prop). */
export function severityName(s: AlertSeverity): "info" | "warning" | "critical" {
  return SEVERITY_NAMES[s] ?? "info";
}

export const SEVERITY_OPTIONS = [
  { value: "info", label: "Info" },
  { value: "warning", label: "Warning" },
  { value: "critical", label: "Critical" },
] as const;

export type SeverityValue = (typeof SEVERITY_OPTIONS)[number]["value"];

export function severityFromName(name: string): AlertSeverity {
  switch (name) {
    case "critical":
      return AlertSeverity.CRITICAL;
    case "warning":
      return AlertSeverity.WARNING;
    default:
      return AlertSeverity.INFO;
  }
}

export function stateLabel(s: AlertState): string {
  switch (s) {
    case AlertState.FIRING:
      return "Firing";
    case AlertState.PENDING:
      return "Pending";
    case AlertState.RESOLVED:
      return "Resolved";
    default:
      return "Inactive";
  }
}

export function stateTone(a: Alert): StatusTone {
  if (a.silencedBy) return "neutral";
  switch (a.state) {
    case AlertState.FIRING:
      return a.severity === AlertSeverity.CRITICAL ? "critical" : a.severity === AlertSeverity.WARNING ? "warning" : "info";
    case AlertState.PENDING:
      return "info";
    case AlertState.RESOLVED:
      return "healthy";
    default:
      return "neutral";
  }
}

/** "5 min", "1 h 30 min", "2 d" for rule durations in seconds. */
export function formatSeconds(seconds: number): string {
  if (seconds <= 0) return "at once";
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  if (seconds < 86400) {
    const h = Math.floor(seconds / 3600);
    const m = Math.round((seconds % 3600) / 60);
    return m ? `${h} h ${m} min` : `${h} h`;
  }
  return `${Math.round(seconds / 86400)} d`;
}

/** One-line summary of a rule's settings for the rules list. */
export function ruleSummary(r: AlertRule): string {
  const wait = r.forSeconds > 0 ? ` for ${formatSeconds(r.forSeconds)}` : "";
  switch (r.kind) {
    case AlertRuleKind.THRESHOLD: {
      const parts: string[] = [];
      if (r.warningThreshold > 0) parts.push(`warning ≥ ${r.warningThreshold}%`);
      if (r.criticalThreshold > 0) parts.push(`critical ≥ ${r.criticalThreshold}%`);
      return parts.join(", ") + wait;
    }
    case AlertRuleKind.COUNT:
      return `${severityName(r.severity)} at ${r.count} in ${formatSeconds(r.windowSeconds)}`;
    case AlertRuleKind.FORWARD:
      return `${r.eventKinds.length ? r.eventKinds.join(", ") : "every kind"} at ≥ ${severityName(r.minEventSeverity)}`;
    default:
      return `${severityName(r.severity)}${wait}`;
  }
}

export const CHANNEL_TYPE_LABELS: Record<ChannelType, string> = {
  [ChannelType.UNSPECIFIED]: "Unknown",
  [ChannelType.SLACK]: "Slack",
  [ChannelType.WEBHOOK]: "Webhook",
  [ChannelType.EMAIL]: "Email",
  [ChannelType.WEBPUSH]: "Browser push",
  [ChannelType.DISCORD]: "Discord",
  [ChannelType.TELEGRAM]: "Telegram",
};

/** Splits "a, b\nc" into trimmed, non-empty, unique values. */
export function parseList(text: string): string[] {
  const out: string[] = [];
  for (const part of text.split(/[,\n]/)) {
    const v = part.trim();
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}

/** Entity link target inside the SPA, or undefined. */
export function entityHref(entityId: string): string | undefined {
  if (entityId.startsWith("ctr:")) return `/containers/${encodeURIComponent(entityId.slice(4))}`;
  if (entityId.startsWith("proj:")) return `/projects/${encodeURIComponent(entityId.slice(5))}`;
  if (entityId === "host") return "/host";
  return undefined;
}

/** A short label for this browser ("Firefox on macOS"). */
export function deviceLabel(ua: string): string {
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const os = /Android/.test(ua)
    ? "Android"
    : /iPhone|iPad/.test(ua)
      ? "iOS"
      : /Mac OS X/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return os ? `${browser} on ${os}` : browser;
}

/** Decodes a base64url VAPID key for PushManager.subscribe. */
export function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padded = (base64 + "=".repeat((4 - (base64.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(padded);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}
