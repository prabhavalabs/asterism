import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  EdgeKind,
  EdgeSource,
  GraphSchema,
  IngressRouteSchema,
  ProjectSchema,
  TopologyEdgeSchema,
  TopologyNodeSchema,
  WorkloadRole,
  WorkloadSchema,
} from "@/gen/asterism/v1/asterism_pb";
import { buildProjectGraph, domainsByProject, projectSubgraph } from "./buildGraph";
import {
  LABEL_TARGET_CLEARANCE,
  LANE_SPACING,
  LANE_START,
  WORKLOAD_COL_GAP,
  WORKLOAD_NODE,
  edgeLabelText,
  estimateLabelHeight,
  estimateLabelWidth,
  groupTargetLabels,
  laneCrossings,
  routeWorkloadEdges,
  workloadColumnGaps,
  type EdgeLabelText,
  type Point,
  type RoutableEdge,
  laneOffsets,
  layoutProjects,
  layoutWorkloads,
  placeLabels,
  projectLane,
  projectPrimaryRole,
  projectRoleCounts,
  rankNodes,
  shouldAnimateEdges,
  shouldShowMiniMap,
  stackColumns,
} from "./layout";

const project = (id: string, roles: WorkloadRole[]) =>
  create(ProjectSchema, {
    id,
    name: id,
    workloads: roles.map((role, i) => create(WorkloadSchema, { id: `${id}/w${i}`, role })),
  });

describe("projectLane", () => {
  it("derives lanes from workload roles, not names", () => {
    expect(projectLane(project("anything", [WorkloadRole.PROXY, WorkloadRole.PROXY, WorkloadRole.APP]))).toBe("edge");
    expect(projectLane(project("edge-ingress", [WorkloadRole.APP, WorkloadRole.DATABASE]))).toBe("app");
    expect(projectLane(project("x", [WorkloadRole.DATABASE, WorkloadRole.CACHE]))).toBe("data");
    expect(projectLane(project("empty", []))).toBe("app");
  });

  it("picks a representative role", () => {
    expect(projectPrimaryRole(project("a", [WorkloadRole.PROXY]))).toBe(WorkloadRole.PROXY);
    expect(projectPrimaryRole(project("b", [WorkloadRole.CACHE, WorkloadRole.QUEUE]))).toBe(WorkloadRole.CACHE);
    expect(projectPrimaryRole(project("c", [WorkloadRole.WORKER, WorkloadRole.DATABASE]))).toBe(WorkloadRole.WORKER);
  });

  it("counts roles in reading order", () => {
    const counts = projectRoleCounts(
      project("p", [WorkloadRole.DATABASE, WorkloadRole.APP, WorkloadRole.APP, WorkloadRole.QUEUE]),
    );
    expect(counts).toEqual([
      { family: "app", count: 2 },
      { family: "db", count: 1 },
      { family: "cache", count: 1 },
    ]);
  });
});

describe("layoutProjects", () => {
  it("puts edge projects left and data projects right", () => {
    const pos = layoutProjects([
      project("data", [WorkloadRole.DATABASE]),
      project("app", [WorkloadRole.APP]),
      project("edge", [WorkloadRole.PROXY]),
    ]);
    expect(pos.get("edge")!.x).toBeLessThan(pos.get("app")!.x);
    expect(pos.get("app")!.x).toBeLessThan(pos.get("data")!.x);
  });
});

describe("rankNodes", () => {
  const nodes = [
    { id: "proxy", role: WorkloadRole.PROXY },
    { id: "api", role: WorkloadRole.APP },
    { id: "worker", role: WorkloadRole.WORKER },
    { id: "db", role: WorkloadRole.DATABASE },
  ];

  it("keeps api and worker in one compute column", () => {
    const rank = rankNodes(nodes, [
      { sourceId: "proxy", targetId: "api" },
      { sourceId: "api", targetId: "db" },
      { sourceId: "worker", targetId: "db" },
    ]);
    expect([rank.get("proxy"), rank.get("api"), rank.get("worker"), rank.get("db")]).toEqual([0, 1, 1, 2]);
  });

  it("pushes call targets right and compresses empty columns", () => {
    const rank = rankNodes(nodes, [
      { sourceId: "api", targetId: "worker" },
      { sourceId: "worker", targetId: "db" },
    ]);
    expect(rank.get("worker")).toBe(2);
    expect(rank.get("db")).toBe(3);
  });

  it("terminates on cycles", () => {
    const rank = rankNodes(
      [
        { id: "a", role: WorkloadRole.APP },
        { id: "b", role: WorkloadRole.APP },
      ],
      [
        { sourceId: "a", targetId: "b" },
        { sourceId: "b", targetId: "a" },
      ],
    );
    expect(rank.size).toBe(2);
  });
});

