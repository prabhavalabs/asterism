import { HealthStatus, PortExposure } from "@/gen/asterism/v1/asterism_pb";

/** The five semantic tones every status surface maps onto. */
export type StatusTone = "healthy" | "warning" | "critical" | "info" | "neutral";

/** Event severities emitted by the daemon. Unknown strings fall back to neutral. */
export type EventSeverity = "info" | "warning" | "critical" | "error";

export interface ToneEntry {
  label: string;
  tone: StatusTone;
}

/** The single HealthStatus mapping used across the UI. */
export const HEALTH_STATUS_STYLES: Record<HealthStatus, ToneEntry> = {
  [HealthStatus.UNSPECIFIED]: { label: "Unknown", tone: "neutral" },
  [HealthStatus.HEALTHY]: { label: "Healthy", tone: "healthy" },
  [HealthStatus.DEGRADED]: { label: "Degraded", tone: "warning" },
  [HealthStatus.DOWN]: { label: "Down", tone: "critical" },
};

export const SEVERITY_STYLES: Record<EventSeverity, ToneEntry> = {
  info: { label: "Info", tone: "info" },
  warning: { label: "Warning", tone: "warning" },
  critical: { label: "Critical", tone: "critical" },
  error: { label: "Error", tone: "critical" },
};

export function healthStyle(status: HealthStatus): ToneEntry {
  return HEALTH_STATUS_STYLES[status] ?? HEALTH_STATUS_STYLES[HealthStatus.UNSPECIFIED];
}

export function healthTone(status: HealthStatus): StatusTone {
  return healthStyle(status).tone;
}

export function severityStyle(severity: string): ToneEntry {
  const key = severity.toLowerCase();
  if (key in SEVERITY_STYLES) return SEVERITY_STYLES[key as EventSeverity];
  return { label: severity ? severity.charAt(0).toUpperCase() + severity.slice(1) : "Unknown", tone: "neutral" };
}

export function severityTone(severity: string): StatusTone {
  return severityStyle(severity).tone;
}

/** Text + 12-15% wash for pills. Full class names so Tailwind finds them. */
export const TONE_PILL_CLASSES: Record<StatusTone, string> = {
  healthy: "bg-status-healthy-surface text-status-healthy",
  warning: "bg-status-warning-surface text-status-warning",
  critical: "bg-status-critical-surface text-status-critical",
  info: "bg-status-info-surface text-status-info",
  neutral: "bg-status-neutral-surface text-status-neutral",
};

export const TONE_DOT_CLASSES: Record<StatusTone, string> = {
  healthy: "bg-status-healthy",
  warning: "bg-status-warning",
  critical: "bg-status-critical",
  info: "bg-status-info",
  neutral: "bg-status-neutral",
};

export const TONE_TEXT_CLASSES: Record<StatusTone, string> = {
  healthy: "text-status-healthy",
  warning: "text-status-warning",
  critical: "text-status-critical",
  info: "text-status-info",
  neutral: "text-status-neutral",
};

/** Port exposure as a plain string. `loopback` is a port bound to 127.0.0.1 / ::1 (private). */
export type ExposureKind = "public" | "loopback" | "internal";

export function exposureKind(exposure: PortExposure | ExposureKind): ExposureKind {
  if (typeof exposure === "string") return exposure;
  switch (exposure) {
    case PortExposure.PUBLIC:
      return "public";
    case PortExposure.LOOPBACK:
      return "loopback";
    default:
      return "internal";
  }
}

/** Status tones, data palette, role accents, or `auto` (thresholds). */
export type MeterTone =
  | StatusTone
  | "auto"
  | "primary"
  | "chart-1"
  | "chart-2"
  | "chart-3"
  | "chart-4"
  | "chart-5"
  | "role-proxy"
  | "role-app"
  | "role-db"
  | "role-cache"
  | "role-worker";

export interface MeterThresholds {
  /** Ratio at which `auto` turns amber. Default 0.75. */
  warning: number;
  /** Ratio at which `auto` turns rose. Default 0.9. */
  critical: number;
}

export const DEFAULT_METER_THRESHOLDS: MeterThresholds = { warning: 0.75, critical: 0.9 };

export function resolveMeterTone(
  tone: MeterTone,
  value: number,
  thresholds: MeterThresholds = DEFAULT_METER_THRESHOLDS,
): Exclude<MeterTone, "auto"> {
  if (tone !== "auto") return tone;
  if (value >= thresholds.critical) return "critical";
  if (value >= thresholds.warning) return "warning";
  return "chart-1";
}
