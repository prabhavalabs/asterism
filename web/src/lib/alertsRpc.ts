import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AlertsService, NotificationsService } from "@/gen/asterism/v1/alerts_pb";

// Imported only by the Alerts and Notifications page chunks, so these
// descriptors stay out of the initial bundle.
const transport = createConnectTransport({
  baseUrl: "/",
  fetch: (input, init) => fetch(input, { ...init, credentials: "same-origin" }),
});

export const alertsClient = createClient(AlertsService, transport);
export const notificationsClient = createClient(NotificationsService, transport);