describe("stackColumns / layoutWorkloads", () => {
  it("centres shorter columns on the tallest", () => {
    const pos = stackColumns([["a"], ["b", "c"]], { width: 100, height: 50 }, 20, 10);
    expect(pos.get("a")).toEqual({ x: 0, y: 30 });
    expect(pos.get("b")).toEqual({ x: 120, y: 0 });
    expect(pos.get("c")).toEqual({ x: 120, y: 60 });
  });

  it("accepts a different gap per column boundary", () => {
    const pos = stackColumns([["a"], ["b"], ["c"]], { width: 100, height: 50 }, [20, 60], 10);
    expect(pos.get("b")!.x).toBe(120);
    expect(pos.get("c")!.x).toBe(280);
  });

  it("spaces workload columns by node width plus the label gap", () => {
    const { positions } = layoutWorkloads(
      [
        { id: "p", role: WorkloadRole.PROXY },
        { id: "a", role: WorkloadRole.APP },
      ],
      [{ sourceId: "p", targetId: "a" }],
    );
    expect(positions.get("a")!.x - positions.get("p")!.x).toBe(WORKLOAD_NODE.width + WORKLOAD_COL_GAP);
  });
});

describe("edgeLabelText", () => {
  const routes = [
    create(IngressRouteSchema, {
      proxyWorkloadId: "proxy",
      strategy: "least_conn",
      upstreams: [
        { workloadId: "api", weight: 3 },
        { workloadId: "api", weight: 2 },
      ],
    }),
    create(IngressRouteSchema, {
      proxyWorkloadId: "proxy",
      strategy: "single",
      upstreams: [{ workloadId: "web", weight: 1 }],
    }),
  ];

  it("shows strategy and replica count for balanced upstreams", () => {
    const edge = create(TopologyEdgeSchema, {
      sourceId: "proxy",
      targetId: "api",
      edgeKind: EdgeKind.INGRESS,
      label: "least_conn (2×)",
    });
    expect(edgeLabelText(edge, routes)).toEqual({
      title: "least_conn",
      detail: "2 replicas",
      loadBalanced: true,
    });
  });

  it("keeps the edge label for single upstreams and non-ingress edges", () => {
    const single = create(TopologyEdgeSchema, {
      sourceId: "proxy",
      targetId: "web",
      edgeKind: EdgeKind.INGRESS,
      label: "HTTPS :3000",
    });
    expect(edgeLabelText(single, routes)).toEqual({ title: "HTTPS :3000", loadBalanced: false });
    const db = create(TopologyEdgeSchema, { edgeKind: EdgeKind.DATABASE, port: 5432 });
    expect(edgeLabelText(db, routes)).toEqual({ title: ":5432", loadBalanced: false });
  });
});

describe("placeLabels", () => {
  it("moves overlapping labels apart and leaves clear ones alone", () => {
    const offsets = placeLabels([
      { id: "a", x: 100, y: 100, width: 80, height: 24 },
      { id: "b", x: 100, y: 104, width: 80, height: 24 },
      { id: "c", x: 400, y: 100, width: 80, height: 24 },
    ]);
    expect(offsets.get("a")).toBe(0);
    expect(Math.abs(offsets.get("b") ?? 0)).toBeGreaterThanOrEqual(24);
    expect(offsets.get("c")).toBe(0);
  });
});

describe("laneOffsets", () => {
  it("spreads edges sharing a gap symmetrically and clamps", () => {
    const offsets = laneOffsets([
      { id: "a", gapKey: "0", order: 1 },
      { id: "b", gapKey: "0", order: 2 },
      { id: "c", gapKey: "1", order: 1 },
    ]);
    expect(offsets.get("a")).toBe(-5);
    expect(offsets.get("b")).toBe(5);
    expect(offsets.get("c")).toBe(0);
  });
});

