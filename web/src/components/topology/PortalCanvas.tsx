import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  ReactFlow,
  useEdgesState,
  useNodesInitialized,
  useNodesState,
  useReactFlow,
  useStore,
  type EdgeTypes,
  type FitViewOptions,
  type Node,
  type NodeTypes,
} from "@xyflow/react";
import { ArrowExpand01Icon, ArrowLeft02Icon, ArrowShrink01Icon, ColorsIcon, CubeIcon, LeftToRightListBulletIcon, RotateLeft01Icon, SparklesIcon, UnfoldLessIcon, UnfoldMoreIcon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import type { Graph, Project } from "@/gen/asterism/v1/asterism_pb";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ChipScroller, SegmentedControl, Toolbar, ToolbarGroup, ToolbarSpacer } from "@/components/common";
import { useIsMobile } from "@/hooks/use-mobile";
import { cn } from "@/lib/utils";
import {
  buildOverviewConstellation,
  buildOverviewGraph,
  buildProjectGraph,
  type PortalViewMode,
} from "./buildGraph";
import { ConstellationNode } from "./ConstellationNode";
import { IsometricBlockNode } from "./IsometricBlockNode";
import { shouldAnimateEdges, shouldShowMiniMap } from "./layout";
import { MapLegend } from "./MapLegend";
import { ProjectOverviewNode } from "./ProjectOverviewNode";
import { TopologyListView } from "./TopologyListView";
import { TrafficEdge, type TrafficFlowEdge } from "./TrafficEdge";
import { useIsLargeScreen, usePrefersReducedMotion } from "./useMediaQuery";

export type { PortalViewMode } from "./buildGraph";

interface PortalCanvasProps {
  graph: Graph;
  allProjects: Project[];
  focusedProjectId: string | null;
  onFocusProject: (projectId: string | null) => void;
  viewMode: PortalViewMode;
  onViewModeChange: (mode: PortalViewMode) => void;
  category: string;
  onCategoryChange?: (category: string) => void;
  selectedWorkloadId?: string;
  onSelectWorkload: (id: string) => void;
  lockToProject?: boolean;
  /** Canvas height on md and up (the canvas is never shorter than 560px there). */
  heightClassName?: string;
  /** Overrides the toolbar title. */
  title?: string;
  /** Show the role and edge legend under the canvas initially (default true). */
  defaultLegendOpen?: boolean;
  className?: string;
}

const nodeTypes: NodeTypes = {
  projectOverview: ProjectOverviewNode,
  isometricBlock: IsometricBlockNode,
  constellationStar: ConstellationNode,
};

const edgeTypes: EdgeTypes = {
  trafficEdge: TrafficEdge,
};

/** Text stays readable: never fit below 0.75, users may zoom out to 0.6. */
const FIT_MIN_ZOOM = 0.75;
const FIT_MAX_ZOOM = 1;
const FIT_PADDING = 24;
const fitViewOptions: FitViewOptions = { padding: 0.08, minZoom: FIT_MIN_ZOOM, maxZoom: FIT_MAX_ZOOM };

const CATEGORY_ORDER = ["infra", "production", "staging"];

function capitalise(value: string): string {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : value;
}

type MobileDisplay = "list" | "map";
type MobileMode = "list" | PortalViewMode;

/**
 * Toolbar rows by container width. Wide: one row (title, filter, spacer,
 * controls). Narrow: title row, a horizontally scrolling filter row, then a
 * controls row with view toggles left and icon actions right. Map-style
 * labels appear only once the whole row fits with them.
 */
const TOOLBAR_WITH_FILTER = {
  title: "@min-[60rem]:basis-auto",
  filter:
    "[mask-image:linear-gradient(to_right,black_calc(100%-24px),transparent)] @min-[60rem]:basis-auto @min-[60rem]:shrink-0 @min-[60rem]:[mask-image:none]",
  filterViewport: "pr-6 @min-[60rem]:pr-0",
  spacer: "@min-[60rem]:block",
  controls: "@min-[60rem]:basis-auto @min-[60rem]:justify-end",
  viewLabel: "@min-[68rem]:inline",
} as const;

