import { useState, useSyncExternalStore } from "react";
import type * as React from "react";
import {
  Activity01Icon,
  CommandLineIcon,
  ContainerIcon,
  CubeIcon,
  DatabaseIcon,
  FlashIcon,
  HardDriveIcon,
  NetworkIcon,
  WorkflowSquare01Icon,
} from "@hugeicons/core-free-icons";
import { Icon, type IconData } from "@/components/ui/icon";
import { iconSourceLabel } from "./iconSource";
import {
  ProjectIconKind,
  type ProjectIcon as ProjectIconMessage,
  type WorkloadRole,
} from "@/gen/asterism/v1/asterism_pb";
import { cn } from "@/lib/utils";
import {
  CATEGORY_COLOR,
  ICON_TONE_CLASSES,
  isIconColorToken,
  monogramFor,
  roleCategory,
  techMeta,
  type IconColorToken,
  type TechCategory,
} from "./tech";

// --- Lazy glyph table -------------------------------------------------------

let glyphs: Readonly<Record<string, string>> | null = null;
let loading: Promise<void> | null = null;
const glyphListeners = new Set<() => void>();

function loadGlyphs(): void {
  if (loading) return;
  loading = import("./techGlyphs")
    .then((m) => {
      glyphs = m.TECH_GLYPHS;
    })
    .catch(() => {
      glyphs = {};
    })
    .finally(() => {
      for (const listener of glyphListeners) listener();
    });
}

function subscribeGlyphs(listener: () => void): () => void {
  glyphListeners.add(listener);
  loadGlyphs();
  return () => glyphListeners.delete(listener);
}

/**
 * The simple-icons path for a slug: `undefined` while the glyph chunk loads,
 * `null` when the slug has no brand glyph.
 */
function useTechGlyph(slug: string | undefined): string | null | undefined {
  return useSyncExternalStore(
    subscribeGlyphs,
    () => (glyphs === null ? undefined : slug ? (glyphs[slug] ?? null) : null),
    () => undefined,
  );
}

// --- Shared tile ------------------------------------------------------------

export type IconTileSize = 16 | 20 | 24 | 32 | 40;

// Corners scale with the tile so small icons stay square-ish: 4px up to
// 24px, the 8px avatar radius from 32px.
const TILE: Record<IconTileSize, { box: string; glyph: string; text: string; letters: 1 | 2 }> = {
  16: { box: "size-4 rounded-xs", glyph: "size-3", text: "text-pill", letters: 1 },
  20: { box: "size-5 rounded-sm", glyph: "size-3", text: "text-pill tracking-[-0.04em]", letters: 2 },
  24: { box: "size-6 rounded-sm", glyph: "size-3.5", text: "text-pill tracking-[-0.02em]", letters: 2 },
  32: { box: "size-8 rounded-avatar", glyph: "size-[18px]", text: "text-control", letters: 2 },
  40: { box: "size-10 rounded-avatar", glyph: "size-[22px]", text: "text-heading", letters: 2 },
};

const CATEGORY_GLYPH: Record<TechCategory, IconData> = {
  app: CubeIcon,
  runtime: CommandLineIcon,
  monitoring: Activity01Icon,
  database: DatabaseIcon,
  queue: WorkflowSquare01Icon,
  storage: HardDriveIcon,
  cache: FlashIcon,
  proxy: NetworkIcon,
  platform: ContainerIcon,
};

// Image icons sit on a neutral tile with a little inset so transparent
// favicons keep their own colours and never touch the corners.
const IMAGE_TILE = "bg-surface-subtle ring-border";
const IMAGE_INSET: Record<IconTileSize, string> = {
  16: "p-px",
  20: "p-px",
  24: "p-0.5",
  32: "p-1",
  40: "p-1",
};

interface TileProps extends Omit<React.ComponentProps<"span">, "children" | "color"> {
  size: IconTileSize;
  /** Tint token, or "image" for the neutral image tile. */
  color: IconColorToken | "image";
  label: string;
  /** Hide from assistive tech when the name is already next to the icon. */
  decorative: boolean;
  children: React.ReactNode;
}

function Tile({ size, color, label, decorative, className, children, ...props }: TileProps) {
  return (
    <span
      data-slot="icon-tile"
      role={decorative ? undefined : "img"}
      aria-label={decorative ? undefined : label}
      aria-hidden={decorative || undefined}
      title={label}
      className={cn(
        // Menus tint descendant SVGs; the glyph always follows the tile colour.
        "inline-flex shrink-0 select-none items-center justify-center overflow-hidden ring-1 ring-inset [&_svg]:text-current!",
        TILE[size].box,
        color === "image" ? cn(IMAGE_TILE, IMAGE_INSET[size]) : ICON_TONE_CLASSES[color],
        className,
      )}
      {...props}
    >
      {children}
    </span>
  );
}

