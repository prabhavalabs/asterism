import type * as React from "react";
import { Slot, Slottable } from "@radix-ui/react-slot";
import { Globe02Icon, NetworkIcon, SquareLock02Icon } from "@hugeicons/core-free-icons";
import { Icon, type IconData } from "@/components/ui/icon";
import type { PortExposure } from "@/gen/asterism/v1/asterism_pb";
import { cn } from "@/lib/utils";
import { exposureKind, type ExposureKind } from "./tones";

const EXPOSURE: Record<ExposureKind, { label: string; icon: IconData; className: string }> = {
  public: {
    label: "Public",
    icon: Globe02Icon,
    className: "bg-exposure-public-surface text-exposure-public",
  },
  loopback: {
    label: "Loopback",
    icon: SquareLock02Icon,
    className: "bg-exposure-loopback-surface text-exposure-loopback",
  },
  internal: {
    label: "Internal",
    icon: NetworkIcon,
    className: "bg-exposure-internal-surface text-exposure-internal",
  },
};

export interface PortChipProps extends Omit<React.ComponentProps<"span">, "children"> {
  /** Container or listening port. */
  port: number;
  /** Published host port. Shown as `host→port` when it differs; 0 means not published. */
  hostPort?: number;
  /** Proto enum or plain string. Defaults to internal. */
  exposure?: PortExposure | ExposureKind;
  /** `tcp` is implied; anything else is shown as a suffix (`/udp`). */
  protocol?: string;
  /** Hide the exposure icon (for very dense rows). */
  hideIcon?: boolean;
  /**
   * Render the single child element (an empty Link or button) as the chip; the
   * chip content is placed inside it. Adds hover and a 32px hit area.
   */
  asChild?: boolean;
  children?: React.ReactElement;
}

/** Port chip (22px, 12px mono). Colour and icon encode exposure. */
export function PortChip({
  port,
  hostPort,
  exposure = "internal",
  protocol,
  hideIcon = false,
  asChild = false,
  className,
  children,
  ...props
}: PortChipProps) {
  const kind = exposureKind(exposure);
  const meta = EXPOSURE[kind];
  const proto = protocol && protocol.toLowerCase() !== "tcp" ? `/${protocol.toLowerCase()}` : "";
  const text =
    hostPort && hostPort !== port ? `${hostPort}→${port}${proto}` : `:${port}${proto}`;
  const Comp = asChild ? Slot : "span";

  return (
    <Comp
      data-slot="port-chip"
      data-exposure={kind}
      title={`${meta.label} port ${hostPort || port}${proto || "/tcp"}`}
      className={cn(
        "inline-flex h-pill shrink-0 items-center gap-1 whitespace-nowrap rounded-chip px-1.5 font-mono text-pill tabular-nums",
        meta.className,
        asChild &&
          "relative outline-none transition-[filter] after:absolute after:-inset-y-[5px] after:inset-x-0 hover:brightness-110 focus-visible:ring-1 focus-visible:ring-ring",
        className,
      )}
      {...props}
    >
      {asChild && <Slottable>{children}</Slottable>}
      {!hideIcon && <Icon icon={meta.icon} className="size-3 shrink-0" />}
      <span className="sr-only">{meta.label} port </span>
      {text}
    </Comp>
  );
}
