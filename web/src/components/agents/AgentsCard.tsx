import { useId, useState, type ReactNode } from "react";
import { Add01Icon, Alert02Icon, ArrowUp01Icon, Satellite01Icon } from "@hugeicons/core-free-icons";
import {
  CodeBlock,
  DataTableFrame,
  EmptyState,
  SectionCard,
  SegmentedControl,
  StatusPill,
  dataTableClasses,
  type SectionCardProps,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { AgentStatus } from "@/gen/asterism/v1/agent_pb";
import { usePagedSearch } from "@/hooks/use-paged-search";
import { formatBytes, formatDateTime, formatPct, formatUptime } from "@/lib/format";
import { paginateLocal } from "@/lib/pagination";
import { AGENTS_LIST } from "@/lib/routeSearch";
import { cn } from "@/lib/utils";
import {
  actionLabel,
  agentState,
  agentStateStyle,
  connectedCount,
  displayCapabilities,
  formatLag,
  formatSkew,
  lagWarning,
  lastSeenText,
  primaryAgent,
  skewWarning,
} from "./model";
import { useAgents } from "./useAgents";

const th = dataTableClasses.th;
const td = dataTableClasses.td;
const MAX_CAP_CHIPS = 3;

/** Docs anchor for the agent install guide in the public README. */
const AGENT_DOCS_URL = "https://github.com/prabhavalabs/asterism#install-the-agent";

type CardTarget = Pick<SectionCardProps, "id" | "className"> & {
  ref?: (el: HTMLElement | null) => void;
  "data-targeted"?: string;
};

export interface AgentsCardProps {
  /** Capabilities.source_mode of this hub. */
  sourceMode?: string;
  /** Hub URL the agents should dial (this page's origin). */
  hubUrl: string;
  /** Deep-link props from Settings (`?section=agents`). */
  target: CardTarget;
}

/**
 * Settings → Agents. In agent mode (or whenever agents are enrolled): a
 * table of every enrolled asterism-agent (status, version, transport, lag
 * and clock skew, inventory version, dropped and rejected frames, actions,
 * capabilities) with an "Add agent" panel. In other modes, a short
 * explainer for switching to agent mode.
 */
export function AgentsCard({ sourceMode, hubUrl, target }: AgentsCardProps) {
  const query = useAgents(true);
  const agents = query.data?.agents ?? [];
  const agentMode = sourceMode === "agent";

  if (!agentMode && agents.length === 0) {
    return <AgentModeExplainer sourceMode={sourceMode} target={target} />;
  }
  return <AgentsTableCard agents={agents} query={query} hubUrl={hubUrl} target={target} />;
}

function AgentsTableCard({
  agents,
  query,
  hubUrl,
  target,
}: {
  agents: readonly AgentStatus[];
  query: ReturnType<typeof useAgents>;
  hubUrl: string;
  target: CardTarget;
}) {
  const paged = usePagedSearch(AGENTS_LIST);
  const page = paginateLocal(agents, paged.page, paged.pageSize);
  const primary = primaryAgent(query.data);
  const connected = connectedCount(agents);
  const noneConnected = query.data !== undefined && agents.every((a) => agentState(a) === "never");
  const [enrollOpen, setEnrollOpen] = useState<boolean | undefined>(undefined);
  const open = enrollOpen ?? noneConnected;
  const panelId = useId();

  return (
    <SectionCard
      title="Agents"
      icon={Satellite01Icon}
      description={
        query.data ? (
          <span className="tabular-nums">
            {connected} of {agents.length} connected
            {primary ? ` · primary ${primary.name || primary.hostId}` : ""}
          </span>
        ) : (
          "asterism-agent processes pushing to this hub"
        )
      }
      flush
      actions={
        <Button variant="outline" size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setEnrollOpen(!open)}>
          <Icon icon={open ? ArrowUp01Icon : Add01Icon} />
          {open ? "Hide setup" : "Add agent"}
        </Button>
      }
      {...target}
    >
      <DataTableFrame
        status={query}
        isEmpty={agents.length === 0}
        empty={
          <EmptyState
            size="compact"
            icon={Satellite01Icon}
            title="No agents enrolled"
            description="Create a token below, add the printed [[agents]] entry to asterism.toml and restart the hub."
          />
        }
        minWidthClassName="min-w-[1260px]"
        aria-label="Enrolled agents"
        pagination={{
          ...page.pageInfo,
          onPageChange: paged.setPage,
          onPageSizeChange: paged.setPageSize,
          pageSizeOptions: paged.pageSizeOptions,
          noun: ["agent", "agents"],
        }}
      >
        <thead className={dataTableClasses.thead}>
          <tr>
            <th scope="col" className={cn(th, "w-36 pl-4")}>Agent</th>
            <th scope="col" className={cn(th, "w-44")}>Status</th>
            <th scope="col" className={cn(th, "w-36")}>Version · transport</th>
            <th scope="col" className={cn(th, "w-20 text-right")}>Lag</th>
            <th scope="col" className={cn(th, "w-24 text-right")}>Inventory</th>
            <th scope="col" className={cn(th, "w-28 text-right")} title="Frames the agent dropped from its buffer / frames the hub refused">
              Drop / reject
            </th>
            <th scope="col" className={cn(th, "w-36 text-right")}>RSS · CPU</th>
            <th scope="col" className={cn(th, "w-40")}>Actions</th>
            <th scope="col" className={cn(th, "pr-4")}>Capabilities</th>
          </tr>
        </thead>
        <tbody>
          {page.items.map((a) => (
            <AgentRow key={a.hostId || a.name} agent={a} primary={primary === a} />
          ))}
        </tbody>
      </DataTableFrame>

      {open && (
        <div id={panelId} className="border-t border-divider">
          <EnrollPanel hubUrl={hubUrl} />
        </div>
      )}
    </SectionCard>
  );
}

