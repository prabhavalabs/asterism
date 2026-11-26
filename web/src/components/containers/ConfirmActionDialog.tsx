import { useState } from "react";
import { Icon } from "@/components/ui/icon";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogBody,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { SegmentedControl } from "@/components/common";
import { ContainerAction, type Container } from "@/gen/asterism/v1/asterism_pb";
import { ACTION_META, DEFAULT_STOP_TIMEOUT, STOP_TIMEOUTS, shortImage, stateLabel, type ContainerActionKind } from "./model";

export interface ConfirmRequest {
  container: Container;
  action: ContainerActionKind;
}

export interface ConfirmActionDialogProps {
  request: ConfirmRequest | null;
  onCancel: () => void;
  onConfirm: (timeoutSeconds: number) => void;
}

const TIMEOUT_OPTIONS = STOP_TIMEOUTS.map((s) => ({ value: String(s), label: `${s}s` }));

/** Names the container and lets the operator pick the graceful-stop timeout. */
export function ConfirmActionDialog({ request, onCancel, onConfirm }: ConfirmActionDialogProps) {
  return (
    <AlertDialog open={request !== null} onOpenChange={(open) => !open && onCancel()}>
      {request && <ConfirmBody key={`${request.container.id}:${request.action}`} request={request} onConfirm={onConfirm} />}
    </AlertDialog>
  );
}

function ConfirmBody({ request, onConfirm }: { request: ConfirmRequest; onConfirm: (timeoutSeconds: number) => void }) {
  const [timeoutValue, setTimeoutValue] = useState(String(DEFAULT_STOP_TIMEOUT));
  const { container: c, action } = request;
  const meta = ACTION_META[action];
  const isStop = action === ContainerAction.STOP;

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>
          {meta.label} <span className="font-mono">{c.name}</span>?
        </AlertDialogTitle>
        <AlertDialogDescription>
          {isStop
            ? "Docker sends SIGTERM, waits for the timeout below, then sends SIGKILL. The container stays stopped until someone starts it again."
            : "Docker stops the container (SIGTERM, then SIGKILL after the timeout) and starts it again. Open connections are dropped."}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogBody>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 rounded-tile bg-surface-subtle px-3 py-2.5 text-control ring-1 ring-inset ring-border">
          <dt className="text-muted-foreground">Image</dt>
          <dd className="truncate font-mono text-mono text-foreground" title={c.image}>
            {shortImage(c)}
          </dd>
          <dt className="text-muted-foreground">State</dt>
          <dd className="text-foreground">{stateLabel(c.state)}</dd>
          {c.composeProject && (
            <>
              <dt className="text-muted-foreground">Project</dt>
              <dd className="truncate text-foreground">
                {c.composeProject}
                {c.composeService ? ` / ${c.composeService}` : ""}
              </dd>
            </>
          )}
        </dl>
        <div className="flex flex-col gap-1.5">
          <span className="text-small font-medium text-muted-foreground">
            Graceful stop timeout
          </span>
          <SegmentedControl
            aria-label="Graceful stop timeout"
            value={timeoutValue}
            onValueChange={setTimeoutValue}
            options={TIMEOUT_OPTIONS}
          />
        </div>
        <p className="text-small text-muted-foreground">The action is recorded in Events with your name and the result.</p>
      </AlertDialogBody>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction variant={isStop ? "destructive" : "default"} onClick={() => onConfirm(Number(timeoutValue))}>
          <Icon icon={meta.icon} />
          {meta.label} {c.name}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}
