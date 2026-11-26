/**
 * Pure layout helpers for the topology canvas. Nothing here depends on
 * project or workload names: lanes, tiers and labels are derived from roles,
 * edges and ingress routes, so any host lays out the same way.
 */
import {
  EdgeKind,
  WorkloadRole,
  type IngressRoute,
  type Project,
  type TopologyEdge,
} from "@/gen/asterism/v1/asterism_pb";
import { isDataRole, roleFamily, type RoleFamily } from "./roles";

export interface Point {
  x: number;
  y: number;
}

export interface Size {
  width: number;
  height: number;
}

/**
 * Sizing contract: workload node ~288px wide (280 keeps three columns at
 * zoom 1 on a 1440px screen with the sidebar open). Heights are estimates;
 * the canvas re-lays out with measured heights after the first render.
 */
export const WORKLOAD_NODE: Size = { width: 280, height: 196 };
export const PROJECT_NODE: Size = { width: 296, height: 196 };

/**
 * Minimum gap between workload columns. Gaps that carry edge lanes and
 * labels widen to fit them (see `workloadColumnGaps`).
 */
export const WORKLOAD_COL_GAP = 128;
/** Upper bound for a widened column gap. */
export const MAX_WORKLOAD_COL_GAP = 280;
export const WORKLOAD_ROW_GAP = 32;
export const PROJECT_COL_GAP = 96;
export const PROJECT_ROW_GAP = 28;

/** Above this many nodes edges stop animating (blueprint §11.3). */
export const EDGE_ANIMATION_NODE_LIMIT = 150;

export function shouldAnimateEdges(nodeCount: number, reducedMotion: boolean): boolean {
  return !reducedMotion && nodeCount <= EDGE_ANIMATION_NODE_LIMIT;
}

/** The MiniMap only earns its space on large screens with a larger graph. */
export const MINIMAP_NODE_THRESHOLD = 8;

export function shouldShowMiniMap(nodeCount: number, isLargeScreen: boolean): boolean {
  return isLargeScreen && nodeCount > MINIMAP_NODE_THRESHOLD;
}

// ---------------------------------------------------------------------------
// Projects (overview level)
// ---------------------------------------------------------------------------

/** Where a project sits on the overview: ingress on the left, data on the right. */
export type ProjectLane = "edge" | "app" | "data";

export function projectLane(project: Pick<Project, "workloads">): ProjectLane {
  const roles = project.workloads.map((w) => w.role);
  if (roles.length === 0) return "app";
  const proxies = roles.filter((r) => r === WorkloadRole.PROXY).length;
  if (proxies > 0 && proxies * 2 >= roles.length) return "edge";
  if (roles.every(isDataRole)) return "data";
  return "app";
}

/** The role whose block represents the project on the overview. */
export function projectPrimaryRole(project: Pick<Project, "workloads">): WorkloadRole {
  const lane = projectLane(project);
  const roles = project.workloads.map((w) => w.role);
  if (lane === "edge") return WorkloadRole.PROXY;
  if (lane === "data") {
    return roles.includes(WorkloadRole.DATABASE) || roles.includes(WorkloadRole.STORAGE)
      ? WorkloadRole.DATABASE
      : WorkloadRole.CACHE;
  }
  if (roles.includes(WorkloadRole.APP)) return WorkloadRole.APP;
  if (roles.includes(WorkloadRole.WORKER)) return WorkloadRole.WORKER;
  return WorkloadRole.APP;
}

export interface RoleCount {
  family: RoleFamily;
  count: number;
}

const FAMILY_ORDER: RoleFamily[] = ["proxy", "app", "worker", "db", "cache"];

/** Workload counts per role family, in reading order (proxy → data). */
export function projectRoleCounts(project: Pick<Project, "workloads">): RoleCount[] {
  const counts = new Map<RoleFamily, number>();
  for (const w of project.workloads) {
    const family = roleFamily(w.role);
    counts.set(family, (counts.get(family) ?? 0) + 1);
  }
  return FAMILY_ORDER.filter((f) => counts.has(f)).map((family) => ({
    family,
    count: counts.get(family) ?? 0,
  }));
}

export const ROLE_FAMILY_LABEL: Record<RoleFamily, { one: string; many: string }> = {
  proxy: { one: "proxy", many: "proxies" },
  app: { one: "app", many: "apps" },
  worker: { one: "worker", many: "workers" },
  db: { one: "db", many: "dbs" },
  cache: { one: "cache", many: "caches" },
};

