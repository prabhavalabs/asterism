import { useEffect, useRef } from "react";
import { Link, useSearch } from "@tanstack/react-router";
import { ArrowRight01Icon, ComputerIcon, ComputerTerminal01Icon, Logout01Icon, Mail01Icon, Moon02Icon, PaintBoardIcon, Plug01Icon, SecurityCheckIcon, ServerStack01Icon, Sun03Icon } from "@hugeicons/core-free-icons";
import { Icon, type IconData } from "@/components/ui/icon";
import { ScrollArea } from "@/components/ui/scroll-area";
import type { HostInfo } from "@/gen/asterism/v1/asterism_pb";
import {
  CopyButton,
  CardGrid,
  CopyValue,
  Page,
  PageHeader,
  PropertyRow,
  SectionCard,
  SegmentedControl,
  categoryLabel,
} from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { AgentsCard } from "@/components/agents/AgentsCard";
import { agentChipText, primaryAgent } from "@/components/agents/model";
import { useAgents } from "@/components/agents/useAgents";
import { useTheme, type ThemePreference } from "@/lib/theme";
import type { SettingsSearch, SettingsSection } from "@/lib/routeSearch";
import { cn } from "@/lib/utils";

const THEME_OPTIONS = [
  { value: "light", label: "Light", icon: Sun03Icon },
  { value: "dark", label: "Dark", icon: Moon02Icon },
  { value: "system", label: "System", icon: ComputerIcon },
] as const satisfies readonly { value: ThemePreference; label: string; icon: IconData }[];

const TOKEN_COMMAND = "asterism token generate --name ci --scope read";

const DEPLOY_SNIPPET = `# Recommended: agent mode. Enroll each VPS, then install asterism-agent on it
asterism agent token create --host prod-1

# Or a read-only SSH bridge from your laptop to the VPS
asterism serve --ssh user@vps.example.com

# Argon2id hash for admin_password in asterism.toml
asterism hash-password "your-production-password"`;

interface SettingsPageProps {
  host?: HostInfo;
  /** Capabilities.source_mode ("agent", "ssh", "local", "showcase"). */
  sourceMode?: string;
  userEmail?: string;
  onLogout: () => void;
}

function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}

/**
 * `?section=mcp` (the sidebar's "API & MCP") scrolls that card into view and
 * outlines it; the other sections can be linked the same way.
 */
function useSectionTarget(section: SettingsSection | undefined) {
  const refs = useRef(new Map<SettingsSection, HTMLElement>());
  useEffect(() => {
    if (!section) return;
    const el = refs.current.get(section);
    if (!el) return;
    el.scrollIntoView({ behavior: prefersReducedMotion() ? "auto" : "smooth", block: "start" });
  }, [section]);
  return (id: SettingsSection) => ({
    id: `settings-${id}`,
    ref: (el: HTMLElement | null) => {
      if (el) refs.current.set(id, el);
      else refs.current.delete(id);
    },
    "data-targeted": section === id ? "" : undefined,
    className: cn("scroll-mt-16", section === id && "ring-2 ring-ring/45"),
  });
}

