import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  ActionsService,
  DockerService,
  EventsService,
  InventoryService,
  LiveService,
  LogsService,
  MetricsService,
  ProjectSettingsService,
  TopologyService,
} from "@/gen/asterism/v1/asterism_pb";

const transport = createConnectTransport({
  baseUrl: "/",
  fetch: (input, init) =>
    fetch(input, {
      ...init,
      credentials: "same-origin",
    }),
});

export const inventoryClient = createClient(InventoryService, transport);
export const metricsClient = createClient(MetricsService, transport);
export const topologyClient = createClient(TopologyService, transport);
export const eventsClient = createClient(EventsService, transport);
export const logsClient = createClient(LogsService, transport);
export const liveClient = createClient(LiveService, transport);
export const dockerClient = createClient(DockerService, transport);
export const actionsClient = createClient(ActionsService, transport);
export const projectSettingsClient = createClient(ProjectSettingsService, transport);

export interface AuthSession {
  authenticated: boolean;
  email?: string;
  expiresAt?: string;
}

export async function fetchAuthSession(): Promise<AuthSession> {
  const res = await fetch("/api/auth/me", {
    credentials: "same-origin",
    cache: "no-store",
  });
  if (!res.ok) {
    return { authenticated: false };
  }
  return (await res.json()) as AuthSession;
}

export async function loginAdmin(email: string, password: string): Promise<AuthSession> {
  const res = await fetch("/api/auth/login", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  const data = (await res.json()) as AuthSession & { error?: string };
  if (!res.ok) {
    throw new Error(data.error ?? "Authentication failed");
  }
  return data;
}

export async function logoutAdmin(): Promise<void> {
  await fetch("/api/auth/logout", {
    method: "POST",
    credentials: "same-origin",
  });
}
