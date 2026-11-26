import { useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  Alert02Icon,
  CheckmarkCircle02Icon,
  Clock01Icon,
  NotificationOff01Icon,
  SearchRemoveIcon,
  Settings02Icon,
} from "@hugeicons/core-free-icons";
import { AlertState, type Alert, type AlertRule, type Silence } from "@/gen/asterism/v1/alerts_pb";
import { alertsClient } from "@/lib/alertsRpc";
import {
  ChipScroller,
  DataTableFrame,
  EmptyState,
  Page,
  PageHeader,
  PagedSearchInput,
  PaginatedScrollArea,
  PropertyRow,
  SectionCard,
  SegmentedControl,
  SortSelect,
  StatCard,
  StatusPill,
  Toolbar,
  ToolbarSpacer,
  dataTableClasses,
  errorMessage,
  type SortOption,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RuleSheet } from "@/components/alerts/RuleSheet";
import { SilenceSheet } from "@/components/alerts/SilenceSheet";
import { entityHref, formatSeconds, ruleSummary, severityFromName, severityName, stateLabel, stateTone } from "@/components/alerts/model";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { formatDateTime, formatRelativeTime } from "@/lib/format";
import {
  ALERTS_LIST,
  ALERTS_TAB_LABELS,
  type AlertSeverityFilter,
  type AlertStateFilter,
  type AlertsSearch,
  type AlertsTab,
} from "@/lib/routeSearch";
import { cn } from "@/lib/utils";

const STATES: Record<AlertStateFilter, AlertState[]> = {
  active: [AlertState.FIRING, AlertState.PENDING],
  firing: [AlertState.FIRING],
  pending: [AlertState.PENDING],
  resolved: [AlertState.RESOLVED],
  all: [],
};

const ALERT_SORTS: readonly SortOption[] = [
  { value: ALERTS_LIST.defaultOrder, label: "Most urgent" },
  { value: "since desc", label: "Newest" },
  { value: "since", label: "Oldest" },
  { value: "rule", label: "Rule" },
  { value: "entity", label: "Entity" },
];

export function AlertsPage() {
  const search = useSearch({ strict: false }) as AlertsSearch;
  const navigate = useNavigate();
  const tab: AlertsTab = search.tab ?? "alerts";
  const setTab = (next: AlertsTab) =>
    void navigate({ to: "/alerts", search: next === "alerts" ? {} : { tab: next }, resetScroll: false });

  const tabs = (Object.keys(ALERTS_TAB_LABELS) as AlertsTab[]).map((value) => ({ value, label: ALERTS_TAB_LABELS[value] }));

  return (
    <Page>
      <PageHeader
        title="Alerts"
        description="Built-in rules evaluated against live inventory and events. Firing and resolved alerts go to your notification channels."
        actions={
          <Button variant="outline" asChild>
            <Link to="/settings/notifications">Notification channels</Link>
          </Button>
        }
      />
      <AlertStats />
      <Toolbar>
        <SegmentedControl aria-label="Alerts view" value={tab} onValueChange={setTab} options={tabs} />
      </Toolbar>
      {tab === "alerts" && <AlertsList openAlertId={search.alert} />}
      {tab === "rules" && <RulesList />}
      {tab === "silences" && <SilencesList />}
    </Page>
  );
}