const TOOLBAR_PLAIN = {
  title: "@min-[40rem]:basis-auto @min-[40rem]:flex-1",
  filter: "",
  filterViewport: "",
  spacer: "",
  controls: "@min-[40rem]:basis-auto @min-[40rem]:justify-end",
  viewLabel: "@min-[48rem]:inline",
} as const;

export function PortalCanvas({
  graph,
  allProjects,
  focusedProjectId,
  onFocusProject,
  viewMode,
  onViewModeChange,
  category,
  onCategoryChange,
  selectedWorkloadId,
  onSelectWorkload,
  lockToProject = false,
  heightClassName = "h-[560px]",
  title,
  defaultLegendOpen = true,
  className,
}: PortalCanvasProps) {
  const isMobile = useIsMobile();
  const isLarge = useIsLargeScreen();
  const reducedMotion = usePrefersReducedMotion();

  const [isFullscreen, setIsFullscreen] = useState(false);
  const [isMinimized, setIsMinimized] = useState(false);
  const [legendOpen, setLegendOpen] = useState(defaultLegendOpen);
  const [layoutVersion, setLayoutVersion] = useState(0);
  const [mobileDisplay, setMobileDisplay] = useState<MobileDisplay>("list");

  const showList = isMobile && mobileDisplay === "list" && !isFullscreen;

  useEffect(() => {
    if (!isFullscreen) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") setIsFullscreen(false);
    };
    window.addEventListener("keydown", onKeyDown);
    const { overflow } = document.body.style;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      document.body.style.overflow = overflow;
    };
  }, [isFullscreen]);

  const categoryOptions = useMemo(() => {
    const counts = new Map<string, number>();
    for (const p of allProjects) if (p.category) counts.set(p.category, (counts.get(p.category) ?? 0) + 1);
    const cats = Array.from(counts.keys()).sort((a, b) => {
      const ia = CATEGORY_ORDER.indexOf(a);
      const ib = CATEGORY_ORDER.indexOf(b);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a.localeCompare(b);
    });
    if (category !== "all" && !counts.has(category)) cats.push(category);
    return [
      { value: "all", label: "All", count: allProjects.length },
      ...cats.map((c) => ({ value: c, label: capitalise(c), count: counts.get(c) ?? 0 })),
    ];
  }, [allProjects, category]);

  // Node heights vary with content; once rendered, the tallest measured node
  // replaces the estimate so rows never overlap.
  const [measured, setMeasured] = useState<{ key: string; heights: Map<string, number>; signature: string } | null>(
    null,
  );
  const layoutKey = `${focusedProjectId ?? "vps"}-${viewMode}`;
  const measuredHeights = measured?.key === layoutKey ? measured.heights : undefined;
  const measuredSignature = measured?.key === layoutKey ? measured.signature : "";

  const nodeCount = graph.nodes.length;
  const animated = shouldAnimateEdges(nodeCount, reducedMotion);

  const computed = useMemo(() => {
    if (focusedProjectId) {
      return buildProjectGraph(
        graph,
        focusedProjectId,
        viewMode,
        selectedWorkloadId,
        onSelectWorkload,
        animated,
        measuredHeights,
      );
    }
    if (viewMode === "constellation") {
      return buildOverviewConstellation(graph, selectedWorkloadId, onSelectWorkload, animated);
    }
    return buildOverviewGraph(graph, onFocusProject, animated, measuredHeights);
    // layoutVersion forces a fresh layout when the user resets dragged nodes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph, focusedProjectId, viewMode, selectedWorkloadId, onSelectWorkload, onFocusProject, animated, measuredHeights, layoutVersion]);

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>(computed.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState<TrafficFlowEdge>(computed.edges);

  const isOverview = !focusedProjectId;
  const flowKey = `${focusedProjectId ?? "vps"}-${viewMode}-${layoutVersion}`;
  const appliedKey = useRef(`${flowKey}:${measuredSignature}`);
  const [layoutRevision, setLayoutRevision] = useState(0);

  useEffect(() => {
    const key = `${flowKey}:${measuredSignature}`;
    const sameLayout = appliedKey.current === key;
    appliedKey.current = key;
    if (!sameLayout) setLayoutRevision((r) => r + 1);
    // Within one layout (selection change, data refresh) keep positions the
    // user dragged; a new level, view mode or reset takes the fresh layout.
    setNodes((prev) => {
      if (!sameLayout) return computed.nodes;
      const prevPos = new Map(prev.map((n) => [n.id, n.position]));
      return computed.nodes.map((n) => ({ ...n, position: prevPos.get(n.id) ?? n.position }));
    });
    setEdges(computed.edges);
  }, [computed, flowKey, measuredSignature, setNodes, setEdges]);

  const showCategory = isOverview && Boolean(onCategoryChange) && !lockToProject && categoryOptions.length > 2;
  const rows = showCategory ? TOOLBAR_WITH_FILTER : TOOLBAR_PLAIN;

  const focusedProject = allProjects.find((p) => p.id === focusedProjectId);
  const heading =
    title ??
    (focusedProjectId
      ? (focusedProject?.name ?? focusedProjectId)
      : lockToProject
        ? "Component architecture"
        : "VPS architecture map");
  const countLabel = isOverview && viewMode === "isometric"
    ? `${nodes.length} ${nodes.length === 1 ? "project" : "projects"}`
    : `${nodes.length} ${nodes.length === 1 ? "service" : "services"}`;

  const showMiniMap = shouldShowMiniMap(nodes.length, isLarge);

  const sizeClass = isFullscreen
    ? "fixed inset-0 z-50 h-dvh rounded-none"
    : isMinimized
      ? "h-auto"
      : showList
        ? "h-auto"
        : isMobile
          ? "h-auto"
          : cn(heightClassName, "min-h-[560px]");

  return (
    <section
      aria-label={heading}
      data-slot="portal-canvas"
      className={cn("@container relative flex w-full flex-col overflow-hidden rounded-card bg-card shadow-card", sizeClass, className)}
    >
      <Toolbar className="z-10 shrink-0 gap-y-2 border-b border-divider bg-surface-subtle px-3 py-2 @min-[30rem]:px-4">
        <ToolbarGroup className={cn("min-w-0 basis-full flex-nowrap", rows.title)}>
          {focusedProjectId && !lockToProject && (
            <Button variant="outline" onClick={() => onFocusProject(null)} aria-label="Back to all projects">
              <Icon icon={ArrowLeft02Icon} />
              <span className="hidden sm:inline">All projects</span>
            </Button>
          )}
          <h2 className="min-w-0 truncate font-heading text-heading text-foreground">{heading}</h2>
          <Badge variant="secondary" mono className="shrink-0">
            {countLabel}
          </Badge>
        </ToolbarGroup>

        {showCategory && (
          <ChipScroller
            data-slot="category-filter"
            className={cn("basis-full", rows.filter)}
            viewportClassName={rows.filterViewport}
          >
            <SegmentedControl
              aria-label="Project category"
              value={category}
              onValueChange={(value) => onCategoryChange?.(value)}
              options={categoryOptions}
              className="max-w-none [&>button]:shrink-0"
            />
          </ChipScroller>
        )}

        <ToolbarSpacer className={cn("hidden", rows.spacer)} />

        <ToolbarGroup className={cn("basis-full flex-nowrap justify-between", rows.controls)}>
          <div className="flex items-center gap-2">
            {isMobile && !isFullscreen ? (
              <SegmentedControl<MobileMode>
                aria-label="Display"
                value={showList ? "list" : viewMode}
                onValueChange={(mode) => {
                  if (mode === "list") {
                    setMobileDisplay("list");
                    return;
                  }
                  setMobileDisplay("map");
                  onViewModeChange(mode);
                }}
                options={[
                  { value: "list", label: "List", icon: LeftToRightListBulletIcon },
                  { value: "isometric", label: null, icon: CubeIcon, ariaLabel: "Isometric map" },
                  { value: "constellation", label: null, icon: SparklesIcon, ariaLabel: "Constellation map" },
                ]}
              />
            ) : (
              <SegmentedControl<PortalViewMode>
                aria-label="Map style"
                value={viewMode}
                onValueChange={onViewModeChange}
                options={[
                  {
                    value: "isometric",
                    label: <span className={cn("hidden", rows.viewLabel)}>Isometric</span>,
                    icon: CubeIcon,
                    ariaLabel: "Isometric",
                  },
                  {
                    value: "constellation",
                    label: <span className={cn("hidden", rows.viewLabel)}>Constellation</span>,
                    icon: SparklesIcon,
                    ariaLabel: "Constellation",
                  },
                ]}
              />
            )}
          </div>
          <div className="flex items-center gap-2">
            {!showList && (
              <IconAction
                label={legendOpen ? "Hide legend" : "Show legend"}
                onClick={() => setLegendOpen((v) => !v)}
                pressed={legendOpen}
              >
                <Icon icon={ColorsIcon} />
              </IconAction>
            )}
            {!showList && (
              <IconAction label="Reset layout" onClick={() => setLayoutVersion((v) => v + 1)}>
                <Icon icon={RotateLeft01Icon} />
              </IconAction>
            )}
            {!isFullscreen && (
              <IconAction
                label={isMinimized ? "Expand map" : "Collapse map"}
                onClick={() => setIsMinimized((v) => !v)}
              >
                {isMinimized ? <Icon icon={UnfoldMoreIcon} /> : <Icon icon={UnfoldLessIcon} />}
              </IconAction>
            )}
            <IconAction
              label={isFullscreen ? "Exit fullscreen (Esc)" : "Fullscreen"}
              onClick={() => {
                setIsMinimized(false);
                setIsFullscreen((v) => !v);
              }}
            >
              {isFullscreen ? <Icon icon={ArrowShrink01Icon} /> : <Icon icon={ArrowExpand01Icon} />}
            </IconAction>
          </div>
        </ToolbarGroup>
      </Toolbar>

      {!isMinimized &&
        (showList ? (
          <TopologyListView
            graph={graph}
            projectId={focusedProjectId}
            selectedWorkloadId={selectedWorkloadId}
            onSelectWorkload={onSelectWorkload}
            onOpenProject={lockToProject || focusedProjectId ? undefined : (id) => onFocusProject(id)}
            className="bg-surface-subtle"
          />
        ) : (
          <>
            <div
              className={cn(
                "relative bg-background",
                isMobile && !isFullscreen ? "h-[480px] flex-none" : "min-h-0 flex-1",
              )}
            >
              <ReactFlow
                key={flowKey}
                nodes={nodes}
                edges={edges}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                nodeTypes={nodeTypes}
                edgeTypes={edgeTypes}
                nodesDraggable
                nodesConnectable={false}
                fitViewOptions={fitViewOptions}
                minZoom={0.6}
                maxZoom={1.75}
                proOptions={{ hideAttribution: true }}
                aria-label={`${heading} diagram`}
              >
                {viewMode === "isometric" && (
                  <HeightMeasurer
                    layoutKey={layoutKey}
                    current={measuredHeights}
                    onMeasured={(heights, signature) => setMeasured({ key: layoutKey, heights, signature })}
                  />
                )}
                <ViewportFitter fitKey={`${flowKey}:${layoutRevision}`} animate={!reducedMotion} />
                <Background variant={BackgroundVariant.Dots} gap={20} size={1.2} color="var(--iso-grid-dot)" />
                <Controls showInteractive={false} fitViewOptions={fitViewOptions} orientation="horizontal" />
                {showMiniMap && (
                  <MiniMap
                    pannable
                    zoomable
                    nodeStrokeWidth={2}
                    nodeColor="var(--border-strong)"
                    maskColor="var(--minimap-mask)"
                    style={{ width: 168, height: 108 }}
                  />
                )}
              </ReactFlow>
            </div>
            {legendOpen && <MapLegend compact={isOverview && viewMode === "isometric"} className="shrink-0" />}
          </>
        ))}
    </section>
  );
}

function IconAction({
  label,
  onClick,
  pressed,
  children,
}: {
  label: string;
  onClick: () => void;
  pressed?: boolean;
  children: ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="outline"
          size="icon"
          aria-label={label}
          aria-pressed={pressed}
          onClick={onClick}
          className={cn(pressed && "bg-secondary text-foreground")}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

/**
 * Fits the graph on load, on layout changes and when the canvas is resized
 * (fullscreen), but never below FIT_MIN_ZOOM: wider graphs start anchored at
 * the top-left (ingress side) and the user pans to the rest.
 */
function ViewportFitter({ fitKey, animate }: { fitKey: string; animate: boolean }) {
  const rf = useReactFlow();
  const initialized = useNodesInitialized();
  const width = useStore((s) => s.width);
  const height = useStore((s) => s.height);
  const lastKey = useRef<string | null>(null);

  useEffect(() => {
    if (!initialized || width === 0 || height === 0) return;
    const key = `${fitKey}:${Math.round(width)}x${Math.round(height)}`;
    if (lastKey.current === key) return;

    // Wait a frame so freshly laid-out positions have reached the store.
    const frame = requestAnimationFrame(() => {
      const isFirst = lastKey.current === null;
      lastKey.current = key;
      const nodes = rf.getNodes();
      if (nodes.length === 0) return;
      const bounds = rf.getNodesBounds(nodes);
      const availW = Math.max(1, width - FIT_PADDING * 2);
      const availH = Math.max(1, height - FIT_PADDING * 2);
      const zoom = Math.min(
        FIT_MAX_ZOOM,
        Math.max(FIT_MIN_ZOOM, Math.min(availW / bounds.width, availH / bounds.height)),
      );
      const contentW = bounds.width * zoom;
      const contentH = bounds.height * zoom;
      const x = contentW <= availW ? (width - contentW) / 2 - bounds.x * zoom : FIT_PADDING - bounds.x * zoom;
      const y = contentH <= availH ? (height - contentH) / 2 - bounds.y * zoom : FIT_PADDING - bounds.y * zoom;
      void rf.setViewport({ x, y, zoom }, { duration: animate && !isFirst ? 200 : 0 });
    });
    return () => cancelAnimationFrame(frame);
  }, [initialized, width, height, fitKey, rf, animate]);

  return null;
}

/** Reports rendered node heights once per layout when they differ from the ones in use. */
function HeightMeasurer({
  layoutKey,
  current,
  onMeasured,
}: {
  layoutKey: string;
  current: ReadonlyMap<string, number> | undefined;
  onMeasured: (heights: Map<string, number>, signature: string) => void;
}) {
  const initialized = useNodesInitialized();
  // A primitive signature keeps the selector stable between renders.
  const signature = useStore((s) => {
    const parts: string[] = [];
    for (const [id, n] of s.nodeLookup) parts.push(`${id}=${Math.ceil(n.measured.height ?? 0)}`);
    return parts.sort().join("|");
  });

  useEffect(() => {
    if (!initialized || !signature) return;
    const heights = new Map<string, number>();
    for (const part of signature.split("|")) {
      const at = part.lastIndexOf("=");
      heights.set(part.slice(0, at), Number(part.slice(at + 1)));
    }
    if ([...heights.values()].some((h) => h === 0)) return;
    const changed =
      !current ||
      [...heights].some(([id, h]) => Math.abs((current.get(id) ?? 0) - h) >= 4);
    if (changed) onMeasured(heights, signature);
  }, [initialized, signature, current, layoutKey, onMeasured]);

  return null;
}
