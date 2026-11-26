import { useCallback, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ContainerAction,
  type Capabilities,
  type Container,
  type ContainerActionResponse,
  type GetContainerResponse,
  type ListContainersResponse,
} from "@/gen/asterism/v1/asterism_pb";
import { actionsClient } from "@/lib/rpc";
import { formatDurationMs } from "@/lib/format";
import { ConfirmActionDialog, type ConfirmRequest } from "./ConfirmActionDialog";
import {
  ACTION_META,
  actionBlockedReason,
  actionErrorText,
  containerKeys,
  stateLabel,
  type ActionErrorText,
  type ContainerActionKind,
} from "./model";

function callAction(
  action: ContainerActionKind,
  id: string,
  timeoutSeconds?: number,
): Promise<ContainerActionResponse> {
  switch (action) {
    case ContainerAction.START:
      return actionsClient.startContainer({ id });
    case ContainerAction.STOP:
      return actionsClient.stopContainer({ id, timeoutSeconds });
    case ContainerAction.RESTART:
      return actionsClient.restartContainer({ id, timeoutSeconds });
    case ContainerAction.PAUSE:
      return actionsClient.pauseContainer({ id });
    case ContainerAction.UNPAUSE:
      return actionsClient.unpauseContainer({ id });
  }
}

export interface ContainerActionsApi {
  /** Runs Start/Pause/Unpause immediately; Stop/Restart open the confirmation first. */
  request: (container: Container, action: ContainerActionKind) => void;
  /** In-flight action per container entity id. */
  pending: Readonly<Record<string, ContainerActionKind>>;
  /** Last failure per container entity id (cleared by the next attempt). */
  errors: Readonly<Record<string, ActionErrorText>>;
  /** Why an action is unavailable for this container, if it is. */
  blockedReason: (container: Container, action?: ContainerActionKind) => string | undefined;
  /** Render once: the stop/restart confirmation dialog. */
  dialog: ReactNode;
}

/**
 * Container actions for one page: confirmation, per-row pending state, toasts
 * and cache updates. Nothing is optimistic: the row changes when the daemon
 * returns the container's post-action state, then the lists refetch.
 */
export function useContainerActions(capabilities: Capabilities | undefined): ContainerActionsApi {
  const qc = useQueryClient();
  const [pending, setPending] = useState<Record<string, ContainerActionKind>>({});
  const [errors, setErrors] = useState<Record<string, ActionErrorText>>({});
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);

  const applyResult = useCallback(
    (updated: Container) => {
      qc.setQueriesData<ListContainersResponse>({ queryKey: containerKeys.list() }, (old) =>
        old
          ? { ...old, containers: old.containers.map((c) => (c.id === updated.id ? updated : c)) }
          : old,
      );
      qc.setQueryData<GetContainerResponse>(containerKeys.detail(updated.name), (old) =>
        old ? { ...old, container: updated } : old,
      );
    },
    [qc],
  );

  const execute = useCallback(
    async (c: Container, action: ContainerActionKind, timeoutSeconds?: number) => {
      const meta = ACTION_META[action];
      setPending((p) => ({ ...p, [c.id]: action }));
      setErrors((e) => {
        if (!(c.id in e)) return e;
        const next = { ...e };
        delete next[c.id];
        return next;
      });
      try {
        const res = await callAction(action, c.id, timeoutSeconds);
        if (res.container) applyResult(res.container);
        const after = res.container ? stateLabel(res.container.state).toLowerCase() : undefined;
        toast.success(`${meta.past} ${c.name}`, {
          description: [
            after ? `Now ${after}` : undefined,
            `took ${formatDurationMs(res.durationMs)}`,
            "recorded in Events",
          ]
            .filter(Boolean)
            .join(" · "),
        });
      } catch (err) {
        const text = actionErrorText(err, action, c.name);
        setErrors((e) => ({ ...e, [c.id]: text }));
        toast.error(text.title, { description: text.description });
      } finally {
        setPending((p) => {
          const next = { ...p };
          delete next[c.id];
          return next;
        });
        void qc.invalidateQueries({ queryKey: containerKeys.all });
        void qc.invalidateQueries({ queryKey: ["events"] });
        void qc.invalidateQueries({ queryKey: ["overview"] });
        void qc.invalidateQueries({ queryKey: ["workload", c.id] });
      }
    },
    [applyResult, qc],
  );

  const request = useCallback(
    (c: Container, action: ContainerActionKind) => {
      if (ACTION_META[action].confirm) {
        setConfirm({ container: c, action });
        return;
      }
      void execute(c, action);
    },
    [execute],
  );

  const blockedReason = useCallback(
    (c: Container, action?: ContainerActionKind) => actionBlockedReason(c, capabilities, action),
    [capabilities],
  );

  const dialog = (
    <ConfirmActionDialog
      request={confirm}
      onCancel={() => setConfirm(null)}
      onConfirm={(timeoutSeconds) => {
        if (confirm) void execute(confirm.container, confirm.action, timeoutSeconds);
        setConfirm(null);
      }}
    />
  );

  return { request, pending, errors, blockedReason, dialog };
}