/** Four equal KPI tiles; counts come from one unfiltered page-of-10 query. */
function AlertStats() {
  const q = useQuery({
    queryKey: ["alerts", "counts"],
    queryFn: () => alertsClient.listAlerts({ page: { pageSize: 10, page: 1 } }),
    refetchInterval: 10_000,
  });
  const silences = useQuery({
    queryKey: ["alerts", "silences", false],
    queryFn: () => alertsClient.listSilences({ includeExpired: false }),
    refetchInterval: 30_000,
  });
  const c = q.data?.stateCounts;
  const firing = (c?.firing ?? 0) - (c?.silenced ?? 0);
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard label="Firing" value={q.data ? firing : "—"} icon={Alert02Icon} tone={firing > 0 ? "critical" : "healthy"} sub="Notifying, not silenced" />
      <StatCard label="Pending" value={q.data ? (c?.pending ?? 0) : "—"} icon={Clock01Icon} tone="info" sub="Waiting out the rule's duration" />
      <StatCard
        label="Silenced"
        value={q.data ? (c?.silenced ?? 0) : "—"}
        icon={NotificationOff01Icon}
        tone="neutral"
        sub={`${silences.data?.silences.length ?? 0} active ${silences.data?.silences.length === 1 ? "silence" : "silences"}`}
      />
      <StatCard label="Resolved" value={q.data ? (c?.resolved ?? 0) : "—"} icon={CheckmarkCircle02Icon} tone="healthy" sub="Kept for 7 days" />
    </div>
  );
}

function AlertStatePill({ alert }: { alert: Alert }) {
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      <StatusPill tone={stateTone(alert)} pulse={alert.state === AlertState.FIRING && !alert.silencedBy}>
        {stateLabel(alert.state)}
      </StatusPill>
      {alert.silencedBy && <Badge variant="neutral">Silenced</Badge>}
      {alert.flapping && <Badge variant="warning">Flapping</Badge>}
    </span>
  );
}

function EntityCell({ alert }: { alert: Alert }) {
  const href = entityHref(alert.entityId);
  const name = alert.entityName || alert.entityId;
  return (
    <span className="flex min-w-0 flex-col">
      {href ? (
        <a href={href} className="truncate text-foreground hover:underline">
          {name}
        </a>
      ) : (
        <span className="truncate">{name}</span>
      )}
      <span className="truncate font-mono text-small text-text-4">{alert.projectId || alert.entityId}</span>
    </span>
  );
}

