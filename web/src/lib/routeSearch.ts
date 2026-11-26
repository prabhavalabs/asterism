/**
 * URL search params of the routes whose tabs, filters or sections are
 * deep-linkable from the sidebar. Validators drop unknown values, so a
 * hand-edited URL falls back to the default view. Kept out of the page
 * modules so the route tree can validate without loading a page chunk.
 */

import { validatePagedSearch, type PagedSearchConfig } from "./pagedSearch";

function oneOf<T extends string>(values: readonly T[], value: unknown): T | undefined {
  return typeof value === "string" && (values as readonly string[]).includes(value) ? (value as T) : undefined;
}

// --- /containers ------------------------------------------------------------

export const CONTAINERS_TABS = ["containers", "images", "volumes", "networks", "disk"] as const;
export type ContainersTab = (typeof CONTAINERS_TABS)[number];
export const CONTAINERS_TAB_LABELS: Record<ContainersTab, string> = {
  containers: "Containers",
  images: "Images",
  volumes: "Volumes",
  networks: "Networks",
  disk: "Disk usage",
};
/** Each field ascending and descending, the order_by values a list accepts. */
function orderValues(fields: readonly string[], ...extra: string[]): string[] {
  return [...extra, ...fields.flatMap((f) => [f, `${f} desc`])];
}

export const CONTAINER_STATE_FILTERS = ["all", "running", "stopped", "attention"] as const;
export type ContainerStateFilter = (typeof CONTAINER_STATE_FILTERS)[number];
/** Grouped by project when the order is this default. */
export const CONTAINERS_DEFAULT_ORDER = "project, name";

/**
 * The Containers tab (`/containers?page=2&size=50&q=redis&order=cpu%20desc
 * &state=running&project=proj%3Ashop`), paged by ListContainers.
 */
export const CONTAINERS_LIST = {
  defaultOrder: CONTAINERS_DEFAULT_ORDER,
  orders: orderValues(["name", "project", "state", "image", "cpu", "memory", "uptime", "restarts", "created"], CONTAINERS_DEFAULT_ORDER),
  filters: {
    state: { default: "all", values: CONTAINER_STATE_FILTERS },
    project: { default: "" },
  },
} as const satisfies PagedSearchConfig<"state" | "project">;

/** The Images tab (`?tab=images&imgPage=2&imgQ=nginx&imgFilter=dangling&imgOrder=created%20desc`). */
export const IMAGES_LIST = {
  prefix: "img",
  defaultOrder: "size desc",
  orders: orderValues(["size", "created", "repository", "containers"]),
  filters: { filter: { default: "all", values: ["all", "in-use", "dangling"] } },
} as const satisfies PagedSearchConfig<"filter">;

/** The Volumes tab (`?tab=volumes&volPage=2&volSize=50&volQ=data&volFilter=unused`). */
export const VOLUMES_LIST = {
  prefix: "vol",
  defaultOrder: "name",
  orders: orderValues(["name", "size", "created", "containers", "project"]),
  filters: { filter: { default: "all", values: ["all", "in-use", "unused"] } },
} as const satisfies PagedSearchConfig<"filter">;

export const NETWORK_FILTERS = ["user-defined", "all"] as const;
export type NetworkFilter = (typeof NETWORK_FILTERS)[number];

/**
 * The Networks tab (`?tab=networks&netQ=shop&netFilter=all&netOrder=containers%20desc`).
 * User-defined networks by default; `all` adds Docker's bridge, host and none.
 */
export const NETWORKS_LIST = {
  prefix: "net",
  defaultOrder: "name",
  orders: orderValues(["name", "driver", "containers", "created"]),
  filters: { filter: { default: "user-defined", values: NETWORK_FILTERS } },
} as const satisfies PagedSearchConfig<"filter">;

/** `tab` is absent for the default Containers tab; list keys only accompany their tab. */
export type ContainersSearch = {
  tab?: Exclude<ContainersTab, "containers">;
} & Record<string, string | number | undefined>;

export function validateContainersSearch(search: Record<string, unknown>): ContainersSearch {
  const tab = oneOf(CONTAINERS_TABS, search.tab);
  if (!tab || tab === "containers") return validatePagedSearch(search, CONTAINERS_LIST);
  const list = tab === "images" ? IMAGES_LIST : tab === "volumes" ? VOLUMES_LIST : tab === "networks" ? NETWORKS_LIST : undefined;
  return { tab, ...(list ? validatePagedSearch(search, list as PagedSearchConfig<string>) : {}) };
}

export const CONTAINER_DETAIL_TABS = ["overview", "config", "events", "logs"] as const;
export type ContainerDetailTab = (typeof CONTAINER_DETAIL_TABS)[number];

