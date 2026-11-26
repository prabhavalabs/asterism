/**
 * Turns the API Graph into React Flow nodes and edges for the two map
 * levels (project overview and per-project component graph) in both view
 * modes. Pure apart from the callbacks it stores in node data.
 */
import type { Node } from "@xyflow/react";
import type { Graph, TopologyEdge, TopologyNode } from "@/gen/asterism/v1/asterism_pb";
import type { ConstellationNodeData } from "./ConstellationNode";
import type { IsometricBlockData } from "./IsometricBlockNode";
import type { ProjectOverviewNodeData } from "./ProjectOverviewNode";
import type { TrafficEdgeData, TrafficFlowEdge } from "./TrafficEdge";
import {
  PROJECT_COL_GAP,
  PROJECT_NODE,
  WORKLOAD_COL_GAP,
  WORKLOAD_NODE,
  edgeLabelText,
  estimateLabelBox,
  laneOffsets,
  layoutProjects,
  layoutRadial,
  layoutWorkloads,
  placeLabels,
  rankNodes,
  routeWorkloadEdges,
  workloadColumnGaps,
  type LabelBox,
  type Point,
  type RoutableEdge,
} from "./layout";

export type PortalViewMode = "isometric" | "constellation";

export interface BuiltGraph {
  nodes: Node[];
  edges: TrafficFlowEdge[];
}

/** Hostnames that reach a project: routes its proxies serve or that target its workloads. */
export function domainsByProject(graph: Graph): Map<string, string[]> {
  const wlToProj = workloadProjectMap(graph);
  const out = new Map<string, Set<string>>();
  const add = (pid: string | undefined, hosts: readonly string[]) => {
    if (!pid) return;
    const set = out.get(pid) ?? new Set<string>();
    for (const h of hosts) set.add(h);
    out.set(pid, set);
  };
  for (const r of graph.ingressRoutes) {
    add(wlToProj.get(r.proxyWorkloadId), r.hostnames);
    for (const u of r.upstreams) add(wlToProj.get(u.workloadId), r.hostnames);
  }
  return new Map(Array.from(out, ([k, v]) => [k, Array.from(v).sort()]));
}