/**
 * Overview positions: edge projects in the first column, application
 * projects in a grid, data-only projects in the last column. Columns are
 * centred on one horizontal axis.
 */
export function layoutProjects(
  projects: ReadonlyArray<Pick<Project, "id" | "workloads">>,
  node: Size = PROJECT_NODE,
  colGap = PROJECT_COL_GAP,
  rowGap = PROJECT_ROW_GAP,
  heights?: ReadonlyMap<string, number>,
): Map<string, Point> {
  const edge = projects.filter((p) => projectLane(p) === "edge");
  const data = projects.filter((p) => projectLane(p) === "data");
  const app = projects.filter((p) => projectLane(p) === "app");

  const appCols = app.length <= 2 ? 1 : app.length <= 6 ? 2 : 3;
  const columns: string[][] = [];
  if (edge.length > 0) columns.push(edge.map((p) => p.id));
  for (let c = 0; c < appCols; c++) {
    const col = app.filter((_, i) => i % appCols === c).map((p) => p.id);
    if (col.length > 0) columns.push(col);
  }
  if (data.length > 0) columns.push(data.map((p) => p.id));

  return stackColumns(columns, node, colGap, rowGap, heights);
}

// ---------------------------------------------------------------------------
// Workloads (project drill-down)
// ---------------------------------------------------------------------------

interface RankNode {
  id: string;
  role: WorkloadRole;
  label?: string;
}

interface RankEdge {
  sourceId: string;
  targetId: string;
}

function baseRank(role: WorkloadRole): number {
  if (role === WorkloadRole.PROXY) return 0;
  if (isDataRole(role)) return 2;
  return 1;
}

/**
 * Column index per node. Starts from the role tier (proxy, compute, data)
 * and pushes every edge target at least one column right of its source, so
 * traffic reads left to right. Cycles are bounded by the node count.
 */
export function rankNodes(nodes: readonly RankNode[], edges: readonly RankEdge[]): Map<string, number> {
  const rank = new Map<string, number>();
  for (const n of nodes) rank.set(n.id, baseRank(n.role));

  const present = edges.filter((e) => rank.has(e.sourceId) && rank.has(e.targetId) && e.sourceId !== e.targetId);
  for (let iter = 0; iter < nodes.length; iter++) {
    let changed = false;
    for (const e of present) {
      const rs = rank.get(e.sourceId) ?? 0;
      const rt = rank.get(e.targetId) ?? 0;
      if (rt <= rs) {
        rank.set(e.targetId, rs + 1);
        changed = true;
      }
    }
    if (!changed) break;
  }

  // Compress to consecutive column indexes.
  const used = Array.from(new Set(rank.values())).sort((a, b) => a - b);
  const index = new Map(used.map((r, i) => [r, i]));
  for (const [id, r] of rank) rank.set(id, index.get(r) ?? 0);
  return rank;
}

/**
 * Orders each column by the mean row of its upstream neighbours (one
 * barycentre pass) to keep edges from crossing, then positions the columns.
 */
export function layoutWorkloads(
  nodes: readonly RankNode[],
  edges: readonly RankEdge[],
  node: Size = WORKLOAD_NODE,
  colGap: ColumnGap = WORKLOAD_COL_GAP,
  rowGap = WORKLOAD_ROW_GAP,
  heights?: ReadonlyMap<string, number>,
): { positions: Map<string, Point>; rank: Map<string, number> } {
  const rank = rankNodes(nodes, edges);
  const colCount = Math.max(0, ...Array.from(rank.values()).map((r) => r + 1));
  const columns: RankNode[][] = Array.from({ length: colCount }, () => []);
  for (const n of nodes) columns[rank.get(n.id) ?? 0].push(n);

  const rowOf = new Map<string, number>();
  const preds = new Map<string, string[]>();
  for (const e of edges) {
    const list = preds.get(e.targetId) ?? [];
    list.push(e.sourceId);
    preds.set(e.targetId, list);
  }

  const ordered: string[][] = columns.map((col, c) => {
    const sorted = [...col].sort((a, b) => {
      if (c > 0) {
        const ba = barycentre(preds.get(a.id), rowOf);
        const bb = barycentre(preds.get(b.id), rowOf);
        if (ba !== bb) return ba - bb;
      }
      const fa = FAMILY_ORDER.indexOf(roleFamily(a.role));
      const fb = FAMILY_ORDER.indexOf(roleFamily(b.role));
      if (fa !== fb) return fa - fb;
      return (a.label ?? a.id).localeCompare(b.label ?? b.id);
    });
    sorted.forEach((n, i) => rowOf.set(n.id, i));
    return sorted.map((n) => n.id);
  });

  return { positions: stackColumns(ordered, node, colGap, rowGap, heights), rank };
}

