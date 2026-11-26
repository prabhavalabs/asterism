import { ProxyKind, WorkloadRole } from "@/gen/asterism/v1/asterism_pb";
import { proxyKindLabel, proxyKindTech } from "@/lib/format";
import { cn } from "@/lib/utils";
import { TechIcon } from "./ProjectIcon";

export interface ProxyKindLabelProps {
  kind: ProxyKind;
  className?: string;
}

/** Proxy kind as a 16px brand glyph plus its label (Nginx, Caddy, Traefik, HAProxy, Cloudflare Tunnel). */
export function ProxyKindLabel({ kind, className }: ProxyKindLabelProps) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      <TechIcon slug={proxyKindTech(kind)} role={WorkloadRole.PROXY} size={16} />
      <span className="truncate">{proxyKindLabel(kind)}</span>
    </span>
  );
}
