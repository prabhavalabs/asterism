import { useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ArrowReloadHorizontalIcon, ArrowUp01Icon, ComputerTerminal01Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState, LoadingState, SearchInput, Toolbar, ToolbarSpacer } from "@/components/common";
import { logsClient } from "@/lib/rpc";
import { formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

export interface WorkloadLogsProps {
  workloadId: string;
  /** Lines per request: the first tail and every "Load older" page (max 500). */
  limit?: number;
  /** Show the grep box and refresh button. */
  toolbar?: boolean;
  className?: string;
  /** Tailwind max-height class for the scroll area. */
  maxHeightClassName?: string;
}

/**
 * Output of a workload through LogsService.TailLogs (on demand, never
 * persisted), newest line at the bottom. "Load older" at the top pages
 * backwards with the server's timestamp cursor (`before_ts_ms`) and keeps
 * the lines under the pointer where they were. The daemon masks tokens and
 * passwords before lines leave it.
 */
export function WorkloadLogs({
  workloadId,
  limit = 60,
  toolbar = false,
  className,
  maxHeightClassName = "max-h-[480px]",
}: WorkloadLogsProps) {
  const [grep, setGrep] = useState("");
  const deferredGrep = useDeferredValue(grep.trim());
  const query = useInfiniteQuery({
    queryKey: ["logs", workloadId, limit, deferredGrep],
    enabled: Boolean(workloadId),
    initialPageParam: 0n,
    queryFn: ({ pageParam }) => logsClient.tailLogs({ workloadId, limit, grep: deferredGrep, beforeTsMs: pageParam }),
    getNextPageParam: (last) => (last.hasMore && last.nextBeforeTsMs > 0n ? last.nextBeforeTsMs : undefined),
  });

  // pages[0] is the newest tail; each later page is older. Lines inside a
  // page are oldest first, so reverse the pages and keep each page's order.
  const lines = useMemo(() => [...(query.data?.pages ?? [])].reverse().flatMap((p) => p.lines), [query.data?.pages]);

  const viewportRef = useRef<HTMLDivElement>(null);
  const anchor = useRef<{ height: number; top: number } | null>(null);
  const stuckToBottom = useRef(true);

  const loadOlder = () => {
    const el = viewportRef.current;
    if (el) anchor.current = { height: el.scrollHeight, top: el.scrollTop };
    void query.fetchNextPage();
  };

  // After older lines are prepended, shift the scroll position by the height
  // they added so the visible lines stay put. Otherwise follow the newest
  // line while the reader is at the bottom.
  useLayoutEffect(() => {
    const el = viewportRef.current;
    if (!el) return;
    if (anchor.current) {
      el.scrollTop = el.scrollHeight - anchor.current.height + anchor.current.top;
      anchor.current = null;
    } else if (stuckToBottom.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [lines]);

  const hasLines = lines.length > 0;
  useEffect(() => {
    const el = viewportRef.current;
    if (!el) return;
    const onScroll = () => {
      stuckToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [hasLines]);

  return (
    <div className={cn("flex flex-col gap-3", className)}>
      {toolbar ? (
        <Toolbar>
          <SearchInput value={grep} onValueChange={setGrep} placeholder="Filter lines" aria-label="Filter log lines" />
          <ToolbarSpacer />
          <span className="hidden text-small text-muted-foreground sm:inline">Secrets are masked by the daemon.</span>
          <Button
            variant="outline"
            onClick={() => {
              stuckToBottom.current = true;
              void query.refetch();
            }}
            disabled={query.isFetching}
          >
            <Icon icon={ArrowReloadHorizontalIcon} className={cn(query.isFetching && !query.isFetchingNextPage && "animate-spin motion-reduce:animate-none")} />
            Refresh
          </Button>
        </Toolbar>
      ) : (
        <div className="flex items-center gap-2 text-small text-muted-foreground">
          <Icon icon={ComputerTerminal01Icon} className="size-4 shrink-0" />
          Recent output. Tokens and passwords are masked by the daemon.
        </div>
      )}
      {query.isPending ? (
        <LoadingState variant="rows" count={6} label="Loading logs…" />
      ) : query.isError && lines.length === 0 ? (
        <ErrorState size="compact" error={query.error} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : lines.length === 0 ? (
        <EmptyState
          size="compact"
          icon={ComputerTerminal01Icon}
          title={deferredGrep ? "No matching lines" : "No log lines"}
          description={deferredGrep ? `No retained line contains "${deferredGrep}".` : undefined}
        />
      ) : (
        <ScrollArea
          className="rounded-tile bg-surface-subtle shadow-card"
          viewportRef={viewportRef}
          viewportClassName={maxHeightClassName}
          viewportProps={{ tabIndex: 0, "aria-label": "Log lines" }}
        >
          <div className="flex min-h-11 items-center justify-between gap-3 border-b border-divider px-3 py-2 text-small text-muted-foreground">
            <span className="tabular-nums" aria-live="polite">
              {lines.length} {lines.length === 1 ? "line" : "lines"}
              {query.hasNextPage ? ", older lines available" : ", start of the retained log"}
            </span>
            {query.hasNextPage && (
              <Button variant="outline" size="sm" onClick={loadOlder} disabled={query.isFetchingNextPage}>
                <Icon
                  icon={query.isFetchingNextPage ? ArrowReloadHorizontalIcon : ArrowUp01Icon}
                  className={cn(query.isFetchingNextPage && "animate-spin motion-reduce:animate-none")}
                />
                {query.isFetchingNextPage ? "Loading…" : "Load older"}
              </Button>
            )}
          </div>
          <ol className="p-3 font-mono text-mono leading-6">
            {lines.map((line, i) => (
              <li key={lines.length - i} className="flex gap-3">
                <span className="w-16 shrink-0 text-right text-muted-foreground tabular-nums">{formatRelativeTime(line.tsMs)}</span>
                <span
                  className={cn(
                    "min-w-0 flex-1 break-words",
                    line.stream === "stderr" ? "text-status-warning" : "text-foreground",
                  )}
                >
                  {line.text}
                </span>
              </li>
            ))}
          </ol>
        </ScrollArea>
      )}
    </div>
  );
}
