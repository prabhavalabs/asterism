import { useQuery } from "@tanstack/react-query";
import { AGENTS_QUERY_KEY, AGENTS_REFETCH_MS } from "./model";

/**
 * ListAgents, refetched every 10 s while `enabled`. Every caller shares one
 * query (the shell, the Overview banner, Settings), so it is one request per
 * interval however many surfaces show agent status.
 */
export function useAgents(enabled: boolean) {
  return useQuery({
    queryKey: AGENTS_QUERY_KEY,
    queryFn: async () => {
      const { agentAdminClient } = await import("@/lib/agentRpc");
      return agentAdminClient.listAgents({});
    },
    enabled,
    refetchInterval: AGENTS_REFETCH_MS,
    staleTime: 5_000,
  });
}