function barycentre(ids: string[] | undefined, rowOf: Map<string, number>): number {
  if (!ids || ids.length === 0) return Number.MAX_SAFE_INTEGER;
  const rows = ids.map((id) => rowOf.get(id)).filter((r): r is number => r !== undefined);
  if (rows.length === 0) return Number.MAX_SAFE_INTEGER;
  return rows.reduce((s, r) => s + r, 0) / rows.length;
}

/** One gap for every column boundary, or one per boundary (index c = after column c). */
export type ColumnGap = number | readonly number[];

/**
 * Places columns left to right, each centred vertically on the tallest one.
 * `heights` (measured after the first render) override the estimated node
 * height per id.
 */
export function stackColumns(
  columns: readonly (readonly string[])[],
  node: Size,
  colGap: ColumnGap,
  rowGap: number,
  heights?: ReadonlyMap<string, number>,
): Map<string, Point> {
  const out = new Map<string, Point>();
  const h = (id: string) => heights?.get(id) ?? node.height;
  const colHeight = (col: readonly string[]) =>
    col.reduce((sum, id) => sum + h(id), 0) + Math.max(0, col.length - 1) * rowGap;
  const maxHeight = Math.max(0, ...columns.map(colHeight));
  const gapAfter = (c: number) =>
    typeof colGap === "number" ? colGap : (colGap[c] ?? colGap[colGap.length - 1] ?? 0);
  let x = 0;
  columns.forEach((col, c) => {
    let y = Math.round((maxHeight - colHeight(col)) / 2);
    for (const id of col) {
      out.set(id, { x, y });
      y += h(id) + rowGap;
    }
    x += node.width + gapAfter(c);
  });
  return out;
}

// ---------------------------------------------------------------------------
// Constellation (radial) layout
// ---------------------------------------------------------------------------

/** Members on an inner ring, external context on an outer ring. */
export function layoutRadial(
  members: readonly string[],
  externals: readonly string[],
  center: Point = { x: 0, y: 0 },
  innerRadius = 220,
  outerRadius = 380,
): Map<string, Point> {
  const out = new Map<string, Point>();
  const ring = (ids: readonly string[], radius: number, phase: number) => {
    ids.forEach((id, i) => {
      if (ids.length === 1 && radius === innerRadius && externals.length === 0) {
        out.set(id, { ...center });
        return;
      }
      const angle = (i / Math.max(1, ids.length)) * Math.PI * 2 - Math.PI / 2 + phase;
      out.set(id, {
        x: Math.round(center.x + Math.cos(angle) * radius * 1.35),
        y: Math.round(center.y + Math.sin(angle) * radius * 0.8),
      });
    });
  };
  ring(members, innerRadius, 0);
  ring(externals, outerRadius, externals.length > 0 ? Math.PI / Math.max(2, externals.length) : 0);
  return out;
}

// ---------------------------------------------------------------------------
// Edge labels
// ---------------------------------------------------------------------------

export interface EdgeLabelText {
  /** Primary text: a strategy (`least_conn`) or the edge's own label (`pg :5432`). */
  title: string;
  /** Second line for load-balanced edges: `2 replicas`. */
  detail?: string;
  /** True when the proxy spreads traffic over more than one upstream. */
  loadBalanced: boolean;
}

const NON_BALANCING = new Set(["", "single", "tunnel", "none"]);

/**
 * Readable label for an edge. Ingress edges from a proxy show the routing
 * strategy and the replica count taken from the matching ingress routes.
 */
export function edgeLabelText(
  edge: Pick<TopologyEdge, "sourceId" | "targetId" | "label" | "edgeKind" | "port">,
  routes: ReadonlyArray<Pick<IngressRoute, "proxyWorkloadId" | "strategy" | "upstreams">>,
  targetReplicas = 1,
): EdgeLabelText {
  const fallback = edge.label || (edge.port > 0 ? `:${edge.port}` : "");
  if (edge.edgeKind !== EdgeKind.INGRESS) return { title: fallback, loadBalanced: false };

  const matching = routes.filter(
    (r) => r.proxyWorkloadId === edge.sourceId && r.upstreams.some((u) => u.workloadId === edge.targetId),
  );
  if (matching.length === 0) return { title: fallback, loadBalanced: false };

  const route =
    matching.find((r) => !NON_BALANCING.has(r.strategy.toLowerCase())) ?? matching[0];
  const upstreams = route.upstreams.filter((u) => u.workloadId === edge.targetId);
  const replicas = Math.max(upstreams.length, targetReplicas);
  const strategy = route.strategy.toLowerCase();

  if (NON_BALANCING.has(strategy) || replicas < 2) {
    return { title: fallback || strategy, loadBalanced: false };
  }

  return { title: route.strategy, detail: `${replicas} replicas`, loadBalanced: true };
}