/** Events tab of a container (`/containers/web?tab=events&evPage=2`), paged by ListEvents. */
export const CONTAINER_EVENTS_LIST = {
  prefix: "ev",
  defaultOrder: "ts desc",
  defaultPageSize: 10,
  orders: orderValues(["ts", "severity", "kind"]),
} as const satisfies PagedSearchConfig;

export type ContainerDetailSearch = { tab?: Exclude<ContainerDetailTab, "overview"> } & Record<string, string | number | undefined>;

export function validateContainerDetailSearch(search: Record<string, unknown>): ContainerDetailSearch {
  const tab = oneOf(CONTAINER_DETAIL_TABS, search.tab);
  if (!tab || tab === "overview") return {};
  return { tab, ...(tab === "events" ? validatePagedSearch(search, CONTAINER_EVENTS_LIST) : {}) };
}

// --- /projects --------------------------------------------------------------

/** Project cards (`/projects?category=production&q=shop&order=cpu%20desc&page=2`), paged by ListProjects. */
export const PROJECTS_LIST = {
  defaultOrder: "memory desc",
  defaultPageSize: 12,
  pageSizeOptions: [12, 24, 48],
  orders: orderValues(["name", "category", "memory", "cpu", "workloads", "health"]),
  filters: { category: { default: "all" } },
} as const satisfies PagedSearchConfig<"category">;

export function validateProjectsSearch(search: Record<string, unknown>): Record<string, string | number> {
  return validatePagedSearch(search, PROJECTS_LIST);
}

/** Services of one project (`/projects/shop?q=api&order=memory%20desc`), paged by ListWorkloads. */
export const PROJECT_SERVICES_LIST = {
  defaultOrder: "name",
  defaultPageSize: 10,
  orders: orderValues(["name", "role", "state", "cpu", "memory", "restarts"]),
} as const satisfies PagedSearchConfig;

export function validateProjectDetailSearch(search: Record<string, unknown>): Record<string, string | number> {
  return validatePagedSearch(search, PROJECT_SERVICES_LIST);
}

// --- /ports -----------------------------------------------------------------

export const PORTS_TABS = ["ports", "ingress"] as const;
export type PortsTab = (typeof PORTS_TABS)[number];
export const PORTS_TAB_LABELS: Record<PortsTab, string> = {
  ports: "Listening ports",
  ingress: "Ingress & domains",
};
export const EXPOSURE_FILTERS = ["all", "public", "loopback", "internal"] as const;

/** Listening ports (`/ports?exposure=public&q=5432&order=workload`), paged by ListPorts. */
export const PORTS_LIST = {
  defaultOrder: "exposure, port",
  orders: orderValues(["exposure", "port", "workload", "project", "proto"], "exposure, port"),
  filters: { exposure: { default: "all", values: EXPOSURE_FILTERS } },
} as const satisfies PagedSearchConfig<"exposure">;

export const INGRESS_VIEWS = ["table", "cards"] as const;

/**
 * Ingress routes (`/ports?tab=ingress&routeQ=shop&routeView=cards`), paged by
 * ListIngress. One list, shown as a domains table or as route cards.
 */
export const INGRESS_LIST = {
  prefix: "route",
  defaultOrder: "hostname",
  defaultPageSize: 24,
  pageSizeOptions: [12, 24, 48, 96],
  orders: orderValues(["hostname", "port", "proxy", "upstreams"]),
  filters: { view: { default: "table", values: INGRESS_VIEWS } },
} as const satisfies PagedSearchConfig<"view">;

export type PortsSearch = { tab?: "ingress" } & Record<string, string | number | undefined>;

export function validatePortsSearch(search: Record<string, unknown>): PortsSearch {
  if (oneOf(PORTS_TABS, search.tab) === "ingress") return { tab: "ingress", ...validatePagedSearch(search, INGRESS_LIST) };
  return validatePagedSearch(search, PORTS_LIST);
}

// --- /events ----------------------------------------------------------------

export const EVENT_KINDS = ["all", "lifecycle", "audit", "alerts"] as const;
export type EventKindFilter = (typeof EVENT_KINDS)[number];
export const EVENT_SEVERITIES = ["all", "critical", "warning", "info"] as const;
export type EventSeverityFilter = (typeof EVENT_SEVERITIES)[number];
/**
 * The events timeline and audit log (`/events?kind=audit&severity=critical
 * &q=restart&page=2`), paged by ListEvents. `kind` and `severity` are absent
 * when "all".
 */
