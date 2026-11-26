import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AgentAdminService } from "@/gen/asterism/v1/agent_pb";

/**
 * AgentAdminService client. Kept apart from `rpc.ts` and loaded with a
 * dynamic import so the agent descriptors stay out of the initial bundle in
 * the modes that never ask for them.
 */
const transport = createConnectTransport({
  baseUrl: "/",
  fetch: (input, init) => fetch(input, { ...init, credentials: "same-origin" }),
});

export const agentAdminClient = createClient(AgentAdminService, transport);
