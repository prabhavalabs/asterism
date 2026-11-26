import { useEffect, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import { ComputerIcon, Mail01Icon, PlusSignIcon, Rocket01Icon, TaskDone01Icon } from "@hugeicons/core-free-icons";
import { ChannelType, type DeliveryAttempt, type NotificationChannel } from "@/gen/asterism/v1/alerts_pb";
import { notificationsClient } from "@/lib/alertsRpc";
import {
  DataTableFrame,
  EmptyState,
  Page,
  PageHeader,
  PaginatedScrollArea,
  SectionCard,
  StatusPill,
  dataTableClasses,
  errorMessage,
  type StatusTone,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { ChannelSheet } from "@/components/alerts/ChannelSheet";
import { CHANNEL_TYPE_LABELS, severityName } from "@/components/alerts/model";
import { currentSubscription, subscribeBrowser, unsubscribeBrowser, webPushUnsupportedReason } from "@/components/alerts/webPush";
import { usePagedSearch, usePager } from "@/hooks/use-paged-search";
import { formatDateTime, formatRelativeTime } from "@/lib/format";
import { DELIVERIES_LIST } from "@/lib/routeSearch";
import { cn } from "@/lib/utils";

const DELIVERY_TONES: Record<string, StatusTone> = { sent: "healthy", retrying: "warning", failed: "critical", rate_limited: "warning", dropped: "critical" };
const DELIVERY_LABELS: Record<string, string> = { sent: "Sent", retrying: "Retrying", failed: "Failed", rate_limited: "Rate limited", dropped: "Dropped" };

function DeliveryPill({ d }: { d: DeliveryAttempt }) {
  return <StatusPill tone={DELIVERY_TONES[d.status] ?? "neutral"}>{DELIVERY_LABELS[d.status] ?? d.status}</StatusPill>;
}

export function NotificationsPage() {
  return (
    <Page>
      <PageHeader
        title="Notifications"
        description="Where firing and resolved alerts are sent. Changes need an admin session and are recorded in the audit log."
        actions={
          <Button variant="outline" asChild>
            <Link to="/alerts" search={{ tab: "rules" }}>
              Alert rules
            </Link>
          </Button>
        }
      />
      <ChannelsCard />
      <BrowserPushCard />
      <DeliveryLogCard />
    </Page>
  );
}

function ChannelsCard() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<NotificationChannel | "new" | null>(null);
  const [deleting, setDeleting] = useState<NotificationChannel | null>(null);
  const query = useQuery({
    queryKey: ["notifications", "channels"],
    queryFn: () => notificationsClient.listChannels({}),
    refetchInterval: 15_000,
  });
  const channels = query.data?.channels ?? [];
  const invalidate = () => void qc.invalidateQueries({ queryKey: ["notifications"] });

  const test = useMutation({
    mutationFn: (ch: NotificationChannel) => notificationsClient.testChannel({ id: ch.id }),
    onSuccess: (r, ch) => {
      const d = r.delivery;
      if (d?.status === "sent") toast.success(`Test sent to ${ch.name}`, { description: d.httpStatus ? `HTTP ${d.httpStatus} in ${d.durationMs} ms` : `${d.durationMs} ms` });
      else toast.error(`Test to ${ch.name} failed`, { description: d?.error || "No details" });
      invalidate();
    },
    onError: (err) => toast.error("Could not send a test", { description: errorMessage(err) }),
  });
  const toggle = useMutation({
    mutationFn: (ch: NotificationChannel) =>
      notificationsClient.updateChannel({ channel: { ...ch, enabled: !ch.enabled, secrets: [], lastDelivery: undefined } }),
    onSuccess: (ch) => {
      toast.success(`${ch.name} ${ch.enabled ? "enabled" : "paused"}`);
      invalidate();
    },
    onError: (err) => toast.error("Could not update the channel", { description: errorMessage(err) }),
  });
  const remove = useMutation({
    mutationFn: (ch: NotificationChannel) => notificationsClient.deleteChannel({ id: ch.id }),
    onSuccess: (_, ch) => {
      toast.success(`Deleted ${ch.name}`);
      invalidate();
    },
    onError: (err) => toast.error("Could not delete the channel", { description: errorMessage(err) }),
  });

  return (
    <SectionCard
      title="Channels"
      icon={Mail01Icon}
      description={query.data ? <span className="tabular-nums">{channels.filter((c) => c.enabled).length} of {channels.length} enabled</span> : undefined}
      actions={
        <Button size="sm" onClick={() => setEditing("new")}>
          <Icon icon={PlusSignIcon} />
          Add channel
        </Button>
      }
      flush
    >
      <PaginatedScrollArea
        status={query}
        isEmpty={channels.length === 0}
        orientation="vertical"
        aria-label="Notification channels"
        empty={
          <EmptyState
            icon={Mail01Icon}
            title="No channels yet"
            description="Add Slack, a webhook, email, Discord or Telegram, or enable browser notifications below. Alerts still fire and appear on the Alerts page without one."
            action={
              <Button variant="outline" onClick={() => setEditing("new")}>
                Add channel
              </Button>
            }
          />
        }
      >
        <ul className="divide-y divide-divider">
          {channels.map((ch) => {
            const d = ch.lastDelivery;
            return (
              <li key={ch.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
                <Switch
                  checked={ch.enabled}
                  disabled={toggle.isPending}
                  onCheckedChange={() => toggle.mutate(ch)}
                  aria-label={`${ch.enabled ? "Pause" : "Enable"} ${ch.name}`}
                />
                <div className="flex min-w-0 flex-1 basis-64 flex-col">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium text-foreground">{ch.name}</span>
                    <Badge variant="outline">{CHANNEL_TYPE_LABELS[ch.type]}</Badge>
                    {ch.secrets.some((s) => s.hasValue) && (
                      <Badge variant="neutral" title="Write-only secrets are stored encrypted">
                        •••• set
                      </Badge>
                    )}
                  </span>
                  <span className="truncate text-small text-muted-foreground">
                    ≥ {severityName(ch.minSeverity)}
                    {ch.sendResolved ? " · resolves" : ""}
                    {ch.rateLimitPerHour ? ` · ${ch.rateLimitPerHour}/h` : " · no rate limit"}
                    {ch.projects.length ? ` · ${ch.projects.join(", ")}` : ""}
                    {ch.kinds.length ? ` · ${ch.kinds.join(", ")}` : ""}
                  </span>
                </div>
                <div className="flex min-w-0 basis-56 flex-col items-start gap-1">
                  {d ? (
                    <>
                      <span className="flex items-center gap-2">
                        <DeliveryPill d={d} />
                        <span className="font-mono text-small tabular-nums text-muted-foreground" title={formatDateTime(d.tsMs)}>
                          {formatRelativeTime(d.tsMs)}
                        </span>
                      </span>
                      {d.error && <span className="line-clamp-1 text-small text-status-critical">{d.error}</span>}
                    </>
                  ) : (
                    <span className="text-small text-text-4">No deliveries yet</span>
                  )}
                </div>
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" disabled={test.isPending} onClick={() => test.mutate(ch)}>
                    Send test
                  </Button>
                  {ch.type !== ChannelType.WEBPUSH && (
                    <Button variant="outline" size="sm" onClick={() => setEditing(ch)}>
                      Edit
                    </Button>
                  )}
                  <Button variant="ghost" size="sm" onClick={() => setDeleting(ch)}>
                    Delete
                  </Button>
                </div>
              </li>
            );
          })}
        </ul>
      </PaginatedScrollArea>
      <ChannelSheet channel={editing} onOpenChange={(o) => !o && setEditing(null)} />
      <AlertDialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {deleting?.name}?</AlertDialogTitle>
            <AlertDialogDescription>Its stored secrets and delivery log are removed. Alerts keep firing for other channels.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (deleting) remove.mutate(deleting);
                setDeleting(null);
              }}
            >
              Delete channel
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SectionCard>
  );
}