export function SettingsPage({ host, sourceMode, userEmail, onLogout }: SettingsPageProps) {
  const { preference, setPreference } = useTheme();
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  const { section } = useSearch({ strict: false }) as SettingsSearch;
  const target = useSectionTarget(section);
  const mode = sourceMode || host?.sourceMode || "local";
  const agentsQuery = useAgents(mode === "agent");
  const agent = mode === "agent" ? primaryAgent(agentsQuery.data) : undefined;

  return (
    <Page>
      <PageHeader title="Settings" description="Appearance, your session, the collector and how agents connect." />

      {/* Appearance and Notifications are one row each, so the pair ends level. */}
      <CardGrid gap={5} breakpoint="xl">
        <SectionCard title="Appearance" icon={PaintBoardIcon} flush {...target("appearance")}>
          <PropertyRow label="Theme" hint="System follows your OS setting">
            <SegmentedControl
              size="sm"
              aria-label="Theme"
              value={preference}
              onValueChange={setPreference}
              options={THEME_OPTIONS}
            />
          </PropertyRow>
        </SectionCard>

        <SectionCard title="Notifications" icon={Mail01Icon} flush>
          <PropertyRow label="Alert channels" hint="Slack, webhooks, email and browser push">
            <Button variant="outline" size="sm" asChild>
              <Link to="/settings/notifications">
                Manage
                <Icon icon={ArrowRight01Icon} />
              </Link>
            </Button>
          </PropertyRow>
        </SectionCard>
      </CardGrid>

      {/* Session and host have the same number of rows, so the pair ends level. */}
      <CardGrid gap={5} breakpoint="xl">
        <SectionCard title="Session" icon={SecurityCheckIcon} flush {...target("session")}>
          <div className="divide-y divide-divider">
            <PropertyRow label="Signed in as" mono>
              {userEmail || "Administrator"}
            </PropertyRow>
            <PropertyRow label="Session cookie" hint="Password verified with Argon2id">
              <Badge variant="outline" mono>
                HttpOnly · SameSite=Strict
              </Badge>
            </PropertyRow>
            <PropertyRow label="Sign out" hint="Ends the session in this browser">
              <Button variant="outline" size="sm" onClick={onLogout}>
                <Icon icon={Logout01Icon} />
                Sign out
              </Button>
            </PropertyRow>
          </div>
        </SectionCard>

        <SectionCard title="Host and collector" icon={ServerStack01Icon} flush {...target("host")}>
          <div className="divide-y divide-divider">
            <PropertyRow label="Host" mono>
              {host?.hostname || "—"}
            </PropertyRow>
            <PropertyRow label="Platform" mono>
              {host ? `${host.os} · ${host.arch}` : "—"}
            </PropertyRow>
            <PropertyRow label="Collector mode">
              <Badge variant="outline">{categoryLabel(mode)}</Badge>
            </PropertyRow>
            {mode === "agent" ? (
              <PropertyRow label="Source" hint="Primary agent feeding this view" mono>
                {agent ? agentChipText(agent) : <span className="font-sans text-control text-muted-foreground">No agent enrolled</span>}
              </PropertyRow>
            ) : (
              <PropertyRow label="SSH target" mono>
                {host?.sshTarget || <span className="font-sans text-control text-muted-foreground">Not set (reads this machine)</span>}
              </PropertyRow>
            )}
          </div>
        </SectionCard>
      </CardGrid>

      <SectionCard
        title="API & MCP"
        icon={Plug01Icon}
        description="Query this observatory from agents, the CLI and scripts"
        flush
        {...target("mcp")}
      >
        <div className="divide-y divide-divider">
          <PropertyRow label="MCP, local (stdio)">
            <CopyValue value="asterism mcp" />
          </PropertyRow>
          <PropertyRow label="MCP, Streamable HTTP">
            <CopyValue value={`${origin}/mcp`} />
          </PropertyRow>
          <PropertyRow label="Connect RPC API" hint="Same data as the web UI">
            <CopyValue value={`${origin}/asterism.v1.*`} />
          </PropertyRow>
          <PropertyRow label="Bearer token" hint="Paste the printed entry into [[auth.tokens]]">
            <CopyValue value={TOKEN_COMMAND} />
          </PropertyRow>
        </div>
      </SectionCard>

      <AgentsCard sourceMode={mode} hubUrl={origin || "https://asterism.example.com"} target={target("agents")} />

      <SectionCard
        title="Deployment"
        icon={ComputerTerminal01Icon}
        description="Run against a remote VPS or harden a production install"
        bodyClassName="flex flex-col gap-3"
        actions={<CopyButton value={DEPLOY_SNIPPET} label="Copy commands" />}
        {...target("deployment")}
      >
        <ScrollArea
          orientation="horizontal"
          className="rounded-tile bg-surface-subtle ring-1 ring-inset ring-border"
          viewportProps={{ tabIndex: 0, "aria-label": "Deployment commands" }}
        >
          <pre className="p-4 font-mono text-mono leading-6 text-foreground">{DEPLOY_SNIPPET}</pre>
        </ScrollArea>
        <p className="text-small text-muted-foreground">
          In production, bind to <code className="font-mono">127.0.0.1:7700</code> behind a TLS proxy and set{" "}
          <code className="font-mono">behind_tls = true</code>.
        </p>
      </SectionCard>
    </Page>
  );
}