export interface LabelBox {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
}

/** Rough label footprint (12px text) used only for collision checks. */
export function estimateLabelBox(id: string, center: Point, text: EdgeLabelText): LabelBox {
  const longest = Math.max(text.title.length, text.detail?.length ?? 0);
  return {
    id,
    x: center.x,
    y: center.y,
    width: Math.min(150, 20 + longest * 7),
    height: text.detail ? 42 : 24,
  };
}

/**
 * Greedy collision avoidance: each label keeps its position unless it
 * overlaps one already placed, in which case it moves down (then up) in
 * steps until it is clear. Returns the vertical offset per label id.
 */
export function placeLabels(boxes: readonly LabelBox[], gap = 6, maxSteps = 6): Map<string, number> {
  const placed: LabelBox[] = [];
  const offsets = new Map<string, number>();
  const sorted = [...boxes].sort((a, b) => a.y - b.y || a.x - b.x);
  for (const box of sorted) {
    let dy = 0;
    const step = box.height + gap;
    for (let attempt = 0; attempt <= maxSteps * 2; attempt++) {
      const candidate = { ...box, y: box.y + dy };
      if (!placed.some((p) => overlaps(p, candidate, gap))) break;
      const k = Math.floor(attempt / 2) + 1;
      dy = attempt % 2 === 0 ? k * step : -k * step;
    }
    placed.push({ ...box, y: box.y + dy });
    offsets.set(box.id, dy);
  }
  return offsets;
}

function overlaps(a: LabelBox, b: LabelBox, gap: number): boolean {
  return (
    Math.abs(a.x - b.x) * 2 < a.width + b.width + gap * 2 &&
    Math.abs(a.y - b.y) * 2 < a.height + b.height + gap * 2
  );
}

/**
 * Spreads the vertical segments of edges that share a column gap so their
 * orthogonal paths do not sit on top of each other. Returns px offsets.
 */