function BrowserPushCard() {
  const qc = useQueryClient();
  const unsupported = webPushUnsupportedReason();
  const [endpoint, setEndpoint] = useState<string | null | undefined>(undefined);
  const config = useQuery({ queryKey: ["notifications", "webpush"], queryFn: () => notificationsClient.getWebPushConfig({}) });
  const devices = useQuery({ queryKey: ["notifications", "webpush", "subs"], queryFn: () => notificationsClient.listWebPushSubscriptions({}) });

  useEffect(() => {
    let alive = true;
    void currentSubscription().then((s) => alive && setEndpoint(s?.endpoint ?? null));
    return () => {
      alive = false;
    };
  }, []);

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["notifications"] });
  const enable = useMutation({
    mutationFn: async () => {
      const key = config.data?.publicKey;
      if (!key) throw new Error(config.data?.unavailableReason || "Web Push is not available on this hub.");
      const sub = await subscribeBrowser(key);
      await notificationsClient.subscribeWebPush(sub);
      return sub.endpoint;
    },
    onSuccess: (ep) => {
      setEndpoint(ep);
      toast.success("Browser notifications enabled", { description: "Use Send test on the Browser notifications channel to try it." });
      invalidate();
    },
    onError: (err) => toast.error("Could not enable browser notifications", { description: errorMessage(err) }),
  });
  const disable = useMutation({
    mutationFn: async () => {
      const ep = await unsubscribeBrowser();
      if (ep) await notificationsClient.unsubscribeWebPush({ endpoint: ep });
    },
    onSuccess: () => {
      setEndpoint(null);
      toast.success("Browser notifications turned off on this device");
      invalidate();
    },
    onError: (err) => toast.error("Could not turn off notifications", { description: errorMessage(err) }),
  });
  const forget = useMutation({
    mutationFn: (id: string) => notificationsClient.unsubscribeWebPush({ id }),
    onSuccess: invalidate,
    onError: (err) => toast.error("Could not remove the device", { description: errorMessage(err) }),
  });

  const subscribed = Boolean(endpoint);
  const reason = unsupported ?? (config.data && !config.data.available ? config.data.unavailableReason : undefined);
  const subs = devices.data?.subscriptions ?? [];

  return (
    <SectionCard
      title="Browser notifications"
      icon={ComputerIcon}
      description={`${subs.length} ${subs.length === 1 ? "device" : "devices"} subscribed`}
      actions={
        subscribed ? (
          <Button size="sm" variant="outline" disabled={disable.isPending} onClick={() => disable.mutate()}>
            Turn off on this device
          </Button>
        ) : (
          <Button size="sm" disabled={Boolean(reason) || enable.isPending || endpoint === undefined} onClick={() => enable.mutate()}>
            {enable.isPending ? "Enabling…" : "Enable browser notifications on this device"}
          </Button>
        )
      }
      flush
    >
      <div className="flex flex-col divide-y divide-divider">
        <p className="px-4 py-3 text-small text-muted-foreground">
          {reason ??
            (subscribed
              ? "This browser receives alerts through the Browser notifications channel, even with the tab closed."
              : "Subscribe this browser to get system notifications for alerts. Each device subscribes separately.")}
        </p>
        {subs.map((s) => (
          <div key={s.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5">
            <span className="min-w-0 flex-1 truncate text-control text-foreground">{s.label}</span>
            <span className="font-mono text-small text-text-4">{s.endpointHost}</span>
            <span className="font-mono text-small tabular-nums text-muted-foreground">
              {s.lastSuccessMs > 0n ? `last push ${formatRelativeTime(s.lastSuccessMs)}` : `added ${formatRelativeTime(s.createdAtMs)}`}
            </span>
            <Button variant="ghost" size="sm" disabled={forget.isPending} onClick={() => forget.mutate(s.id)}>
              Remove
            </Button>
          </div>
        ))}
      </div>
    </SectionCard>
  );
}