function AlertsList({ openAlertId }: { openAlertId?: string }) {
  const paged = usePagedSearch(ALERTS_LIST);
  const navigate = useNavigate();
  const state = paged.filters.state as AlertStateFilter;
  const severity = paged.filters.severity as AlertSeverityFilter;
  const [silenceFor, setSilenceFor] = useState<Alert | null>(null);
  const [silenceOpen, setSilenceOpen] = useState(false);

  const query = useQuery({
    queryKey: paged.queryKey(["alerts", "list"]),
    queryFn: () =>
      alertsClient.listAlerts({
        page: paged.request.page,
        query: paged.request.query,
        orderBy: paged.request.orderBy,
        states: STATES[state],
        severities: severity === "all" ? [] : [severityFromName(severity)],
      }),
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  });
  const alerts = query.data?.alerts ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "alerts");
  const c = query.data?.stateCounts;

  const detail = useQuery({
    queryKey: ["alerts", "detail", openAlertId],
    enabled: Boolean(openAlertId),
    queryFn: async () => {
      const r = await alertsClient.listAlerts({ query: openAlertId, page: { pageSize: 10, page: 1 } });
      return r.alerts.find((a) => a.id === openAlertId) ?? null;
    },
  });
  const closeDetail = () =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, alert: undefined }), resetScroll: false });
  const openDetail = (a: Alert) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, alert: a.id }), resetScroll: false });

  const silence = (a: Alert | null) => {
    setSilenceFor(a);
    setSilenceOpen(true);
  };

  const stateOptions = [
    { value: "active" as const, label: "Active", count: (c?.firing ?? 0) + (c?.pending ?? 0) },
    { value: "firing" as const, label: "Firing", count: c?.firing ?? 0 },
    { value: "pending" as const, label: "Pending", count: c?.pending ?? 0 },
    { value: "resolved" as const, label: "Resolved", count: c?.resolved ?? 0 },
    { value: "all" as const, label: "All", count: (c?.firing ?? 0) + (c?.pending ?? 0) + (c?.resolved ?? 0) },
  ];
  const severityOptions = [
    { value: "all" as const, label: "Any severity" },
    { value: "critical" as const, label: "Critical" },
    { value: "warning" as const, label: "Warning" },
    { value: "info" as const, label: "Info" },
  ];
  const filtered = paged.query !== "" || state !== "active" || severity !== "all";

  return (
    <>
      <Toolbar>
        <ChipScroller>
          <SegmentedControl aria-label="Alert state" className="max-w-none" value={state} onValueChange={(v) => paged.setFilter("state", v)} options={stateOptions} />
        </ChipScroller>
        <ChipScroller>
          <SegmentedControl aria-label="Severity" className="max-w-none" value={severity} onValueChange={(v) => paged.setFilter("severity", v)} options={severityOptions} />
        </ChipScroller>
        <ToolbarSpacer />
        <PagedSearchInput value={paged.query} onCommit={paged.setQuery} placeholder="Search alerts" aria-label="Search alerts" />
      </Toolbar>
      <SectionCard
        title={state === "resolved" ? "Recent history" : state === "all" ? "All alerts" : "Active alerts"}
        icon={Alert02Icon}
        description={query.data ? <span className="tabular-nums">{pager.totalSize} {pager.totalSize === 1 ? "alert" : "alerts"} · updates every 10 s</span> : undefined}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => silence(null)}>
              New silence
            </Button>
            <SortSelect size="sm" value={paged.order} onValueChange={paged.setOrder} options={ALERT_SORTS} className="min-w-36" />
          </>
        }
        flush
      >
        <DataTableFrame
          status={query}
          isEmpty={alerts.length === 0}
          minWidthClassName="min-w-[880px]"
          aria-label="Alerts"
          pagination={pager}
          empty={
            filtered ? (
              <EmptyState
                size="compact"
                icon={SearchRemoveIcon}
                title="No alerts match"
                description="Try another state, severity or search term."
                action={
                  <Button variant="outline" onClick={paged.reset}>
                    Clear filters
                  </Button>
                }
              />
            ) : (
              <EmptyState icon={CheckmarkCircle02Icon} title="All clear" description="No rule is firing or pending. Resolved alerts stay under Resolved for a week." />
            )
          }
        >
          <colgroup>
            <col className="w-28" />
            <col />
            <col className="w-56" />
            <col className="w-40" />
            <col className="w-24" />
            <col className="w-28" />
          </colgroup>
          <thead className={dataTableClasses.thead}>
            <tr>
              <th className={dataTableClasses.th}>Severity</th>
              <th className={dataTableClasses.th}>Alert</th>
              <th className={dataTableClasses.th}>Entity</th>
              <th className={dataTableClasses.th}>State</th>
              <th className={dataTableClasses.th}>Since</th>
              <th className={cn(dataTableClasses.th, "text-right")}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {alerts.map((a) => (
              <tr key={a.id} className={dataTableClasses.tr}>
                <td className={dataTableClasses.td}>
                  <StatusPill severity={severityName(a.severity)} />
                </td>
                <td className={cn(dataTableClasses.td, "py-2")}>
                  <button type="button" className="flex min-w-0 max-w-full flex-col text-left" onClick={() => openDetail(a)}>
                    <span className="truncate font-medium text-foreground hover:underline">{a.ruleName}</span>
                    <span className="truncate text-small text-muted-foreground">{a.summary}</span>
                  </button>
                </td>
                <td className={dataTableClasses.td}>
                  <EntityCell alert={a} />
                </td>
                <td className={dataTableClasses.td}>
                  <AlertStatePill alert={a} />
                </td>
                <td className={cn(dataTableClasses.td, "font-mono text-small tabular-nums text-muted-foreground")} title={formatDateTime(a.startedAtMs)}>
                  {formatRelativeTime(a.state === AlertState.RESOLVED ? a.resolvedAtMs : a.startedAtMs)}
                </td>
                <td className={cn(dataTableClasses.td, "text-right")}>
                  {a.state !== AlertState.RESOLVED && !a.silencedBy && (
                    <Button variant="ghost" size="sm" onClick={() => silence(a)}>
                      Silence
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </DataTableFrame>
      </SectionCard>
      <AlertDetailSheet alert={detail.data ?? null} open={Boolean(openAlertId)} loading={detail.isPending} onClose={closeDetail} onSilence={silence} />
      <SilenceSheet alert={silenceFor} open={silenceOpen} onOpenChange={setSilenceOpen} />
    </>
  );
}

function AlertDetailSheet({
  alert,
  open,
  loading,
  onClose,
  onSilence,
}: {
  alert: Alert | null;
  open: boolean;
  loading: boolean;
  onClose: () => void;
  onSilence: (a: Alert) => void;
}) {
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>{alert?.ruleName ?? (loading ? "Loading alert…" : "Alert not found")}</SheetTitle>
          <SheetDescription>{alert?.summary ?? (loading ? "" : "It may have been pruned from the 7-day history.")}</SheetDescription>
        </SheetHeader>
        {alert && (
          <SheetBody className="p-0">
            <div className="divide-y divide-divider">
              <PropertyRow label="Severity">
                <StatusPill severity={severityName(alert.severity)} />
              </PropertyRow>
              <PropertyRow label="State">
                <AlertStatePill alert={alert} />
              </PropertyRow>
              <PropertyRow label="Entity" mono>
                {alert.entityId}
              </PropertyRow>
              {alert.projectId && (
                <PropertyRow label="Project" mono>
                  {alert.projectId}
                </PropertyRow>
              )}
              <PropertyRow label="Rule" mono>
                {alert.ruleId}
              </PropertyRow>
              <PropertyRow label="Pending since" mono>
                {formatDateTime(alert.startedAtMs)}
              </PropertyRow>
              {alert.firingAtMs > 0n && (
                <PropertyRow label="Firing since" mono>
                  {formatDateTime(alert.firingAtMs)}
                </PropertyRow>
              )}
              {alert.resolvedAtMs > 0n && (
                <PropertyRow label="Resolved" mono>
                  {formatDateTime(alert.resolvedAtMs)}
                </PropertyRow>
              )}
              <PropertyRow label="Last notified" mono>
                {alert.lastNotifiedMs > 0n ? formatDateTime(alert.lastNotifiedMs) : "Not yet"}
              </PropertyRow>
              {alert.value !== 0 && (
                <PropertyRow label="Value" mono>
                  {alert.value.toFixed(1)}
                </PropertyRow>
              )}
              {Object.entries(alert.labels).map(([k, v]) => (
                <PropertyRow key={k} label={k} mono>
                  {v}
                </PropertyRow>
              ))}
              <PropertyRow label="Fingerprint" mono>
                {alert.id}
              </PropertyRow>
            </div>
          </SheetBody>
        )}
        <SheetFooter>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          {alert && alert.state !== AlertState.RESOLVED && !alert.silencedBy && (
            <Button
              onClick={() => {
                onClose();
                onSilence(alert);
              }}
            >
              Silence
            </Button>
          )}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

function RulesList() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<AlertRule | null>(null);
  const query = useQuery({ queryKey: ["alerts", "rules"], queryFn: () => alertsClient.listRules({}) });
  const toggle = useMutation({
    mutationFn: (r: AlertRule) => alertsClient.updateRule({ rule: { ...r, enabled: !r.enabled } }),
    onSuccess: (r) => {
      toast.success(`${r.name} ${r.enabled ? "enabled" : "disabled"}`);
      void qc.invalidateQueries({ queryKey: ["alerts"] });
    },
    onError: (err) => toast.error("Could not update the rule", { description: errorMessage(err) }),
  });
  const rules = query.data?.rules ?? [];
  return (
    <SectionCard
      title="Rules"
      icon={Settings02Icon}
      description={query.data ? <span className="tabular-nums">{rules.filter((r) => r.enabled).length} of {rules.length} enabled</span> : undefined}
      flush
    >
      <PaginatedScrollArea status={query} isEmpty={rules.length === 0} orientation="vertical" aria-label="Alert rules">
        <ul className="divide-y divide-divider">
          {rules.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
              <Switch checked={r.enabled} disabled={toggle.isPending} onCheckedChange={() => toggle.mutate(r)} aria-label={`${r.enabled ? "Disable" : "Enable"} ${r.name}`} />
              <div className="flex min-w-0 flex-1 basis-72 flex-col">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">{r.name}</span>
                  {!r.isDefault && <Badge variant="info">Customised</Badge>}
                </span>
                <span className="text-small text-muted-foreground">{r.description}</span>
              </div>
              <span className={cn("min-w-0 basis-56 font-mono text-small tabular-nums", r.enabled ? "text-text-2" : "text-text-4")}>
                {ruleSummary(r)}
                {r.includeProjects.length > 0 && ` · only ${r.includeProjects.join(", ")}`}
                {r.excludeProjects.length > 0 && ` · not ${r.excludeProjects.join(", ")}`}
              </span>
              <Button variant="outline" size="sm" onClick={() => setEditing(r)}>
                Edit
              </Button>
            </li>
          ))}
        </ul>
      </PaginatedScrollArea>
      <RuleSheet rule={editing} onOpenChange={(o) => !o && setEditing(null)} />
    </SectionCard>
  );
}

function SilencesList() {
  const qc = useQueryClient();
  const [showExpired, setShowExpired] = useState(false);
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: ["alerts", "silences", showExpired],
    queryFn: () => alertsClient.listSilences({ includeExpired: showExpired }),
    refetchInterval: 30_000,
  });
  const expire = useMutation({
    mutationFn: (s: Silence) => alertsClient.deleteSilence({ id: s.id }),
    onSuccess: () => {
      toast.success("Silence expired");
      void qc.invalidateQueries({ queryKey: ["alerts"] });
    },
    onError: (err) => toast.error("Could not expire the silence", { description: errorMessage(err) }),
  });
  const silences = query.data?.silences ?? [];
  const matchers = (s: Silence) =>
    [s.ruleId && `rule ${s.ruleId}`, s.entityId && `entity ${s.entityId}`, s.projectId && `project ${s.projectId}`].filter(Boolean).join(" · ");
  return (
    <SectionCard
      title="Silences"
      icon={NotificationOff01Icon}
      description="Mute matching alerts for a while; they keep evaluating."
      actions={
        <>
          <label className="flex items-center gap-2 text-small text-muted-foreground">
            <Switch checked={showExpired} onCheckedChange={setShowExpired} aria-label="Show expired silences" />
            Expired
          </label>
          <Button size="sm" onClick={() => setOpen(true)}>
            New silence
          </Button>
        </>
      }
      flush
    >
      <PaginatedScrollArea
        status={query}
        isEmpty={silences.length === 0}
        orientation="vertical"
        aria-label="Silences"
        empty={<EmptyState size="compact" icon={NotificationOff01Icon} title="No silences" description="Silence an alert from its row, or create one for a planned maintenance window." />}
      >
        <ul className="divide-y divide-divider">
          {silences.map((s) => (
            <li key={s.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
              <StatusPill tone={s.active ? "info" : "neutral"}>{s.active ? "Active" : "Ended"}</StatusPill>
              <div className="flex min-w-0 flex-1 basis-72 flex-col">
                <span className="truncate font-mono text-control text-foreground">{matchers(s)}</span>
                <span className="truncate text-small text-muted-foreground">
                  {s.reason} · by {s.createdBy}
                </span>
              </div>
              <span className="font-mono text-small tabular-nums text-muted-foreground" title={formatDateTime(s.endsAtMs)}>
                {s.active ? `ends in ${formatSeconds(Math.max(0, Math.round((Number(s.endsAtMs) - Date.now()) / 1000)))}` : `ended ${formatRelativeTime(s.endsAtMs)}`}
              </span>
              {s.active && (
                <Button variant="outline" size="sm" disabled={expire.isPending} onClick={() => expire.mutate(s)}>
                  Expire now
                </Button>
              )}
            </li>
          ))}
        </ul>
      </PaginatedScrollArea>
      <SilenceSheet alert={null} open={open} onOpenChange={setOpen} />
    </SectionCard>
  );
}
