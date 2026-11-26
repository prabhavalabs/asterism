import { WorkloadRole } from "@/gen/asterism/v1/asterism_pb";

interface IsometricBlockSvgProps {
  role: WorkloadRole;
  replicas?: number;
  size?: number;
  className?: string;
}

/**
 * Crisp 2.5D isometric vector blocks per infrastructure role.
 * Faces and highlights come from the --iso-* tokens so both themes stay crisp.
 */
export function IsometricBlockSvg({
  role,
  replicas = 1,
  size = 56,
  className,
}: IsometricBlockSvgProps) {
  if (role === WorkloadRole.DATABASE) {
    // Stacked isometric database cylinder
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 64 64"
        fill="none"
        className={className}
        aria-hidden="true"
      >
        {/* Bottom tier */}
        <path
          d="M12 36 v12 c0 5.5 9 10 20 10 s20-4.5 20-10 V36"
          fill="var(--iso-db-right)"
        />
        <path
          d="M12 36 v12 c0 5.5 9 10 20 10 V46 c-11 0-20-4.5-20-10z"
          fill="var(--iso-db-left)"
        />
        {/* Middle tier */}
        <path
          d="M12 24 v12 c0 5.5 9 10 20 10 s20-4.5 20-10 V24"
          fill="var(--iso-db-right)"
        />
        <path
          d="M12 24 v12 c0 5.5 9 10 20 10 V34 c-11 0-20-4.5-20-10z"
          fill="var(--iso-db-left)"
        />
        {/* Top platter */}
        <ellipse cx="32" cy="24" rx="20" ry="10" fill="var(--iso-db-top)" />
        <ellipse
          cx="32"
          cy="24"
          rx="19.2"
          ry="9.2"
          stroke="var(--iso-highlight-strong)"
          strokeWidth="1"
        />
        {/* Ring highlights */}
        <path
          d="M12 36 c0 5.5 9 10 20 10 s20-4.5 20-10"
          stroke="var(--iso-highlight)"
          strokeWidth="1"
        />
      </svg>
    );
  }

  if (role === WorkloadRole.PROXY) {
    // Faceted ingress / load-balancer gateway prism
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 64 64"
        fill="none"
        className={className}
        aria-hidden="true"
      >
        {/* Base plinth */}
        <polygon points="32,12 56,25 32,38 8,25" fill="var(--iso-proxy-top)" />
        <polygon points="8,25 32,38 32,54 8,41" fill="var(--iso-proxy-left)" />
        <polygon points="56,25 32,38 32,54 56,41" fill="var(--iso-proxy-right)" />
        {/* Load-balancer crown */}
        <polygon points="32,7 45,14 32,21 19,14" fill="var(--iso-highlight)" />
        <polyline
          points="8,25 32,38 56,25"
          stroke="var(--iso-highlight-strong)"
          strokeWidth="1.2"
        />
        <line x1="32" y1="38" x2="32" y2="54" stroke="var(--iso-highlight)" strokeWidth="1" />
        {/* Signal slits on left face */}
        <line x1="14" y1="34" x2="26" y2="41" stroke="var(--iso-highlight-strong)" strokeWidth="1.6" strokeLinecap="round" />
        <line x1="14" y1="39" x2="23" y2="44" stroke="var(--iso-highlight-strong)" strokeOpacity="0.65" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    );
  }

  if (role === WorkloadRole.CACHE || role === WorkloadRole.QUEUE) {
    // Compact faceted diamond memory / broker prism
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 64 64"
        fill="none"
        className={className}
        aria-hidden="true"
      >
        <polygon points="32,10 52,26 32,36 12,26" fill="var(--iso-cache-top)" />
        <polygon points="12,26 32,36 32,54 12,40" fill="var(--iso-cache-left)" />
        <polygon points="52,26 32,36 32,54 52,40" fill="var(--iso-cache-right)" />
        <polyline
          points="12,26 32,36 52,26"
          stroke="var(--iso-highlight-strong)"
          strokeWidth="1.2"
        />
        <polygon points="32,17 42,24 32,29 22,24" fill="var(--iso-highlight)" />
      </svg>
    );
  }

  if (role === WorkloadRole.WORKER) {
    // Chamfered worker processing cube
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 64 64"
        fill="none"
        className={className}
        aria-hidden="true"
      >
        <polygon points="32,14 54,26 32,38 10,26" fill="var(--iso-worker-top)" />
        <polygon points="10,26 32,38 32,54 10,42" fill="var(--iso-worker-left)" />
        <polygon points="54,26 32,38 32,54 54,42" fill="var(--iso-worker-right)" />
        <polyline points="10,26 32,38 54,26" stroke="var(--iso-highlight-strong)" strokeWidth="1.2" />
        <circle cx="32" cy="26" r="4.5" fill="var(--iso-highlight)" />
      </svg>
    );
  }

  // App / API block (stacks a crisp replica slab when replicas > 1)
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      className={className}
      aria-hidden="true"
    >
      {replicas > 1 && (
        <>
          <polygon points="10,35 32,47 32,56 10,44" fill="var(--iso-app-left)" opacity="0.72" />
          <polygon points="54,35 32,47 32,56 54,44" fill="var(--iso-app-right)" opacity="0.72" />
        </>
      )}
      <polygon points="32,10 54,22 32,34 10,22" fill="var(--iso-app-top)" />
      <polygon points="10,22 32,34 32,47 10,35" fill="var(--iso-app-left)" />
      <polygon points="54,22 32,34 32,47 54,35" fill="var(--iso-app-right)" />
      <polyline points="10,22 32,34 54,22" stroke="var(--iso-highlight-strong)" strokeWidth="1.2" />
      <line x1="32" y1="34" x2="32" y2="47" stroke="var(--iso-highlight)" strokeWidth="1" />
    </svg>
  );
}