function DeliveryLogCard() {
  const paged = usePagedSearch(DELIVERIES_LIST);
  const [channel, setChannel] = useState("all");
  const channels = useQuery({ queryKey: ["notifications", "channels"], queryFn: () => notificationsClient.listChannels({}) });
  const query = useQuery({
    queryKey: paged.queryKey(["notifications", "deliveries", channel]),
    queryFn: () => notificationsClient.listDeliveries({ channelId: channel === "all" ? "" : channel, page: paged.request.page }),
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  });
  const rows = query.data?.deliveries ?? [];
  const pager = usePager(paged, query.data?.pageInfo, "deliveries");
  const names = new Map((channels.data?.channels ?? []).map((c) => [c.id, c.name]));

  return (
    <SectionCard
      title="Delivery log"
      icon={TaskDone01Icon}
      description="Last 200 attempts per channel; secrets are redacted from errors"
      actions={
        <Select
          value={channel}
          onValueChange={(v) => {
            setChannel(v);
            paged.setPage(1);
          }}
        >
          <SelectTrigger size="sm" aria-label="Channel" className="min-w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent align="end">
            <SelectItem value="all">All channels</SelectItem>
            {(channels.data?.channels ?? []).map((c) => (
              <SelectItem key={c.id} value={c.id}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
      flush
    >
      <DataTableFrame
        status={query}
        isEmpty={rows.length === 0}
        minWidthClassName="min-w-[860px]"
        aria-label="Delivery log"
        pagination={pager}
        empty={<EmptyState size="compact" icon={Rocket01Icon} title="Nothing delivered yet" description="Test sends and alert notifications are logged here with their outcome." />}
      >
        <colgroup>
          <col className="w-28" />
          <col className="w-32" />
          <col className="w-40" />
          <col />
          <col className="w-20" />
          <col className="w-36" />
        </colgroup>
        <thead className={dataTableClasses.thead}>
          <tr>
            <th className={dataTableClasses.th}>Time</th>
            <th className={dataTableClasses.th}>Status</th>
            <th className={dataTableClasses.th}>Channel</th>
            <th className={dataTableClasses.th}>Notification</th>
            <th className={cn(dataTableClasses.th, "text-right")}>Attempt</th>
            <th className={cn(dataTableClasses.th, "text-right")}>Result</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((d) => (
            <tr key={String(d.id)} className={dataTableClasses.tr}>
              <td className={cn(dataTableClasses.td, "font-mono text-small tabular-nums text-muted-foreground")} title={formatDateTime(d.tsMs)}>
                {formatRelativeTime(d.tsMs)}
              </td>
              <td className={dataTableClasses.td}>
                <DeliveryPill d={d} />
              </td>
              <td className={cn(dataTableClasses.td, "truncate")}>{names.get(d.channelId) ?? d.channelId}</td>
              <td className={cn(dataTableClasses.td, "py-2")}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-foreground">
                    <span className="text-muted-foreground">{d.notification} · </span>
                    {d.subject}
                  </span>
                  {d.error && <span className="truncate text-small text-status-critical" title={d.error}>{d.error}</span>}
                </span>
              </td>
              <td className={cn(dataTableClasses.td, "text-right font-mono tabular-nums")}>{d.attempt || "—"}</td>
              <td className={cn(dataTableClasses.td, "whitespace-nowrap text-right font-mono text-small tabular-nums text-muted-foreground")}>
                {d.httpStatus ? `HTTP ${d.httpStatus}` : d.status === "sent" ? "OK" : "—"}
                {d.durationMs > 0n ? ` · ${d.durationMs} ms` : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </DataTableFrame>
    </SectionCard>
  );
}
