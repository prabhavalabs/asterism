import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Delete02Icon, PlusSignIcon } from "@hugeicons/core-free-icons";
import {
  ChannelType,
  SmtpSecurity,
  type ChannelConfig,
  type NotificationChannel,
  type SecretStatus,
} from "@/gen/asterism/v1/alerts_pb";
import { notificationsClient } from "@/lib/alertsRpc";
import { SegmentedControl, errorMessage } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Field, ToggleRow } from "./Field";
import { CHANNEL_TYPE_LABELS, SEVERITY_OPTIONS, parseList, severityFromName, severityName, type SeverityValue } from "./model";

/** Types offered in the editor (Web Push is created by subscribing a browser). */
const EDITABLE_TYPES = [ChannelType.SLACK, ChannelType.WEBHOOK, ChannelType.EMAIL, ChannelType.DISCORD, ChannelType.TELEGRAM] as const;

export interface ChannelSheetProps {
  /** The channel to edit, "new" to create one, or null when closed. */
  channel: NotificationChannel | "new" | null;
  onOpenChange: (open: boolean) => void;
}

export function ChannelSheet({ channel, onOpenChange }: ChannelSheetProps) {
  return (
    <Sheet open={channel !== null} onOpenChange={onOpenChange}>
      <SheetContent className="sm:max-w-xl">
        {channel !== null && (
          <ChannelForm key={channel === "new" ? "new" : channel.id} existing={channel === "new" ? null : channel} onDone={() => onOpenChange(false)} />
        )}
      </SheetContent>
    </Sheet>
  );
}

interface HeaderRow {
  name: string;
  value: string;
  stored: boolean;
}

function secretOf(existing: NotificationChannel | null, field: string): SecretStatus | undefined {
  return existing?.secrets.find((s) => s.field === field);
}

/**
 * Password input for a write-only secret: empty keeps the stored value,
 * which is shown only as "•••• set" (plus a masked hint).
 */
function SecretInput({
  id,
  value,
  onChange,
  status,
  placeholder,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  status?: SecretStatus;
  placeholder?: string;
}) {
  const stored = status?.hasValue;
  return (
    <Input
      id={id}
      type="password"
      autoComplete="off"
      spellCheck={false}
      className="font-mono"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={stored ? `•••• set${status?.hint && status.hint !== "••••" ? ` (${status.hint})` : ""} — leave empty to keep` : placeholder}
    />
  );
}

