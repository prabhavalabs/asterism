/**
 * Resolves CSS colour variables (OKLCH in tokens.css) to rgba() strings a
 * canvas can use and modify. A probe element resolves var() against the
 * current theme; a 1×1 canvas converts whatever colour syntax the browser
 * returns into sRGB bytes.
 */

export interface ChartColors {
  stroke: string;
  fillTop: string;
  fillBottom: string;
  axis: string;
  grid: string;
  surface: string;
}

type Rgba = [number, number, number, number];

let scratch: CanvasRenderingContext2D | null | undefined;

function scratchContext(): CanvasRenderingContext2D | null {
  if (scratch !== undefined) return scratch;
  if (typeof document === "undefined") return (scratch = null);
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  scratch = canvas.getContext("2d", { willReadFrequently: true });
  return scratch;
}

/** Any CSS colour string → [r, g, b, a] (a in 0..1), or null when it cannot be parsed. */
export function parseColor(color: string): Rgba | null {
  const rgb = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/i.exec(color.trim());
  if (rgb) {
    const alpha = rgb[4] === undefined ? 1 : rgb[4].endsWith("%") ? parseFloat(rgb[4]) / 100 : parseFloat(rgb[4]);
    return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), alpha];
  }
  const ctx = scratchContext();
  if (!ctx) return null;
  ctx.clearRect(0, 0, 1, 1);
  ctx.fillStyle = "transparent";
  ctx.fillStyle = color;
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  if (d[3] === 0) return null;
  return [d[0], d[1], d[2], Math.round((d[3] / 255) * 1000) / 1000];
}

export function rgba([r, g, b]: Rgba, alpha: number): string {
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

/** Computed colour of `var(--name)` inside `host` (so scoped themes apply). */
export function resolveCssColor(host: Element, varName: string): string {
  const probe = document.createElement("span");
  probe.style.display = "none";
  probe.style.color = `var(${varName})`;
  host.appendChild(probe);
  const value = getComputedStyle(probe).color;
  probe.remove();
  return value;
}

export function resolveChartColors(host: Element, colorVar: string): ChartColors {
  const read = (name: string): Rgba | null => parseColor(resolveCssColor(host, name));
  // The host's own text colour is always an rgb() string, so this parses.
  const fallback: Rgba = parseColor(getComputedStyle(host).color) ?? [0, 0, 0, 0];

  const stroke = read(colorVar) ?? fallback;
  const axis = read("--muted-foreground") ?? fallback;
  const grid = read("--border") ?? axis;
  const surface = read("--card") ?? fallback;

  return {
    stroke: rgba(stroke, stroke[3]),
    fillTop: rgba(stroke, 0.24),
    fillBottom: rgba(stroke, 0),
    axis: rgba(axis, axis[3]),
    // Dotted grid at roughly 6-10% so it recedes in both themes.
    grid: rgba(grid, Math.min(1, Math.max(grid[3], 0.08))),
    surface: rgba(surface, 1),
  };
}