export const EVENTS_LIST = {
  defaultOrder: "ts desc",
  orders: orderValues(["ts", "severity", "kind", "entity"]),
  filters: {
    kind: { default: "all", values: EVENT_KINDS },
    severity: { default: "all", values: EVENT_SEVERITIES },
  },
} as const satisfies PagedSearchConfig<"kind" | "severity">;

export type EventsSearch = {
  kind?: Exclude<EventKindFilter, "all">;
  severity?: Exclude<EventSeverityFilter, "all">;
} & Record<string, string | number | undefined>;

export function validateEventsSearch(search: Record<string, unknown>): EventsSearch {
  return validatePagedSearch(search, EVENTS_LIST) as EventsSearch;
}

// --- /alerts ----------------------------------------------------------------

export const ALERTS_TABS = ["alerts", "rules", "silences"] as const;
export type AlertsTab = (typeof ALERTS_TABS)[number];
export const ALERTS_TAB_LABELS: Record<AlertsTab, string> = { alerts: "Alerts", rules: "Rules", silences: "Silences" };
export const ALERT_STATE_FILTERS = ["active", "firing", "pending", "resolved", "all"] as const;
export type AlertStateFilter = (typeof ALERT_STATE_FILTERS)[number];
export const ALERT_SEVERITY_FILTERS = ["all", "critical", "warning", "info"] as const;
export type AlertSeverityFilter = (typeof ALERT_SEVERITY_FILTERS)[number];

/**
 * The alerts list (`/alerts?state=resolved&severity=critical&q=disk`),
 * paged by ListAlerts. `state` is absent for "active" (firing + pending).
 */
export const ALERTS_LIST = {
  defaultOrder: "state, severity desc, since desc",
  orders: ["state, severity desc, since desc", ...orderValues(["since", "severity", "rule", "entity"])],
  filters: {
    state: { default: "active", values: ALERT_STATE_FILTERS },
    severity: { default: "all", values: ALERT_SEVERITY_FILTERS },
  },
} as const satisfies PagedSearchConfig<"state" | "severity">;

export type AlertsSearch = {
  tab?: Exclude<AlertsTab, "alerts">;
  /** Deep link from a notification: opens that alert. */
  alert?: string;
} & Record<string, string | number | undefined>;

export function validateAlertsSearch(search: Record<string, unknown>): AlertsSearch {
  const tab = oneOf(ALERTS_TABS, search.tab);
  const alert = typeof search.alert === "string" && search.alert.length > 0 && search.alert.length < 400 ? search.alert : undefined;
  return {
    ...(tab && tab !== "alerts" ? { tab } : {}),
    ...(alert ? { alert } : {}),
    ...validatePagedSearch(search, ALERTS_LIST),
  };
}

/** The delivery log on /settings/notifications (`?deliveryPage=2`). */
export const DELIVERIES_LIST = {
  prefix: "delivery",
  defaultPageSize: 10,
} as const satisfies PagedSearchConfig;

export function validateNotificationsSearch(search: Record<string, unknown>): Record<string, string | number> {
  return validatePagedSearch(search, DELIVERIES_LIST);
}

// --- /settings --------------------------------------------------------------

export const SETTINGS_SECTIONS = ["appearance", "session", "host", "agents", "mcp", "deployment"] as const;
export type SettingsSection = (typeof SETTINGS_SECTIONS)[number];
export type SettingsSearch = {
  section?: SettingsSection;
} & Record<string, string | number | undefined>;

/**
 * Enrolled agents on Settings (`/settings?section=agents&agentPage=2`).
 * ListAgents returns every agent in configuration order, so the table pages
 * client-side and keeps that order.
 */
export const AGENTS_LIST = {
  prefix: "agent",
  defaultPageSize: 10,
} as const satisfies PagedSearchConfig;

export function validateSettingsSearch(search: Record<string, unknown>): SettingsSearch {
  const section = oneOf(SETTINGS_SECTIONS, search.section);
  return { ...(section ? { section } : {}), ...validatePagedSearch(search, AGENTS_LIST) };
}

// --- /topology --------------------------------------------------------------

export const TOPOLOGY_VIEWS = ["isometric", "constellation"] as const;
export type TopologyView = (typeof TOPOLOGY_VIEWS)[number];
/** `view` is absent for the isometric default; `project` focuses one project. */
export interface TopologySearch {
  view?: "constellation";
  project?: string;
}

export function validateTopologySearch(search: Record<string, unknown>): TopologySearch {
  const view = oneOf(TOPOLOGY_VIEWS, search.view);
  const project = typeof search.project === "string" && search.project.length > 0 ? search.project : undefined;
  return {
    ...(view === "constellation" ? { view } : {}),
    ...(project ? { project } : {}),
  };
}