describe("thresholds", () => {
  it("disables edge animation for large graphs and reduced motion", () => {
    expect(shouldAnimateEdges(20, false)).toBe(true);
    expect(shouldAnimateEdges(151, false)).toBe(false);
    expect(shouldAnimateEdges(20, true)).toBe(false);
  });

  it("shows the minimap only on large screens with more than 8 nodes", () => {
    expect(shouldShowMiniMap(9, true)).toBe(true);
    expect(shouldShowMiniMap(8, true)).toBe(false);
    expect(shouldShowMiniMap(20, false)).toBe(false);
  });
});

describe("buildProjectGraph", () => {
  const graph = create(GraphSchema, {
    projects: [
      project("edge", [WorkloadRole.PROXY]),
      create(ProjectSchema, {
        id: "core",
        name: "core",
        workloads: [
          create(WorkloadSchema, { id: "api", role: WorkloadRole.APP }),
          create(WorkloadSchema, { id: "pg", role: WorkloadRole.DATABASE }),
        ],
      }),
    ],
    nodes: [
      create(TopologyNodeSchema, { id: "edge/w0", projectId: "edge", role: WorkloadRole.PROXY, label: "proxy" }),
      create(TopologyNodeSchema, { id: "api", projectId: "core", role: WorkloadRole.APP, label: "api", replicas: 2 }),
      create(TopologyNodeSchema, { id: "pg", projectId: "core", role: WorkloadRole.DATABASE, label: "pg" }),
    ],
    edges: [
      create(TopologyEdgeSchema, {
        id: "e1",
        sourceId: "edge/w0",
        targetId: "api",
        edgeKind: EdgeKind.INGRESS,
        source: EdgeSource.PROXY,
      }),
      create(TopologyEdgeSchema, {
        id: "e2",
        sourceId: "api",
        targetId: "pg",
        edgeKind: EdgeKind.DATABASE,
        source: EdgeSource.OBSERVED,
        label: "pg :5432",
      }),
    ],
    ingressRoutes: [
      create(IngressRouteSchema, {
        proxyWorkloadId: "edge/w0",
        hostnames: ["api.example.test"],
        strategy: "round_robin",
        upstreams: [{ workloadId: "api" }, { workloadId: "api" }],
      }),
    ],
  });

  it("includes external neighbours as shared context", () => {
    const sub = projectSubgraph(graph, "core");
    expect(sub.members.map((n) => n.id).sort()).toEqual(["api", "pg"]);
    expect(sub.externals.map((n) => n.id)).toEqual(["edge/w0"]);
  });

  it("lays out ingress → app → database left to right with LB labels", () => {
    const built = buildProjectGraph(graph, "core", "isometric", undefined, () => {}, true);
    const x = (id: string) => built.nodes.find((n) => n.id === id)!.position.x;
    expect(x("edge/w0")).toBeLessThan(x("api"));
    expect(x("api")).toBeLessThan(x("pg"));
    const lb = built.edges.find((e) => e.id === "e1")!;
    expect(lb.data).toMatchObject({ label: "round_robin", detail: "2 replicas", loadBalanced: true });
  });

  it("anchors labels at the target end with a lane per edge", () => {
    const built = buildProjectGraph(graph, "core", "isometric", undefined, () => {}, true);
    const db = built.edges.find((e) => e.id === "e2")!;
    expect(db.data).toMatchObject({ label: "pg :5432", labelAnchor: "target", showLabel: true, labelCount: 1 });
    expect(db.data?.laneX).toBe(LANE_START);
  });

  it("maps domains to the proxy project and the upstream project", () => {
    const domains = domainsByProject(graph);
    expect(domains.get("edge")).toEqual(["api.example.test"]);
    expect(domains.get("core")).toEqual(["api.example.test"]);
  });
});

