import { Link } from "@tanstack/react-router";
import {
  ComputerTerminal01Icon,
  InformationCircleIcon,
  Loading03Icon,
  MoreHorizontalIcon,
  SidebarRight01Icon,
} from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ContainerAction, type Container } from "@/gen/asterism/v1/asterism_pb";
import { cn } from "@/lib/utils";
import { ACTION_META, ACTION_ORDER, containerName, validActions, type ContainerActionKind } from "./model";
import type { ContainerActionsApi } from "./useContainerActions";

function Spinner({ className }: { className?: string }) {
  return (
    <Icon
      icon={Loading03Icon}
      className={cn("animate-spin motion-reduce:animate-none", className)}
      aria-hidden={false}
      aria-label="Working"
    />
  );
}

function orderedActions(c: Container): ContainerActionKind[] {
  const valid = new Set(validActions(c.state));
  return ACTION_ORDER.filter((a) => valid.has(a));
}

export interface ContainerRowMenuProps {
  container: Container;
  actions: ContainerActionsApi;
  className?: string;
}

/**
 * Row overflow menu: the actions valid for the container's state (disabled
 * with the reason when actions are off or the container is protected), then
 * details and logs. The trigger shows a spinner while an action runs.
 */
export function ContainerRowMenu({ container: c, actions, className }: ContainerRowMenuProps) {
  const pending = actions.pending[c.id];
  const items = orderedActions(c);
  const blocked = actions.blockedReason(c);
  const name = containerName(c.id);

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={pending ? `${ACTION_META[pending].progress} ${c.name}` : `Actions for ${c.name}`}
          aria-busy={pending ? true : undefined}
        >
          {pending ? <Spinner /> : <Icon icon={MoreHorizontalIcon} />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="truncate font-mono">{c.name}</DropdownMenuLabel>
        {blocked && (
          <p className="flex gap-2 px-2.5 pb-2 text-small text-muted-foreground text-pretty">
            <Icon icon={InformationCircleIcon} className="mt-0.5 size-3.5 shrink-0 text-text-4" />
            <span>{blocked}</span>
          </p>
        )}
        {items.map((a) => {
          const meta = ACTION_META[a];
          const reason = actions.blockedReason(c, a);
          return (
            <DropdownMenuItem
              key={a}
              disabled={Boolean(reason) || Boolean(pending)}
              variant={a === ContainerAction.STOP ? "destructive" : "default"}
              onSelect={() => actions.request(c, a)}
            >
              <Icon icon={meta.icon} />
              {meta.label}
              {meta.confirm && <span className="ml-auto text-small text-text-4">Confirm</span>}
            </DropdownMenuItem>
          );
        })}
        {items.length > 0 && <DropdownMenuSeparator />}
        <DropdownMenuItem asChild>
          <Link to="/containers/$containerId" params={{ containerId: name }}>
            <Icon icon={SidebarRight01Icon} />
            View details
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/containers/$containerId" params={{ containerId: name }} search={{ tab: "logs" }}>
            <Icon icon={ComputerTerminal01Icon} />
            View logs
          </Link>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export interface ContainerActionBarProps {
  container: Container;
  actions: ContainerActionsApi;
  size?: "default" | "sm";
  className?: string;
}

/**
 * Header buttons for the actions valid in the current state. Disabled buttons
 * stay visible and explain why in a tooltip; the running action shows a
 * spinner in place of its icon.
 */
export function ContainerActionBar({ container: c, actions, size = "default", className }: ContainerActionBarProps) {
  const pending = actions.pending[c.id];
  const items = orderedActions(c);
  if (items.length === 0) return null;

  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      {items.map((a) => {
        const meta = ACTION_META[a];
        const reason = actions.blockedReason(c, a);
        const isPending = pending === a;
        const button = (
          <Button
            variant={a === ContainerAction.STOP ? "destructive" : "outline"}
            size={size}
            disabled={Boolean(reason) || Boolean(pending)}
            aria-busy={isPending || undefined}
            onClick={() => actions.request(c, a)}
          >
            {isPending ? <Spinner /> : <Icon icon={meta.icon} />}
            {isPending ? `${meta.progress}…` : meta.label}
          </Button>
        );
        if (!reason) return <span key={a}>{button}</span>;
        return (
          <Tooltip key={a}>
            <TooltipTrigger asChild>
              {/* Disabled buttons swallow pointer events; the wrapper keeps the tooltip reachable by mouse and keyboard. */}
              <span tabIndex={0} className="inline-flex outline-none focus-visible:ring-1 focus-visible:ring-ring" aria-label={`${meta.label} unavailable: ${reason}`}>
                {button}
              </span>
            </TooltipTrigger>
            <TooltipContent side="bottom" className="max-w-72">
              {reason}
            </TooltipContent>
          </Tooltip>
        );
      })}
    </div>
  );
}
