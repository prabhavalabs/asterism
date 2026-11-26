import { useEffect, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ArrowUp01Icon, AuditIcon, Notification03Icon, SearchRemoveIcon } from "@hugeicons/core-free-icons";
import { eventsClient } from "@/lib/rpc";
import {
  ChipScroller,
  EmptyState,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  SectionCard,
  SegmentedControl,
  SortSelect,
  Toolbar,
  ToolbarSpacer,
  type SortOption,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { WorkloadInspectorSheet } from "@/components/topology/WorkloadInspectorSheet";
import { EventRow } from "@/components/portal/EventRow";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { EVENTS_LIST, type EventKindFilter as KindFilter, type EventSeverityFilter as SeverityFilter } from "@/lib/routeSearch";
import { liveStore } from "@/live/LiveStore";

/** ListEvents `kind_group` for the kind chips ("alerts" is the URL name of "other"). */
const KIND_GROUP: Record<KindFilter, string> = { all: "", lifecycle: "lifecycle", audit: "audit", alerts: "other" };
const SEVERITIES: Record<SeverityFilter, string[]> = { all: [], critical: ["critical"], warning: ["warning"], info: ["info"] };

const EVENT_SORTS: readonly SortOption[] = [
  { value: "ts desc", label: "Newest" },
  { value: "ts", label: "Oldest" },
  { value: "severity desc", label: "Severity" },
  { value: "kind", label: "Kind" },
  { value: "entity", label: "Entity" },
];

/** At most one refetch per this many milliseconds while events stream in. */
const LIVE_REFETCH_MS = 3_000;

export function EventsPage() {
  // Page, size, search, sort, severity and kind all live in the URL
  // (?kind=audit is the sidebar's audit log) and go to ListEvents.
  const paged = usePagedSearch(EVENTS_LIST);
  const severity = paged.filters.severity as SeverityFilter;
  const kind = paged.filters.kind as KindFilter;
  const isAudit = kind === "audit";
  const navigate = useNavigate();
  const [selectedWorkloadId, setSelectedWorkloadId] = useState<string | null>(null);
  const openEntity = (entityId: string) => {
    if (entityId.startsWith("ctr:")) {
      void navigate({ to: "/containers/$containerId", params: { containerId: entityId.slice(4) } });
      return;
    }
    setSelectedWorkloadId(entityId);
  };

  const query = useQuery({
    queryKey: paged.queryKey(["events", "page"]),
    queryFn: () =>
      eventsClient.listEvents({ ...paged.request, entityId: "", severities: SEVERITIES[severity], kindGroup: KIND_GROUP[kind] }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const events = query.data?.events ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "events");
  const sev = query.data?.severityCounts;
  const kinds = query.data?.kindCounts;

  // Live events: at the head of the timeline (page 1, newest first) the page
  // refetches, at most every few seconds. Anywhere else the rows stay put
  // and a "N new events" button counts what arrived.
  const atHead = paged.page === 1 && paged.order === EVENTS_LIST.defaultOrder;
  const viewKey = JSON.stringify(paged.queryKey([]));
  const [fresh, setFresh] = useState({ key: viewKey, count: 0 });
  const freshCount = fresh.key === viewKey ? fresh.count : 0;
  const { refetch } = query;
  useEffect(() => {
    let last = 0;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const run = () => {
      timer = null;
      last = Date.now();
      void refetch();
    };
    const unsubscribe = liveStore.subscribeEvents(() => {
      if (!atHead) {
        setFresh((prev) => ({ key: viewKey, count: (prev.key === viewKey ? prev.count : 0) + 1 }));
        return;
      }
      if (timer) return;
      const wait = Math.max(0, LIVE_REFETCH_MS - (Date.now() - last));
      timer = setTimeout(run, wait);
    });
    return () => {
      unsubscribe();
      if (timer) clearTimeout(timer);
    };
  }, [atHead, viewKey, refetch]);

  const severityOptions = [
    { value: "all" as const, label: "All", count: sev?.total ?? 0 },
    { value: "critical" as const, label: "Critical", count: sev?.critical ?? 0 },
    { value: "warning" as const, label: "Warning", count: sev?.warning ?? 0 },
    { value: "info" as const, label: "Info", count: sev?.info ?? 0 },
  ];
  const kindOptions = [
    { value: "all" as const, label: "All kinds", count: kinds?.total ?? 0 },
    { value: "lifecycle" as const, label: "Lifecycle", count: kinds?.lifecycle ?? 0 },
    { value: "audit" as const, label: "Audit", count: kinds?.audit ?? 0 },
    { value: "alerts" as const, label: "Other", count: kinds?.other ?? 0 },
  ];

  const filtered = paged.query !== "" || severity !== "all" || kind !== "all";
  const noAuditYet = isAudit && kinds !== undefined && kinds.audit === 0 && paged.query === "" && severity === "all";

  return (
    <Page>
      <PageHeader
        title={isAudit ? "Audit log" : "Events"}
        description={
          isAudit
            ? "Audited container actions: who requested them, the outcome and how long they took, newest first."
            : "Container lifecycle, proxy reloads, memory pressure alerts and audited actions, newest first."
        }
      />

      <Toolbar>
        <ChipScroller>
          <SegmentedControl
            aria-label="Severity"
            className="max-w-none"
            value={severity}
            onValueChange={(value) => paged.setFilter("severity", value)}
            options={severityOptions}
          />
        </ChipScroller>
        <ChipScroller>
          <SegmentedControl
            aria-label="Event kind"
            className="max-w-none"
            value={kind}
            onValueChange={(value) => paged.setFilter("kind", value)}
            options={kindOptions}
          />
        </ChipScroller>
        <ToolbarSpacer />
        <PagedSearchInput value={paged.query} onCommit={paged.setQuery} placeholder="Search events" aria-label="Search events" />
      </Toolbar>

      <SectionCard
        title="Timeline"
        icon={Notification03Icon}
        description={
          query.data ? (
            <span className="tabular-nums">
              {pager.totalSize} {filtered ? "matching " : ""}
              {pager.totalSize === 1 ? "event" : "events"}
              {atHead ? " · updates live" : ""}
            </span>
          ) : undefined
        }
        actions={
          <>
            {freshCount > 0 && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setFresh({ key: viewKey, count: 0 });
                  paged.update({ page: 1, order: EVENTS_LIST.defaultOrder });
                }}
              >
                <Icon icon={ArrowUp01Icon} />
                <span className="tabular-nums">
                  {freshCount} new {freshCount === 1 ? "event" : "events"}
                </span>
              </Button>
            )}
            <SortSelect size="sm" value={paged.order} onValueChange={paged.setOrder} options={EVENT_SORTS} className="min-w-36" />
          </>
        }
        flush
      >
        <PaginatedScrollArea
          status={query}
          isEmpty={events.length === 0}
          orientation="vertical"
          aria-label="Events"
          pagination={pager}
          empty={
            noAuditYet ? (
              <EmptyState
                icon={AuditIcon}
                title="No audited actions yet"
                description="Every container start, stop, restart, pause and unpause request lands here with who asked and how it ended."
                action={
                  <Button variant="outline" onClick={() => paged.setFilter("kind", "all")}>
                    Show all events
                  </Button>
                }
              />
            ) : !filtered ? (
              <EmptyState icon={Notification03Icon} title="No events yet" description="Lifecycle changes and alerts show up here as they happen." />
            ) : (
              <EmptyState
                size="compact"
                icon={SearchRemoveIcon}
                title="No events match"
                description="Try another severity, kind or search term."
                action={
                  <Button variant="outline" onClick={paged.reset}>
                    Clear filters
                  </Button>
                }
              />
            )
          }
        >
          <div className="divide-y divide-divider">
            {events.map((evt) => (
              <EventRow key={evt.id} event={evt} onSelectWorkload={openEntity} />
            ))}
          </div>
        </PaginatedScrollArea>
      </SectionCard>

      <WorkloadInspectorSheet workloadId={selectedWorkloadId} onClose={() => setSelectedWorkloadId(null)} />
    </Page>
  );
}