function Hint({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} aria-label={label} className={cn("inline-flex cursor-default rounded-xs outline-none focus-visible:ring-1 focus-visible:ring-ring", className)}>
          {children}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-80">{label}</TooltipContent>
    </Tooltip>
  );
}

function Dash() {
  return <span className="text-text-4">—</span>;
}

function AgentRow({ agent: a, primary }: { agent: AgentStatus; primary: boolean }) {
  const state = agentState(a);
  const style = agentStateStyle(a);
  const live = state === "connected";
  const upSec = Math.max(0, Math.floor((Date.now() - Number(a.connectedSinceMs)) / 1000));
  const seen = state === "never" ? undefined : live ? `for ${upSec < 60 ? `${upSec}s` : formatUptime(upSec)}` : lastSeenText(a);
  const stats = a.stats;
  const dropped = Number(stats?.framesDropped ?? 0n);
  const rejected = Number(a.framesRejected);
  const caps = displayCapabilities(a.capabilities);

  return (
    <tr className={dataTableClasses.tr}>
      <td className={cn(td, "pl-4")}>
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate font-mono text-mono text-foreground" title={a.remoteAddr ? `${a.name} · ${a.remoteAddr}` : a.name}>
            {a.name || a.hostId}
          </span>
          {primary && <Badge variant="info">Primary</Badge>}
        </span>
      </td>
      <td className={td}>
        <span className="flex min-w-0 items-center gap-2">
          <StatusPill tone={style.tone} pulse={live}>
            {style.label}
          </StatusPill>
          {seen && (
            <span
              className="truncate text-small text-muted-foreground tabular-nums"
              title={a.lastSeenMs > 0n ? `Last seen ${formatDateTime(a.lastSeenMs)}` : undefined}
            >
              {seen}
            </span>
          )}
          {a.lastError && (
            <Hint label={`Last error: ${a.lastError}`}>
              <Icon icon={Alert02Icon} className="size-4 text-status-critical" />
            </Hint>
          )}
        </span>
      </td>
      <td className={td}>
        {a.agentVersion || a.transport ? (
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-mono text-text-2" title={a.configHash ? `config ${a.configHash}` : undefined}>
              {a.agentVersion || "—"}
            </span>
            {a.transport && (
              <Badge
                variant="outline"
                mono
                title={a.transport === "unary" ? "HTTP/1.1 fallback: unary Push plus a Commands stream" : "HTTP/2 bidirectional stream"}
              >
                {a.transport}
              </Badge>
            )}
          </span>
        ) : (
          <Dash />
        )}
      </td>
      <td className={cn(td, "text-right font-mono text-mono tabular-nums")}>
        {live ? (
          <span className="inline-flex items-center justify-end gap-1.5">
            {skewWarning(a) && (
              <Hint label={`Clock skew ${formatSkew(a.clockSkewMs)}: the hub rewrites this agent's timestamps. Run NTP (chrony or systemd-timesyncd) on the host.`}>
                <Icon icon={Alert02Icon} className="size-4 text-status-warning" />
              </Hint>
            )}
            <span className={lagWarning(a) ? "text-status-warning" : "text-foreground"}>{formatLag(a.lagMs)}</span>
          </span>
        ) : (
          <Dash />
        )}
      </td>
      <td className={cn(td, "text-right font-mono text-mono text-text-2 tabular-nums")}>
        {a.inventoryVersion > 0n ? `v${a.inventoryVersion}` : <Dash />}
      </td>
      <td className={cn(td, "text-right font-mono text-mono tabular-nums")}>
        {state === "never" ? (
          <Dash />
        ) : (
          <>
            <span className={dropped > 0 ? "text-status-warning" : "text-text-2"}>{dropped.toLocaleString()}</span>
            <span className="text-text-4"> / </span>
            <span className={rejected > 0 ? "text-status-critical" : "text-text-2"}>{rejected.toLocaleString()}</span>
          </>
        )}
      </td>
      <td className={cn(td, "text-right font-mono text-mono text-text-2 tabular-nums")}>
        {stats && stats.rssBytes > 0n ? `${formatBytes(stats.rssBytes)} · ${formatPct(stats.cpuUtil)}` : <Dash />}
      </td>
      <td className={td}>
        <AgentActions agent={a} />
      </td>
      <td className={cn(td, "max-w-0 pr-4")}>
        {caps.length === 0 ? (
          <Dash />
        ) : (
          <span className="flex min-w-0 items-center gap-1 overflow-hidden">
            {caps.slice(0, MAX_CAP_CHIPS).map((c) => (
              <Badge key={c} variant="secondary" mono>
                {c}
              </Badge>
            ))}
            {caps.length > MAX_CAP_CHIPS && (
              <Hint label={caps.join(", ")}>
                <span className="text-small text-muted-foreground tabular-nums">+{caps.length - MAX_CAP_CHIPS}</span>
              </Hint>
            )}
          </span>
        )}
      </td>
    </tr>
  );
}