describe("edge label routing", () => {
  const text = (title: string): EdgeLabelText => ({ title, loadBalanced: false });
  // Two compute nodes feeding a database and a cache, as in a typical project.
  const edges: RoutableEdge[] = [
    { id: "api-pg", sourceId: "api", targetId: "pg", text: text("pg :5432") },
    { id: "api-redis", sourceId: "api", targetId: "redis", text: text("valkey :6379") },
    { id: "worker-pg", sourceId: "worker", targetId: "pg", text: text("pg :5432") },
    { id: "worker-redis", sourceId: "worker", targetId: "redis", text: text("queue :6379") },
  ];
  const rank = new Map([
    ["api", 0],
    ["worker", 0],
    ["pg", 1],
    ["redis", 1],
  ]);
  const gaps = workloadColumnGaps(edges, rank);
  const x1 = WORKLOAD_NODE.width + gaps[0];
  const centres: Record<string, Point> = {
    api: { x: 0, y: 100 },
    worker: { x: 0, y: 330 },
    pg: { x: x1, y: 100 },
    redis: { x: x1, y: 330 },
  };
  const handle = (id: string, side: "in" | "out"): Point => ({
    x: side === "out" ? centres[id].x + WORKLOAD_NODE.width : centres[id].x,
    y: centres[id].y,
  });
  const routes = routeWorkloadEdges(edges, rank, handle, () => WORKLOAD_NODE.height);

  it("merges identical labels into one per target and keeps different ones apart", () => {
    const groups = groupTargetLabels(edges);
    expect(groups.map((g) => [g.targetId, g.text.title, g.count])).toEqual([
      ["pg", "pg :5432", 2],
      ["redis", "valkey :6379", 1],
      ["redis", "queue :6379", 1],
    ]);
    const pg = [routes.get("api-pg")!, routes.get("worker-pg")!];
    expect(pg.filter((r) => r.showLabel)).toHaveLength(1);
    expect(pg.every((r) => r.labelCount === 2)).toBe(true);
    // A merged trunk shares one entry point.
    expect(pg[0].targetOffsetY).toBe(pg[1].targetOffsetY);
  });

  it("stacks different labels into one target without overlap, in source order", () => {
    const valkey = routes.get("api-redis")!;
    const queue = routes.get("worker-redis")!;
    expect(valkey.showLabel && queue.showLabel).toBe(true);
    expect(valkey.targetOffsetY).toBeLessThan(queue.targetOffsetY);
    expect(queue.targetOffsetY - valkey.targetOffsetY).toBeGreaterThanOrEqual(
      estimateLabelHeight(text("valkey :6379")),
    );
  });

  it("gives every edge in a gap its own lane, clear of the target-end labels", () => {
    const lanes = edges.map((e) => routes.get(e.id)!.laneX!);
    expect(new Set(lanes).size).toBe(edges.length);
    expect(Math.min(...lanes)).toBe(LANE_START);
    const widest = Math.max(...edges.map((e) => estimateLabelWidth(e.text, e.targetId === "pg" ? 2 : 1)));
    const lastLane = WORKLOAD_NODE.width + Math.max(...lanes);
    expect(lastLane).toBeLessThan(x1 - LABEL_TARGET_CLEARANCE - widest);
  });

  it("sizes column gaps for lanes and full label text", () => {
    expect(gaps).toHaveLength(1);
    expect(gaps[0]).toBeGreaterThanOrEqual(
      LANE_START + 3 * LANE_SPACING + estimateLabelWidth(text("valkey :6379")) + LABEL_TARGET_CLEARANCE,
    );
    // A gap without edges keeps the minimum.
    expect(workloadColumnGaps([], new Map([["a", 0], ["b", 1]]))).toEqual([WORKLOAD_COL_GAP]);
  });

  it("counts merged edges in the label width", () => {
    expect(estimateLabelWidth(text("pg :5432"), 2)).toBeGreaterThan(estimateLabelWidth(text("pg :5432")));
    expect(estimateLabelWidth(text("CF Tunnel → Caddy 443"))).toBeGreaterThan(150);
  });

  it("orders lanes to avoid crossings", () => {
    const near = { id: "near", sx: 0, sy: 0, tx: 300, ty: 100 };
    const far = { id: "far", sx: 0, sy: 0, tx: 300, ty: 200 };
    expect(laneCrossings([near, far])).toBe(1);
    expect(laneCrossings([far, near])).toBe(0);

    const fanRank = new Map([["s", 0], ["a", 1], ["b", 1]]);
    const fanPos: Record<string, Point> = { s: { x: 0, y: 0 }, a: { x: 300, y: 100 }, b: { x: 300, y: 200 } };
    const fan = routeWorkloadEdges(
      [
        { id: "near", sourceId: "s", targetId: "a", text: text("") },
        { id: "far", sourceId: "s", targetId: "b", text: text("") },
      ],
      fanRank,
      (id) => fanPos[id],
      () => WORKLOAD_NODE.height,
    );
    expect(fan.get("far")!.laneX).toBeLessThan(fan.get("near")!.laneX!);
  });
});