function ChannelForm({ existing, onDone }: { existing: NotificationChannel | null; onDone: () => void }) {
  const qc = useQueryClient();
  const cfg = existing?.config?.kind;
  const [type, setType] = useState<ChannelType>(existing?.type ?? ChannelType.SLACK);
  const [name, setName] = useState(existing?.name ?? "");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [minSeverity, setMinSeverity] = useState<SeverityValue>(existing ? severityName(existing.minSeverity) : "warning");
  const [sendResolved, setSendResolved] = useState(existing?.sendResolved ?? true);
  const [rateLimit, setRateLimit] = useState(String(existing?.rateLimitPerHour ?? 60));
  const [kinds, setKinds] = useState(existing?.kinds.join(", ") ?? "");
  const [projects, setProjects] = useState(existing?.projects.join(", ") ?? "");

  // Secret fields start empty: the server never sends them back.
  const [url, setUrl] = useState("");
  const [signingSecret, setSigningSecret] = useState("");
  const [password, setPassword] = useState("");
  const [botToken, setBotToken] = useState("");
  const [clear, setClear] = useState<string[]>([]);
  const [headers, setHeaders] = useState<HeaderRow[]>(
    cfg?.case === "webhook" ? cfg.value.headerNames.map((n) => ({ name: n, value: "", stored: true })) : [],
  );

  const email = cfg?.case === "email" ? cfg.value : undefined;
  const [smtpHost, setSmtpHost] = useState(email?.host ?? "");
  const [smtpPort, setSmtpPort] = useState(String(email?.port || 587));
  const [security, setSecurity] = useState<SmtpSecurity>(email?.security || SmtpSecurity.STARTTLS);
  const [username, setUsername] = useState(email?.username ?? "");
  const [from, setFrom] = useState(email?.from ?? "");
  const [to, setTo] = useState(email?.to.join(", ") ?? "");
  const [chatId, setChatId] = useState(cfg?.case === "telegram" ? cfg.value.chatId : "");

  const buildConfig = (): ChannelConfig["kind"] => {
    switch (type) {
      case ChannelType.SLACK:
        return { case: "slack", value: { webhookUrl: url } } as ChannelConfig["kind"];
      case ChannelType.DISCORD:
        return { case: "discord", value: { webhookUrl: url } } as ChannelConfig["kind"];
      case ChannelType.WEBHOOK:
        return {
          case: "webhook",
          value: { url, signingSecret, headers: Object.fromEntries(headers.filter((h) => h.name.trim()).map((h) => [h.name.trim(), h.value])) },
        } as ChannelConfig["kind"];
      case ChannelType.EMAIL:
        return {
          case: "email",
          value: { host: smtpHost.trim(), port: Number(smtpPort) || 0, security, username: username.trim(), password, from: from.trim(), to: parseList(to) },
        } as ChannelConfig["kind"];
      case ChannelType.TELEGRAM:
        return { case: "telegram", value: { botToken, chatId: chatId.trim() } } as ChannelConfig["kind"];
      default:
        return { case: "webpush", value: {} } as ChannelConfig["kind"];
    }
  };

  const save = useMutation({
    mutationFn: async () => {
      const channel = {
        id: existing?.id ?? "",
        name: name.trim(),
        type,
        enabled,
        minSeverity: severityFromName(minSeverity),
        sendResolved,
        rateLimitPerHour: Math.max(0, Math.round(Number(rateLimit) || 0)),
        kinds: parseList(kinds),
        projects: parseList(projects),
        config: { kind: buildConfig() },
      };
      // Stored headers removed from the list are cleared explicitly (an
      // empty header map means "keep" on the server).
      const initial = cfg?.case === "webhook" ? cfg.value.headerNames : [];
      const kept = new Set(headers.filter((h) => h.stored).map((h) => h.name));
      const removed = initial.filter((n) => !kept.has(n)).map((n) => `headers.${n}`);
      return existing
        ? notificationsClient.updateChannel({ channel, clearSecrets: [...clear, ...removed] })
        : notificationsClient.createChannel({ channel });
    },
    onSuccess: (ch) => {
      toast.success(existing ? `Saved ${ch.name}` : `Created ${ch.name}`, { description: "Use Send test to check the channel." });
      void qc.invalidateQueries({ queryKey: ["notifications"] });
      onDone();
    },
    onError: (err) => toast.error("Could not save the channel", { description: errorMessage(err) }),
  });

  const isWebPush = type === ChannelType.WEBPUSH;
  const urlLabel = type === ChannelType.WEBHOOK ? "URL" : "Webhook URL";
  const urlPlaceholder =
    type === ChannelType.SLACK
      ? "https://hooks.slack.com/services/…"
      : type === ChannelType.DISCORD
        ? "https://discord.com/api/webhooks/…"
        : "https://example.com/hooks/asterism";
  const toggleClear = (field: string) => setClear((prev) => (prev.includes(field) ? prev.filter((f) => f !== field) : [...prev, field]));

  return (
    <form
      className="flex h-full min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <SheetHeader>
        <SheetTitle>{existing ? `Edit ${existing.name}` : "Add notification channel"}</SheetTitle>
        <SheetDescription>Secrets are write-only: they are encrypted at rest and never shown again.</SheetDescription>
      </SheetHeader>
      <SheetBody className="flex flex-col gap-4">
        {!existing && (
          <Field label="Type">
            {(id) => (
              <Select value={String(type)} onValueChange={(v) => setType(Number(v) as ChannelType)}>
                <SelectTrigger id={id} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EDITABLE_TYPES.map((t) => (
                    <SelectItem key={t} value={String(t)}>
                      {CHANNEL_TYPE_LABELS[t]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
        )}
        <Field label="Name">
          {(id) => <Input id={id} value={name} maxLength={80} onChange={(e) => setName(e.target.value)} placeholder={`${CHANNEL_TYPE_LABELS[type]} · on-call`} />}
        </Field>
        <ToggleRow label="Enabled" control={<Switch checked={enabled} onCheckedChange={setEnabled} aria-label="Channel enabled" />} />

        {(type === ChannelType.SLACK || type === ChannelType.DISCORD || type === ChannelType.WEBHOOK) && (
          <Field label={urlLabel} hint={type === ChannelType.WEBHOOK ? "https required; private and loopback addresses are refused unless allow_private_targets is on" : undefined}>
            {(id) => <SecretInput id={id} value={url} onChange={setUrl} status={secretOf(existing, type === ChannelType.WEBHOOK ? "url" : "webhook_url")} placeholder={urlPlaceholder} />}
          </Field>
        )}

        {type === ChannelType.WEBHOOK && (
          <>
            <Field label="Signing secret" hint="Optional, 16+ characters. Requests carry X-Asterism-Signature: t=…,v1=HMAC-SHA256(t.body).">
              {(id) => (
                <div className="flex gap-2">
                  <SecretInput id={id} value={signingSecret} onChange={setSigningSecret} status={secretOf(existing, "signing_secret")} />
                  {secretOf(existing, "signing_secret")?.hasValue && (
                    <Button type="button" variant={clear.includes("signing_secret") ? "destructive" : "outline"} onClick={() => toggleClear("signing_secret")}>
                      {clear.includes("signing_secret") ? "Will clear" : "Clear"}
                    </Button>
                  )}
                </div>
              )}
            </Field>
            <div className="flex flex-col gap-2">
              <span className="text-control font-medium text-foreground">Custom headers</span>
              {headers.map((h, i) => (
                <div key={i} className="flex gap-2">
                  <Input
                    aria-label="Header name"
                    className="w-48 shrink-0 font-mono"
                    value={h.name}
                    disabled={h.stored}
                    onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))}
                    placeholder="Authorization"
                  />
                  <Input
                    aria-label={`Value for ${h.name || "header"}`}
                    type="password"
                    autoComplete="off"
                    className="font-mono"
                    value={h.value}
                    onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                    placeholder={h.stored ? "•••• set — leave empty to keep" : "value"}
                  />
                  <Button type="button" variant="ghost" size="icon" aria-label={`Remove ${h.name || "header"}`} onClick={() => setHeaders(headers.filter((_, j) => j !== i))}>
                    <Icon icon={Delete02Icon} />
                  </Button>
                </div>
              ))}
              <Button type="button" variant="outline" size="sm" className="self-start" disabled={headers.length >= 10} onClick={() => setHeaders([...headers, { name: "", value: "", stored: false }])}>
                <Icon icon={PlusSignIcon} />
                Add header
              </Button>
            </div>
          </>
        )}

        {type === ChannelType.EMAIL && (
          <>
            <div className="grid grid-cols-[1fr_7rem] gap-3">
              <Field label="SMTP host">{(id) => <Input id={id} value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} placeholder="smtp.example.com" />}</Field>
              <Field label="Port">{(id) => <Input id={id} inputMode="numeric" className="tabular-nums" value={smtpPort} onChange={(e) => setSmtpPort(e.target.value)} />}</Field>
            </div>
            <Field label="Security">
              {() => (
                <SegmentedControl
                  aria-label="SMTP security"
                  fullWidth
                  value={String(security)}
                  onValueChange={(v) => setSecurity(Number(v) as SmtpSecurity)}
                  options={[
                    { value: String(SmtpSecurity.STARTTLS), label: "STARTTLS" },
                    { value: String(SmtpSecurity.TLS), label: "TLS" },
                    { value: String(SmtpSecurity.NONE), label: "None (loopback)" },
                  ]}
                />
              )}
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Username">{(id) => <Input id={id} autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />}</Field>
              <Field label="Password">{(id) => <SecretInput id={id} value={password} onChange={setPassword} status={secretOf(existing, "password")} />}</Field>
            </div>
            <Field label="From">{(id) => <Input id={id} value={from} onChange={(e) => setFrom(e.target.value)} placeholder="Asterism <alerts@example.com>" />}</Field>
            <Field label="To" hint="Comma separated, up to 20">
              {(id) => <Input id={id} value={to} onChange={(e) => setTo(e.target.value)} placeholder="ops@example.com" />}
            </Field>
          </>
        )}

        {type === ChannelType.TELEGRAM && (
          <>
            <Field label="Bot token">{(id) => <SecretInput id={id} value={botToken} onChange={setBotToken} status={secretOf(existing, "bot_token")} placeholder="123456:ABC…" />}</Field>
            <Field label="Chat id" hint="Numeric id or @channel">
              {(id) => <Input id={id} className="font-mono" value={chatId} onChange={(e) => setChatId(e.target.value)} />}
            </Field>
          </>
        )}

        {isWebPush && <p className="text-small text-text-4">Delivers to every browser that enabled notifications on this hub.</p>}

        <Field label="Minimum severity">
          {() => <SegmentedControl aria-label="Minimum severity" fullWidth value={minSeverity} onValueChange={setMinSeverity} options={SEVERITY_OPTIONS} />}
        </Field>
        <ToggleRow label="Notify on resolve" hint="Also send when an alert stops firing" control={<Switch checked={sendResolved} onCheckedChange={setSendResolved} aria-label="Notify on resolve" />} />
        <div className="grid grid-cols-2 gap-3">
          <Field label="Rate limit per hour" hint="0 = unlimited">
            {(id) => <Input id={id} inputMode="numeric" className="tabular-nums" value={rateLimit} onChange={(e) => setRateLimit(e.target.value)} />}
          </Field>
          <Field label="Projects" hint="Empty = all, including the host">
            {(id) => <Input id={id} className="font-mono" value={projects} onChange={(e) => setProjects(e.target.value)} placeholder="shop, blog" />}
          </Field>
        </div>
        <Field label="Rules or event kinds" hint="Empty = all. Rule ids (disk_usage) or forwarded event kinds (container.*).">
          {(id) => <Input id={id} className="font-mono" value={kinds} onChange={(e) => setKinds(e.target.value)} />}
        </Field>
      </SheetBody>
      <SheetFooter>
        <Button type="button" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={save.isPending || name.trim() === ""}>
          {save.isPending ? "Saving…" : existing ? "Save channel" : "Create channel"}
        </Button>
      </SheetFooter>
    </form>
  );
}