export function workloadProjectMap(graph: Graph): Map<string, string> {
  const map = new Map<string, string>();
  for (const p of graph.projects) for (const w of p.workloads) map.set(w.id, p.id);
  for (const n of graph.nodes) if (!map.has(n.id)) map.set(n.id, n.projectId);
  return map;
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

export function buildOverviewGraph(
  graph: Graph,
  onExpandProject: (projectId: string) => void,
  animated: boolean,
  /** Measured node heights; replace the estimate once nodes have rendered. */
  heights?: ReadonlyMap<string, number>,
): BuiltGraph {
  const positions = layoutProjects(graph.projects, PROJECT_NODE, undefined, undefined, heights);
  const domains = domainsByProject(graph);

  const nodes: Node[] = graph.projects.map((p) => ({
    id: p.id,
    type: "projectOverview",
    position: positions.get(p.id) ?? { x: 0, y: 0 },
    data: {
      project: p,
      domains: domains.get(p.id) ?? [],
      onExpandProject,
    } satisfies ProjectOverviewNodeData,
  }));

  const wlToProj = workloadProjectMap(graph);
  const aggregated = new Map<string, { edge: TopologyEdge; src: string; dst: string; conns: number; bps: number }>();
  for (const e of graph.edges) {
    const src = wlToProj.get(e.sourceId);
    const dst = wlToProj.get(e.targetId);
    if (!src || !dst || src === dst) continue;
    const key = `${src}->${dst}:${e.edgeKind}`;
    const agg = aggregated.get(key);
    if (agg) {
      agg.conns += e.connCount;
      agg.bps += e.bytesPerSec;
    } else {
      aggregated.set(key, { edge: e, src, dst, conns: e.connCount, bps: e.bytesPerSec });
    }
  }

  const items = Array.from(aggregated.entries());
  const lanes = laneOffsets(
    items.map(([key, a]) => ({
      id: key,
      gapKey: `${Math.round(positions.get(a.src)?.x ?? 0)}`,
      order: positions.get(a.dst)?.y ?? 0,
    })),
  );

  const edges: TrafficFlowEdge[] = items.map(([key, a]) => ({
    id: `proj-edge:${key}`,
    source: a.src,
    target: a.dst,
    type: "trafficEdge",
    data: {
      showLabel: false,
      edgeKind: a.edge.edgeKind,
      source: a.edge.source,
      connCount: a.conns,
      bytesPerSec: a.bps,
      animated,
      pathStyle: "orthogonal",
      laneOffset: lanes.get(key) ?? 0,
      columnGap: PROJECT_COL_GAP,
    } satisfies TrafficEdgeData,
  }));

  return { nodes, edges };
}

/** All workloads as stars, clustered by project on a grid. */
export function buildOverviewConstellation(
  graph: Graph,
  selectedId: string | undefined,
  onInspect: (id: string) => void,
  animated: boolean,
): BuiltGraph {
  const byProject = new Map<string, TopologyNode[]>();
  for (const n of graph.nodes) {
    const list = byProject.get(n.projectId) ?? [];
    list.push(n);
    byProject.set(n.projectId, list);
  }
  const projectIds = graph.projects.map((p) => p.id).filter((id) => byProject.has(id));
  const cols = Math.max(1, Math.ceil(Math.sqrt(projectIds.length)));
  const cellW = 500;
  const cellH = 300;

  const nodes: Node[] = [];
  projectIds.forEach((pid, i) => {
    const members = (byProject.get(pid) ?? []).map((n) => n.id);
    const center = { x: (i % cols) * cellW, y: Math.floor(i / cols) * cellH };
    const radius = members.length <= 1 ? 0 : members.length <= 3 ? 90 : 115;
    const pos = layoutRadial(members, [], center, radius, radius);
    for (const n of byProject.get(pid) ?? []) {
      nodes.push(starNode(n, pos.get(n.id) ?? center, selectedId, false, onInspect));
    }
  });

  const edges = graph.edges.map((e) => straightEdge(e, animated));
  return { nodes, edges };
}

// ---------------------------------------------------------------------------
// Project drill-down
// ---------------------------------------------------------------------------

interface ProjectSubgraph {
  members: TopologyNode[];
  externals: TopologyNode[];
  edges: TopologyEdge[];
}

/** Project workloads plus the nodes outside the project they talk to. */
export function projectSubgraph(graph: Graph, projectId: string): ProjectSubgraph {
  const nodeMap = new Map(graph.nodes.map((n) => [n.id, n]));
  const memberIds = new Set(graph.nodes.filter((n) => n.projectId === projectId).map((n) => n.id));
  const externalIds = new Set<string>();
  const edges = graph.edges.filter((e) => {
    const srcIn = memberIds.has(e.sourceId);
    const dstIn = memberIds.has(e.targetId);
    if (srcIn && dstIn) return true;
    if (srcIn && nodeMap.has(e.targetId)) {
      externalIds.add(e.targetId);
      return true;
    }
    if (dstIn && nodeMap.has(e.sourceId)) {
      externalIds.add(e.sourceId);
      return true;
    }
    return false;
  });
  const pick = (ids: Set<string>) =>
    Array.from(ids)
      .map((id) => nodeMap.get(id))
      .filter((n): n is TopologyNode => Boolean(n));
  return { members: pick(memberIds), externals: pick(externalIds), edges };
}

export function buildProjectGraph(
  graph: Graph,
  projectId: string,
  viewMode: PortalViewMode,
  selectedId: string | undefined,
  onInspect: (id: string) => void,
  animated: boolean,
  /** Measured node heights; replace the estimate once nodes have rendered. */
  heights?: ReadonlyMap<string, number>,
): BuiltGraph {
  const { members, externals, edges } = projectSubgraph(graph, projectId);
  const externalIds = new Set(externals.map((n) => n.id));
  const projectName = new Map(graph.projects.map((p) => [p.id, p.name]));
  const projectIcon = new Map(graph.projects.map((p) => [p.id, p.icon]));

  if (viewMode === "constellation") {
    const pos = layoutRadial(
      members.map((n) => n.id),
      externals.map((n) => n.id),
      { x: 0, y: 0 },
    );
    const nodes = [...externals, ...members].map((n) =>
      starNode(n, pos.get(n.id) ?? { x: 0, y: 0 }, selectedId, externalIds.has(n.id), onInspect),
    );
    return { nodes, edges: edges.map((e) => straightEdge(e, animated)) };
  }

  const all = [...externals, ...members];
  const rankInput = all.map((n) => ({ id: n.id, role: n.role, label: n.label }));
  const replicas = new Map(all.map((n) => [n.id, n.replicas]));
  const routable: RoutableEdge[] = edges.map((e) => ({
    id: e.id,
    sourceId: e.sourceId,
    targetId: e.targetId,
    text: edgeLabelText(e, graph.ingressRoutes, replicas.get(e.targetId) ?? 1),
  }));
  const gaps = workloadColumnGaps(routable, rankNodes(rankInput, edges));
  const { positions, rank } = layoutWorkloads(rankInput, edges, WORKLOAD_NODE, gaps, undefined, heights);

  const nodes: Node[] = all.map((n) => ({
    id: n.id,
    type: "isometricBlock",
    position: positions.get(n.id) ?? { x: 0, y: 0 },
    data: {
      node: n,
      selectedId,
      isExternalContext: externalIds.has(n.id),
      externalProjectName: externalIds.has(n.id) ? projectName.get(n.projectId) : undefined,
      externalProjectIcon: externalIds.has(n.id) ? projectIcon.get(n.projectId) : undefined,
      onInspect,
    } satisfies IsometricBlockData,
  }));

  // Estimated handle positions drive lane ordering and label stacking.
  const nodeHeight = (id: string) => heights?.get(id) ?? WORKLOAD_NODE.height;
  const handle = (id: string, side: "in" | "out"): Point => {
    const p = positions.get(id) ?? { x: 0, y: 0 };
    return { x: side === "out" ? p.x + WORKLOAD_NODE.width : p.x, y: p.y + nodeHeight(id) / 2 };
  };
  const routes = routeWorkloadEdges(routable, rank, handle, nodeHeight);

  // Backward edges (cycles) keep a mid-path label with collision avoidance.
  const boxes: LabelBox[] = [];
  for (const e of routable) {
    if (routes.get(e.id)?.laneX !== undefined || !e.text.title) continue;
    const s = handle(e.sourceId, "out");
    const t = handle(e.targetId, "in");
    boxes.push(estimateLabelBox(e.id, { x: (s.x + t.x) / 2, y: (s.y + t.y) / 2 }, e.text));
  }
  const labelOffsets = placeLabels(boxes);

  const showLabels = edges.length <= 24;
  const flowEdges: TrafficFlowEdge[] = edges.map((e, i) => {
    const text = routable[i].text;
    const route = routes.get(e.id);
    return {
      id: e.id,
      source: e.sourceId,
      target: e.targetId,
      type: "trafficEdge",
      data: {
        label: text.title,
        labelCount: route?.labelCount ?? 1,
        detail: text.detail,
        loadBalanced: text.loadBalanced,
        showLabel: showLabels && (route?.showLabel ?? true),
        edgeKind: e.edgeKind,
        source: e.source,
        connCount: e.connCount,
        bytesPerSec: e.bytesPerSec,
        animated,
        pathStyle: "orthogonal",
        laneX: route?.laneX,
        targetOffsetY: route?.targetOffsetY ?? 0,
        labelAnchor: route?.laneX !== undefined ? "target" : "center",
        labelOffsetY: labelOffsets.get(e.id) ?? 0,
        columnGap: gaps[rank.get(e.sourceId) ?? 0] ?? WORKLOAD_COL_GAP,
      } satisfies TrafficEdgeData,
    };
  });

  return { nodes, edges: flowEdges };
}

function starNode(
  n: TopologyNode,
  position: Point,
  selectedId: string | undefined,
  isExternalContext: boolean,
  onInspect: (id: string) => void,
): Node {
  return {
    id: n.id,
    type: "constellationStar",
    // Stars are 192px wide; centre them on the layout point.
    position: { x: position.x - 96, y: position.y - 30 },
    data: { node: n, selectedId, isExternalContext, onInspect } satisfies ConstellationNodeData,
  };
}

function straightEdge(e: TopologyEdge, animated: boolean): TrafficFlowEdge {
  return {
    id: e.id,
    source: e.sourceId,
    target: e.targetId,
    type: "trafficEdge",
    data: {
      label: e.label,
      showLabel: false,
      edgeKind: e.edgeKind,
      source: e.source,
      connCount: e.connCount,
      bytesPerSec: e.bytesPerSec,
      animated,
      pathStyle: "straight",
    } satisfies TrafficEdgeData,
  };
}