/** Both gates: the agent's own allow-list (from Hello) and the hub's per-agent switch. */
function AgentActions({ agent: a }: { agent: AgentStatus }) {
  if (agentState(a) === "never") return <Dash />;
  const allowed = a.allowedActions.map(actionLabel);
  if (allowed.length === 0) {
    return (
      <Hint label="Read-only: actions.enabled is false (or actions.allow is empty) in this agent's agent.yaml.">
        <span className="text-text-4">Off</span>
      </Hint>
    );
  }
  if (!a.actionsEnabled) {
    return (
      <Hint label={`The agent allows ${allowed.join(", ")}, but the hub gate is off: set allow_actions = true on this [[agents]] entry.`}>
        <Badge variant="outline">Hub gate off</Badge>
      </Hint>
    );
  }
  return (
    <span className="flex min-w-0 items-center gap-1 overflow-hidden">
      {allowed.map((label) => (
        <Badge key={label} variant="healthy">
          {label}
        </Badge>
      ))}
    </span>
  );
}

// --- Enrollment ---------------------------------------------------------------

const TOKEN_COMMAND = "asterism agent token create --host prod-1";

const HUB_TOML = `[collect]
mode = "agent"

[[agents]]
name = "prod-1"
sha256 = "<digest printed by token create>"
machine_id = ""          # optional pin
allow_actions = false    # hub-side per-agent gate`;

/** Files and names below match deploy/agent/ (install.sh, asterism-agent.service, docker-compose.agent.yml). */
const RAW_DEPLOY_URL = "https://raw.githubusercontent.com/prabhavalabs/asterism/main/deploy/agent";

function installCommand(hubUrl: string): string {
  return `# On the VPS, with the token saved to ./agent.token
curl -fsSLo install.sh https://github.com/prabhavalabs/asterism/releases/latest/download/install.sh
sudo sh install.sh --hub ${hubUrl} --token-file ./agent.token --host prod-1
systemctl status asterism-agent`;
}

const COMPOSE_COMMANDS = `curl -fsSLO ${RAW_DEPLOY_URL}/docker-compose.agent.yml
curl -fsSLo agent.yaml ${RAW_DEPLOY_URL}/examples/agent.compose.yaml
install -m 0640 /dev/stdin ./agent.token   # paste the asta_ token, then Ctrl-D
docker compose -f docker-compose.agent.yml up -d`;

function agentYaml(hubUrl: string): string {
  return `# agent.yaml (token file, host root and Docker endpoint come from the container)
version: 1
hub:
  url: ${hubUrl}
host:
  name: prod-1          # must match --host`;
}

const INSTALL_OPTIONS = [
  { value: "systemd", label: "systemd" },
  { value: "docker", label: "Docker" },
] as const;

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex min-w-0 gap-3">
      <span
        aria-hidden="true"
        className="flex size-6 shrink-0 items-center justify-center rounded-avatar bg-surface-subtle font-heading text-small font-semibold text-text-2 tabular-nums ring-1 ring-inset ring-border"
      >
        {n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <p className="pt-0.5 text-control font-medium text-foreground">{title}</p>
        {children}
      </div>
    </li>
  );
}