function BrandGlyph({ path, className }: { path: string; className: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" className={cn("fill-current", className)}>
      <path d={path} />
    </svg>
  );
}

// --- ProjectIcon ------------------------------------------------------------

export interface ProjectIconProps extends Omit<React.ComponentProps<"span">, "children" | "color"> {
  /** Resolved by the daemon; missing on stale data, which renders a monogram. */
  icon?: ProjectIconMessage;
  /** Project name, for the fallback monogram and the accessible label. */
  name: string;
  size?: IconTileSize;
  /** Set false when the icon stands alone (no visible project name next to it). */
  decorative?: boolean;
}

function iconTitle(icon: ProjectIconMessage | undefined, name: string): string {
  if (icon?.imageUrl && (icon.kind === ProjectIconKind.UPLOADED || icon.kind === ProjectIconKind.FAVICON)) {
    return `${name} · ${iconSourceLabel(icon).toLowerCase()}`;
  }
  if (!icon?.slug) return name;
  return `${name} · ${icon.label || icon.slug} (${iconSourceLabel(icon).toLowerCase()})`;
}

/**
 * Square project tile (16/20/24/32/40px): an uploaded or favicon image when
 * the daemon resolved one, else the technology glyph or the monogram tinted
 * with the daemon-chosen design token. A broken image falls back to the
 * glyph or monogram.
 */
export function ProjectIcon({ icon, name, size = 24, decorative = true, className, ...props }: ProjectIconProps) {
  const [failedUrl, setFailedUrl] = useState<string | undefined>(undefined);
  const imageUrl = icon?.imageUrl || undefined;
  const slug = icon?.slug || undefined;
  const glyph = useTechGlyph(slug);
  const color: IconColorToken =
    icon && isIconColorToken(icon.colorToken) ? icon.colorToken : CATEGORY_COLOR.app;
  const monogram = (icon?.monogram || monogramFor(name)).slice(0, TILE[size].letters);
  const meta = techMeta(slug);

  if (imageUrl && failedUrl !== imageUrl) {
    return (
      <Tile
        size={size}
        color="image"
        label={iconTitle(icon, name)}
        decorative={decorative}
        data-kind={icon ? ProjectIconKind[icon.kind]?.toLowerCase() : undefined}
        className={className}
        {...props}
      >
        <img
          src={imageUrl}
          alt=""
          width={size}
          height={size}
          loading="lazy"
          decoding="async"
          draggable={false}
          referrerPolicy="no-referrer"
          className="size-full object-contain"
          onError={() => setFailedUrl(imageUrl)}
        />
      </Tile>
    );
  }

  let content: React.ReactNode;
  if (slug && glyph) {
    content = <BrandGlyph path={glyph} className={TILE[size].glyph} />;
  } else if (slug && glyph === null && meta) {
    content = <Icon icon={CATEGORY_GLYPH[meta.category]} className={TILE[size].glyph} />;
  } else if (slug && glyph === undefined) {
    content = null;
  } else {
    content = <span className={cn("font-heading font-semibold leading-none", TILE[size].text)}>{monogram}</span>;
  }

  return (
    <Tile
      size={size}
      color={color}
      label={iconTitle(icon, name)}
      decorative={decorative}
      data-kind={icon ? ProjectIconKind[icon.kind]?.toLowerCase() : "monogram"}
      className={className}
      {...props}
    >
      {content}
    </Tile>
  );
}

// --- TechIcon ---------------------------------------------------------------

export interface TechIconProps extends Omit<React.ComponentProps<"span">, "children" | "color" | "role"> {
  /** `Workload.tech` / `Container.tech`; empty for custom images. */
  slug?: string;
  /** Picks the fallback glyph and colour for custom images. */
  role?: WorkloadRole;
  /** Image reference, shown in the tooltip. */
  image?: string;
  size?: IconTileSize;
  decorative?: boolean;
}

/** Square tile with the brand glyph of a container image, or a role glyph for custom builds. */
export function TechIcon({ slug, role, image, size = 24, decorative = true, className, ...props }: TechIconProps) {
  const meta = techMeta(slug);
  const glyph = useTechGlyph(meta ? slug : undefined);
  const category = meta?.category ?? roleCategory(role);
  const color = CATEGORY_COLOR[category];
  const label = meta ? `${meta.label}${image ? ` · ${image}` : ""}` : image || "Custom image";

  return (
    <Tile size={size} color={color} label={label} decorative={decorative} className={className} {...props}>
      {meta && glyph ? (
        <BrandGlyph path={glyph} className={TILE[size].glyph} />
      ) : meta && glyph === undefined ? null : (
        <Icon icon={CATEGORY_GLYPH[category]} className={TILE[size].glyph} />
      )}
    </Tile>
  );
}
