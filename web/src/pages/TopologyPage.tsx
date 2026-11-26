import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ErrorState, LoadingState, Page, projectNameFromId } from "@/components/common";
import { Card } from "@/components/ui/card";
import { PortalCanvas, type PortalViewMode } from "@/components/topology/PortalCanvas";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { inventoryClient, topologyClient } from "@/lib/rpc";
import type { TopologySearch } from "@/lib/routeSearch";

/** Below the 48px topbar with the page's 16px padding above and below. */
const CANVAS_HEIGHT = "h-[calc(100dvh-var(--topbar-h)-2rem)]";

/**
 * The architecture map on its own, full width and as tall as the window.
 * The map style (`?view=constellation`) and the focused project
 * (`?project=<name>`) live in the URL, so a link opens the same view and
 * back/forward step between levels. Shares the overview and graph queries
 * with the dashboard, so switching between them costs no extra request.
 */
export function TopologyPage() {
  const search = useSearch({ strict: false }) as TopologySearch;
  const navigate = useNavigate();
  const [category, setCategory] = useState<string>("all");
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);

  const viewMode: PortalViewMode = search.view ?? "isometric";
  const focusedProjectId = search.project ? `proj:${search.project}` : null;

  const overviewQuery = useQuery({
    queryKey: ["overview"],
    queryFn: () => inventoryClient.getOverview({}),
    refetchInterval: 15_000,
  });
  const graphQuery = useQuery({
    queryKey: ["graph", category],
    queryFn: () => topologyClient.getGraph({ category: category === "all" ? "" : category }),
    refetchInterval: 15_000,
  });

  const setSearch = useCallback(
    (next: TopologySearch) => {
      void navigate({ to: "/topology", search: next });
    },
    [navigate],
  );
  const onViewModeChange = useCallback(
    (mode: PortalViewMode) => setSearch({ ...search, view: mode === "constellation" ? "constellation" : undefined }),
    [search, setSearch],
  );
  const onFocusProject = useCallback(
    (projectId: string | null) =>
      setSearch({ ...search, project: projectId ? projectNameFromId(projectId) : undefined }),
    [search, setSearch],
  );

  const graph = graphQuery.data?.graph;
  const allProjects = overviewQuery.data?.projects ?? [];

  return (
    <Page width="full" className="gap-3 pt-4 pb-4">
      <h1 className="sr-only">Topology map</h1>
      {graph ? (
        <PortalCanvas
          graph={graph}
          allProjects={allProjects}
          focusedProjectId={focusedProjectId}
          onFocusProject={onFocusProject}
          viewMode={viewMode}
          onViewModeChange={onViewModeChange}
          category={category}
          onCategoryChange={setCategory}
          selectedWorkloadId={selectedWorkloadId ?? undefined}
          onSelectWorkload={setSelectedWorkloadId}
          heightClassName={CANVAS_HEIGHT}
        />
      ) : (
        <Card className={`${CANVAS_HEIGHT} min-h-[560px] justify-center`}>
          {graphQuery.isError ? (
            <ErrorState
              title="Couldn't load the topology"
              error={graphQuery.error}
              onRetry={() => void graphQuery.refetch()}
              retrying={graphQuery.isFetching}
            />
          ) : (
            <LoadingState label="Mapping projects, proxies and ports…" />
          )}
        </Card>
      )}
      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}
