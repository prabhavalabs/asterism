import { useEffect, useLayoutEffect, useRef } from "react";
import uPlot from "uplot";
import { liveStore } from "@/live/LiveStore";
import { resolveChartColors, type ChartColors } from "./chartColors";
import { autoRange, formatTimeTicks } from "./chartScales";

interface UPlotChartProps {
  entityId: string;
  metric: string;
  /** CSS variable for the series colour, e.g. `--chart-1`. */
  colorVar?: string;
  /** Plot height in px (sizing contract: main 240, grid 180, inspector 160). */
  height?: number;
  /** Charts sharing a key share a crosshair. */
  syncKey?: string;
  /** Y-axis tick formatter. Safe to pass inline: it is read through a ref. */
  valueFormatter?: (v: number) => string;
  /** Fixed y range (e.g. [0, 1] for utilisation). Safe to pass inline. */
  yRange?: [number, number];
  className?: string;
  /** Accessible name of the chart (defaults to the metric). */
  ariaLabel?: string;
}

const syncPool = new Map<string, uPlot.SyncPubSub>();

function getSync(key?: string): uPlot.SyncPubSub | undefined {
  if (!key) return undefined;
  let pub = syncPool.get(key);
  if (!pub) {
    pub = uPlot.sync(key);
    syncPool.set(key, pub);
  }
  return pub;
}

function appFont(): string {
  if (typeof document === "undefined") return "sans-serif";
  return getComputedStyle(document.body).fontFamily || "sans-serif";
}

/**
 * Live time-series chart. The uPlot instance is created once per
 * entity/metric and then only receives setData/setSize/redraw: formatter and
 * range props are read through refs, colours are resolved from CSS variables
 * and re-resolved when the theme changes.
 */
export function UPlotChart({
  entityId,
  metric,
  colorVar = "--chart-1",
  height = 240,
  syncKey,
  valueFormatter,
  yRange,
  className,
  ariaLabel,
}: UPlotChartProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const plotRef = useRef<uPlot | null>(null);
  const formatterRef = useRef(valueFormatter);
  const yRangeRef = useRef(yRange);
  const colorVarRef = useRef(colorVar);
  const colorsRef = useRef<ChartColors | null>(null);
  const heightRef = useRef(height);

  useLayoutEffect(() => {
    formatterRef.current = valueFormatter;
  }, [valueFormatter]);

  // Create the instance (once per data source).
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    colorsRef.current = resolveChartColors(el, colorVarRef.current);
    const colors = () => colorsRef.current ?? resolveChartColors(el, colorVarRef.current);
    const font = `12px ${appFont()}`;

    const buildData = (): uPlot.AlignedData => {
      const s = liveStore.getSeries(entityId, metric);
      if (s.timestamps.length === 0) {
        const now = Math.floor(Date.now() / 1000);
        return [[now - 60, now], [null, null]];
      }
      return [s.timestamps, s.values];
    };

    const sync = getSync(syncKey);

    const opts: uPlot.Options = {
      width: Math.max(el.clientWidth, 160),
      height: heightRef.current,
      // Right padding leaves room for the last centred time label.
      padding: [12, 28, 0, 4],
      legend: { show: false },
      cursor: {
        drag: { x: false, y: false },
        sync: sync ? { key: sync.key } : undefined,
        points: {
          size: 7,
          width: 2,
          fill: () => colors().stroke,
          stroke: () => colors().surface,
        },
      },
      scales: {
        x: { time: true },
        y: {
          range: (_u, min, max) => yRangeRef.current ?? autoRange(min, max),
        },
      },
      axes: [
        {
          stroke: () => colors().axis,
          grid: { stroke: () => colors().grid, width: 1, dash: [2, 4] },
          ticks: { show: false },
          font,
          size: 32,
          gap: 8,
          // Minimum px between time ticks: room for `HH:MM:SS` plus a gap.
          space: 84,
          values: (_u, splits, _ai, _space, incr) => formatTimeTicks(splits, incr),
        },
        {
          stroke: () => colors().axis,
          grid: { stroke: () => colors().grid, width: 1, dash: [2, 4] },
          ticks: { show: false },
          font,
          gap: 8,
          size: (u, values) => {
            if (!values || values.length === 0) return 48;
            u.ctx.font = font;
            const widest = Math.max(...values.map((v) => u.ctx.measureText(v).width));
            return Math.ceil(widest) + 20;
          },
          values: (_u, splits) =>
            splits.map((v) => {
              const fmt = formatterRef.current;
              return fmt ? fmt(v) : Number.isInteger(v) ? String(v) : v.toFixed(1);
            }),
        },
      ],
      series: [
        {},
        {
          stroke: () => colors().stroke,
          width: 1.5,
          fill: (u) => {
            const c = colors();
            const top = u.bbox.top;
            const bottom = u.bbox.top + u.bbox.height;
            const grad = u.ctx.createLinearGradient(0, top, 0, bottom);
            grad.addColorStop(0, c.fillTop);
            grad.addColorStop(1, c.fillBottom);
            return grad;
          },
          points: { show: false },
          spanGaps: false,
        },
      ],
    };

    const plot = new uPlot(opts, buildData(), el);
    plotRef.current = plot;

    const unsub = liveStore.subscribeSeries(entityId, metric, () => {
      plotRef.current?.setData(buildData(), true);
    });

    const ro = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width;
      if (width && width > 40 && plotRef.current) {
        const w = Math.floor(width);
        if (w !== plotRef.current.width) plotRef.current.setSize({ width: w, height: heightRef.current });
      }
    });
    ro.observe(el);

    // Theme switches toggle the `dark` class (or data-theme) on <html>.
    const recolor = () => {
      colorsRef.current = resolveChartColors(el, colorVarRef.current);
      plotRef.current?.redraw(false, true);
    };
    const mo = new MutationObserver(recolor);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "data-theme", "style"] });

    return () => {
      mo.disconnect();
      ro.disconnect();
      unsub();
      plot.destroy();
      plotRef.current = null;
    };
  }, [entityId, metric, syncKey]);

  // Colour prop change: re-resolve and redraw.
  useEffect(() => {
    if (colorVarRef.current === colorVar) return;
    colorVarRef.current = colorVar;
    const el = containerRef.current;
    if (!el || !plotRef.current) return;
    colorsRef.current = resolveChartColors(el, colorVar);
    plotRef.current.redraw(false, true);
  }, [colorVar]);

  // Height prop change: resize in place.
  useEffect(() => {
    if (heightRef.current === height) return;
    heightRef.current = height;
    const plot = plotRef.current;
    if (plot) plot.setSize({ width: plot.width, height });
  }, [height]);

  // Range prop change (compared by value, so inline arrays are fine): rescale.
  const rangeKey = yRange ? `${yRange[0]}:${yRange[1]}` : "";
  useEffect(() => {
    const [lo, hi] = rangeKey ? rangeKey.split(":").map(Number) : [];
    const next: [number, number] | undefined = rangeKey ? [lo, hi] : undefined;
    const prev = yRangeRef.current;
    yRangeRef.current = next;
    const plot = plotRef.current;
    if (plot && (prev?.[0] !== next?.[0] || prev?.[1] !== next?.[1])) {
      plot.setData(plot.data, true);
    }
  }, [rangeKey]);

  return (
    <div
      ref={containerRef}
      role="img"
      aria-label={ariaLabel ?? `${metric} over time`}
      className={className}
      style={{ width: "100%", height }}
    />
  );
}