function Note({ children }: { children: ReactNode }) {
  return <p className="text-small text-muted-foreground text-pretty">{children}</p>;
}

function Path({ children }: { children: ReactNode }) {
  return <code className="font-mono text-text-2">{children}</code>;
}

function EnrollPanel({ hubUrl }: { hubUrl: string }) {
  const [install, setInstall] = useState<(typeof INSTALL_OPTIONS)[number]["value"]>("systemd");

  return (
    <div className="flex flex-col gap-4 p-4">
      <ol className="flex flex-col gap-5">
        <Step n={1} title="Create a token on the hub">
          <CodeBlock value={TOKEN_COMMAND} label="Token command" />
          <Note>
            It prints the <Path>asta_…</Path> token once and the <Path>[[agents]]</Path> entry. Only the digest is stored. Add the
            entry to <Path>asterism.toml</Path> and restart Asterism:
          </Note>
          <CodeBlock value={HUB_TOML} label="Hub configuration" />
        </Step>

        <Step n={2} title="Install the agent on the VPS">
          <SegmentedControl size="sm" aria-label="Install method" value={install} onValueChange={setInstall} options={INSTALL_OPTIONS} className="self-start" />
          {install === "systemd" ? (
            <>
              <CodeBlock value={installCommand(hubUrl)} label="Install commands" />
              <Note>
                <Path>install.sh</Path> checks the release checksum, creates the <Path>asterism-agent</Path> user, copies the token
                to <Path>/etc/asterism/agent.token</Path>, writes <Path>/etc/asterism/agent.yaml</Path> if absent and installs the
                hardened <Path>asterism-agent.service</Path> unit. The token is read from a file, never from the command line; add{" "}
                <Path>--dry-run</Path> to print every step first.
              </Note>
            </>
          ) : (
            <>
              <CodeBlock value={COMPOSE_COMMANDS} label="Compose commands" />
              <CodeBlock value={agentYaml(hubUrl)} label="Agent configuration" />
              <Note>
                <Path>docker-compose.agent.yml</Path> runs <Path>ghcr.io/prabhavalabs/asterism-agent</Path> as uid 65532 with a
                read-only root filesystem, behind a GET-only Docker socket proxy, so the agent never holds the raw Docker socket.
                Set <Path>DOCKER_GID</Path> if the host&apos;s docker group is not 999.
              </Note>
            </>
          )}
        </Step>

        <Step n={3} title="Check it">
          <CodeBlock value="asterism-agent validate --config /etc/asterism/agent.yaml" label="Validate command" />
          <Note>The agent shows up in the table above as connected within a few seconds.</Note>
        </Step>
      </ol>

      <p className="rounded-tile bg-surface-subtle px-3 py-2.5 text-small text-text-2 text-pretty ring-1 ring-inset ring-border">
        Outbound only: the agent dials this hub and never listens on a port. Its token can push data but read nothing. Container
        actions stay off unless both <Path>agent.yaml</Path> and the hub allow them.{" "}
        <a href={AGENT_DOCS_URL} target="_blank" rel="noreferrer" className="rounded-xs font-medium text-primary underline-offset-4 outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring">
          Agent guide
        </a>
      </p>
    </div>
  );
}

function AgentModeExplainer({ sourceMode, target }: { sourceMode?: string; target: CardTarget }) {
  return (
    <SectionCard title="Agents" icon={Satellite01Icon} description="Switch to agent mode · recommended for new installs" bodyClassName="flex flex-col gap-3" {...target}>
      <p className="text-control text-text-2 text-pretty">
        This hub runs in <span className="font-medium text-foreground">{sourceMode || "local"}</span> mode. In agent mode a small{" "}
        <Path>asterism-agent</Path> on each VPS pushes telemetry out to this hub, so the hub needs no SSH key and the VPS opens no
        port.
      </p>
      <CodeBlock value={`${TOKEN_COMMAND}\n\n${HUB_TOML}`} label="Agent mode setup" />
      <Note>
        Restart Asterism after editing <Path>asterism.toml</Path>; this card then lists the agents and how to install them.{" "}
        <a href={AGENT_DOCS_URL} target="_blank" rel="noreferrer" className="rounded-xs font-medium text-primary underline-offset-4 outline-none hover:underline focus-visible:ring-1 focus-visible:ring-ring">
          Agent guide
        </a>
      </Note>
    </SectionCard>
  );
}
