import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Alert } from "@/gen/asterism/v1/alerts_pb";
import { alertsClient } from "@/lib/alertsRpc";
import { errorMessage } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { DurationSelect, Field, ToggleRow } from "./Field";

export interface SilenceSheetProps {
  /** Prefill matchers from an alert; null closes the sheet. */
  alert: Alert | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Mute alerts by rule, entity and/or project for a while, with a reason. */
export function SilenceSheet({ alert, open, onOpenChange }: SilenceSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        {open && <SilenceForm key={alert?.id ?? "new"} alert={alert} onDone={() => onOpenChange(false)} />}
      </SheetContent>
    </Sheet>
  );
}

function SilenceForm({ alert, onDone }: { alert: Alert | null; onDone: () => void }) {
  const qc = useQueryClient();
  const [useRule, setUseRule] = useState(Boolean(alert));
  const [useEntity, setUseEntity] = useState(Boolean(alert));
  const [useProject, setUseProject] = useState(false);
  const [ruleId, setRuleId] = useState(alert?.ruleId ?? "");
  const [entityId, setEntityId] = useState(alert?.entityId ?? "");
  const [projectId, setProjectId] = useState(alert?.projectId ?? "");
  const [duration, setDuration] = useState(3600);
  const [reason, setReason] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      alertsClient.createSilence({
        ruleId: useRule ? ruleId.trim() : "",
        entityId: useEntity ? entityId.trim() : "",
        projectId: useProject ? projectId.trim() : "",
        durationSeconds: duration,
        reason: reason.trim(),
      }),
    onSuccess: () => {
      toast.success("Silence created");
      void qc.invalidateQueries({ queryKey: ["alerts"] });
      onDone();
    },
    onError: (err) => toast.error("Could not create the silence", { description: errorMessage(err) }),
  });
  const hasMatcher = (useRule && ruleId.trim()) || (useEntity && entityId.trim()) || (useProject && projectId.trim());
  const valid = Boolean(hasMatcher) && reason.trim().length > 0;

  return (
    <form
      className="flex h-full min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) mutation.mutate();
      }}
    >
      <SheetHeader>
        <SheetTitle>Silence alerts</SheetTitle>
        <SheetDescription>Matching alerts keep their state but send no notifications until the silence ends.</SheetDescription>
      </SheetHeader>
      <SheetBody className="flex flex-col gap-4">
        <ToggleRow label="Rule" hint={useRule ? undefined : "Any rule"} control={<Switch checked={useRule} onCheckedChange={setUseRule} aria-label="Match a rule" />} />
        {useRule && (
          <Field label="Rule id" hint="e.g. disk_usage">
            {(id) => <Input id={id} value={ruleId} onChange={(e) => setRuleId(e.target.value)} className="font-mono" />}
          </Field>
        )}
        <ToggleRow label="Entity" hint={useEntity ? undefined : "Any entity"} control={<Switch checked={useEntity} onCheckedChange={setUseEntity} aria-label="Match an entity" />} />
        {useEntity && (
          <Field label="Entity id" hint="e.g. ctr:shop-api-1, host, agent:vps-1">
            {(id) => <Input id={id} value={entityId} onChange={(e) => setEntityId(e.target.value)} className="font-mono" />}
          </Field>
        )}
        <ToggleRow label="Project" hint={useProject ? undefined : "Any project"} control={<Switch checked={useProject} onCheckedChange={setUseProject} aria-label="Match a project" />} />
        {useProject && (
          <Field label="Project" hint="Name or id, e.g. shop or proj:shop">
            {(id) => <Input id={id} value={projectId} onChange={(e) => setProjectId(e.target.value)} className="font-mono" />}
          </Field>
        )}
        <Field label="Duration">{(id) => <DurationSelect id={id} value={duration} onChange={setDuration} min={60} />}</Field>
        <Field label="Reason" hint="Shown on the silence and in the audit log">
          {(id) => <Input id={id} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="Planned maintenance" />}
        </Field>
        {!hasMatcher && <p className="text-small text-text-4">Choose at least one of rule, entity or project.</p>}
      </SheetBody>
      <SheetFooter>
        <Button type="button" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={!valid || mutation.isPending}>
          {mutation.isPending ? "Creating…" : "Create silence"}
        </Button>
      </SheetFooter>
    </form>
  );
}