export function laneOffsets(
  edges: ReadonlyArray<{ id: string; gapKey: string; order: number }>,
  spacing = 10,
  max = 12,
): Map<string, number> {
  const groups = new Map<string, { id: string; order: number }[]>();
  for (const e of edges) {
    const list = groups.get(e.gapKey) ?? [];
    list.push(e);
    groups.set(e.gapKey, list);
  }
  const out = new Map<string, number>();
  for (const list of groups.values()) {
    list.sort((a, b) => a.order - b.order);
    const mid = (list.length - 1) / 2;
    list.forEach((e, i) => {
      out.set(e.id, Math.max(-max, Math.min(max, Math.round((i - mid) * spacing))));
    });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Workload edge routing (orthogonal map)
// ---------------------------------------------------------------------------

/** First vertical lane sits this far right of the source handle. */
export const LANE_START = 20;
/** Distance between parallel vertical lanes in one column gap. */
export const LANE_SPACING = 12;
/** Space between a target-end label and the target handle. */
export const LABEL_TARGET_CLEARANCE = 10;
/** Space between the last lane and the widest label in a gap. */
const LANE_LABEL_CLEARANCE = 12;
/** Vertical space between stacked target-end labels. */
const LABEL_STACK_GAP = 8;
/** Lane orders are searched exhaustively up to this many edges per gap. */
const LANE_SEARCH_LIMIT = 6;

/** 12px Geist Mono advance; labels never truncate, so estimates err wide. */
const MONO_CHAR_PX = 7.3;
const SANS_CHAR_PX = 6.8;
const LABEL_PADDING_X = 16;
const LABEL_ICON_PX = 16;

/** Text shown after a merged label, e.g. `×2`. */
export function labelCountSuffix(count: number): string {
  return count > 1 ? `×${count}` : "";
}

/** Rendered width of an edge label chip (title line, optional count, detail line). */
export function estimateLabelWidth(text: EdgeLabelText, count = 1): number {
  const suffix = labelCountSuffix(count);
  const titleChars = text.title.length + (suffix ? suffix.length + 1 : 0);
  const title = titleChars * MONO_CHAR_PX + (text.loadBalanced ? LABEL_ICON_PX : 0);
  const detail = (text.detail?.length ?? 0) * SANS_CHAR_PX;
  return Math.ceil(Math.max(title, detail) + LABEL_PADDING_X);
}

/** Rendered height of an edge label chip. */
export function estimateLabelHeight(text: EdgeLabelText): number {
  return text.detail ? 44 : 22;
}

export interface RoutableEdge {
  id: string;
  sourceId: string;
  targetId: string;
  text: EdgeLabelText;
}

/**
 * Edges that end on the same target with the same label share one trunk
 * and one label (`pg :5432 ×2`) instead of drawing identical labels on top
 * of each other.
 */
export interface LabelGroup {
  targetId: string;
  edgeIds: string[];
  text: EdgeLabelText;
  count: number;
}

export function groupTargetLabels(edges: readonly RoutableEdge[]): LabelGroup[] {
  const groups = new Map<string, LabelGroup>();
  for (const e of edges) {
    const key = `${e.targetId}\u0000${e.text.title}\u0000${e.text.detail ?? ""}`;
    const group = groups.get(key);
    if (group) {
      group.edgeIds.push(e.id);
      group.count += 1;
    } else {
      groups.set(key, { targetId: e.targetId, edgeIds: [e.id], text: e.text, count: 1 });
    }
  }
  return Array.from(groups.values());
}

function isForward(e: Pick<RoutableEdge, "sourceId" | "targetId">, rank: ReadonlyMap<string, number>): boolean {
  return (rank.get(e.targetId) ?? 0) > (rank.get(e.sourceId) ?? 0);
}

/**
 * Width of each column gap: room for one vertical lane per edge leaving the
 * column, then the widest target-end label of the next column, never below
 * `minGap`.
 */
export function workloadColumnGaps(
  edges: readonly RoutableEdge[],
  rank: ReadonlyMap<string, number>,
  minGap = WORKLOAD_COL_GAP,
  maxGap = MAX_WORKLOAD_COL_GAP,
): number[] {
  const colCount = Math.max(0, ...Array.from(rank.values()).map((r) => r + 1));
  const forward = edges.filter((e) => isForward(e, rank));
  const lanes = new Array<number>(Math.max(0, colCount - 1)).fill(0);
  const labels = new Array<number>(Math.max(0, colCount - 1)).fill(0);
  for (const e of forward) {
    const c = rank.get(e.sourceId) ?? 0;
    if (c < lanes.length) lanes[c] += 1;
  }
  for (const g of groupTargetLabels(forward)) {
    const c = (rank.get(g.targetId) ?? 0) - 1;
    if (c >= 0 && c < labels.length && g.text.title) {
      labels[c] = Math.max(labels[c], estimateLabelWidth(g.text, g.count));
    }
  }
  return lanes.map((n, c) => {
    const laneSpan = LANE_START + Math.max(0, n - 1) * LANE_SPACING;
    const needed = labels[c] > 0 ? laneSpan + LANE_LABEL_CLEARANCE + labels[c] + LABEL_TARGET_CLEARANCE : laneSpan;
    return Math.min(maxGap, Math.max(minGap, Math.ceil(needed)));
  });
}

export interface EdgeRoute {
  /** Vertical segment x, relative to the source handle. Forward edges only. */
  laneX?: number;
  /** Where the edge meets the target, relative to the target handle centre. */
  targetOffsetY: number;
  /** Only one edge per label group draws the label. */
  showLabel: boolean;
  /** Edges merged into this label (shown as `×N` when above 1). */
  labelCount: number;
}

/**
 * Routes the orthogonal edges of a workload graph:
 * - edges into one target with the same label merge into one trunk and one
 *   label; different labels get their own entry point, stacked so their
 *   target-end labels never overlap;
 * - every edge leaving a column gets its own vertical lane, ordered to
 *   minimise crossings, so parallel edges never share a segment.
 */
export function routeWorkloadEdges(
  edges: readonly RoutableEdge[],
  rank: ReadonlyMap<string, number>,
  handle: (id: string, side: "in" | "out") => Point,
  nodeHeight: (id: string) => number,
): Map<string, EdgeRoute> {
  const out = new Map<string, EdgeRoute>();
  const forward = edges.filter((e) => isForward(e, rank));
  for (const e of edges) out.set(e.id, { targetOffsetY: 0, showLabel: Boolean(e.text.title), labelCount: 1 });

  // Entry points: label groups per target, stacked in source order.
  const byTarget = new Map<string, LabelGroup[]>();
  for (const g of groupTargetLabels(forward)) {
    const list = byTarget.get(g.targetId) ?? [];
    list.push(g);
    byTarget.set(g.targetId, list);
  }
  const sourceY = new Map(forward.map((e) => [e.id, handle(e.sourceId, "out").y]));
  for (const [targetId, groups] of byTarget) {
    const meanY = (g: LabelGroup) => g.edgeIds.reduce((s, id) => s + (sourceY.get(id) ?? 0), 0) / g.edgeIds.length;
    groups.sort((a, b) => meanY(a) - meanY(b) || a.text.title.localeCompare(b.text.title));
    const heights = groups.map((g) => (g.text.title ? estimateLabelHeight(g.text) : 12));
    const total = heights.reduce((s, h) => s + h, 0) + (groups.length - 1) * LABEL_STACK_GAP;
    const limit = Math.max(0, nodeHeight(targetId) / 2 - 16);
    let y = -total / 2;
    groups.forEach((g, i) => {
      const offset = Math.max(-limit, Math.min(limit, Math.round(y + heights[i] / 2)));
      y += heights[i] + LABEL_STACK_GAP;
      g.edgeIds.forEach((id, k) => {
        out.set(id, { targetOffsetY: offset, showLabel: k === 0 && Boolean(g.text.title), labelCount: g.count });
      });
    });
  }

  // Lanes: one per edge in each source column's gap.
  const byGap = new Map<number, RoutableEdge[]>();
  for (const e of forward) {
    const c = rank.get(e.sourceId) ?? 0;
    const list = byGap.get(c) ?? [];
    list.push(e);
    byGap.set(c, list);
  }
  for (const list of byGap.values()) {
    const segs = list.map((e) => {
      const s = handle(e.sourceId, "out");
      const t = handle(e.targetId, "in");
      return { id: e.id, sx: s.x, sy: s.y, tx: t.x, ty: t.y + (out.get(e.id)?.targetOffsetY ?? 0) };
    });
    segs.sort((a, b) => a.ty - b.ty || a.sy - b.sy || a.id.localeCompare(b.id));
    const order = bestLaneOrder(segs);
    order.forEach((seg, i) => {
      const route = out.get(seg.id);
      if (route) route.laneX = LANE_START + i * LANE_SPACING;
    });
  }
  return out;
}

interface LaneSeg {
  id: string;
  sx: number;
  sy: number;
  tx: number;
  ty: number;
}

/** Tries every lane order for small gaps (first minimum wins), else keeps the given order. */
function bestLaneOrder(segs: LaneSeg[]): LaneSeg[] {
  if (segs.length < 2 || segs.length > LANE_SEARCH_LIMIT) return segs;
  let best = segs;
  let bestScore = laneCrossings(segs);
  for (const perm of permutations(segs)) {
    if (bestScore === 0) break;
    const score = laneCrossings(perm);
    if (score < bestScore) {
      best = perm;
      bestScore = score;
    }
  }
  return best;
}

function* permutations<T>(items: readonly T[]): Generator<T[]> {
  if (items.length <= 1) {
    yield [...items];
    return;
  }
  for (let i = 0; i < items.length; i++) {
    const rest = [...items.slice(0, i), ...items.slice(i + 1)];
    for (const tail of permutations(rest)) yield [items[i], ...tail];
  }
}

/** Perpendicular crossings between edges routed through lanes in this order. */
export function laneCrossings(order: readonly LaneSeg[]): number {
  type H = { y: number; x1: number; x2: number };
  type V = { x: number; y1: number; y2: number };
  const parts = order.map((s, i) => {
    const lx = s.sx + LANE_START + i * LANE_SPACING;
    const hs: H[] = [
      { y: s.sy, x1: s.sx, x2: lx },
      { y: s.ty, x1: lx, x2: s.tx },
    ];
    const vs: V[] = s.sy === s.ty ? [] : [{ x: lx, y1: Math.min(s.sy, s.ty), y2: Math.max(s.sy, s.ty) }];
    return { hs, vs };
  });
  let crossings = 0;
  for (let a = 0; a < parts.length; a++) {
    for (let b = 0; b < parts.length; b++) {
      if (a === b) continue;
      for (const v of parts[a].vs) {
        for (const h of parts[b].hs) {
          if (v.x > h.x1 && v.x < h.x2 && h.y > v.y1 && h.y < v.y2) crossings++;
        }
      }
    }
  }
  return crossings;
}
