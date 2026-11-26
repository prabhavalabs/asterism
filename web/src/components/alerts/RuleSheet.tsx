import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertRuleKind, type AlertRule } from "@/gen/asterism/v1/alerts_pb";
import { alertsClient } from "@/lib/alertsRpc";
import { SegmentedControl, errorMessage } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { DurationSelect, Field, ToggleRow } from "./Field";
import { SEVERITY_OPTIONS, parseList, severityFromName, severityName, type SeverityValue } from "./model";

export interface RuleSheetProps {
  rule: AlertRule | null;
  onOpenChange: (open: boolean) => void;
}

/** Edit one rule's settings (enabled, severity, thresholds, timing, filters). */
export function RuleSheet({ rule, onOpenChange }: RuleSheetProps) {
  return (
    <Sheet open={rule !== null} onOpenChange={onOpenChange}>
      <SheetContent>{rule && <RuleForm key={rule.id} rule={rule} onDone={() => onOpenChange(false)} />}</SheetContent>
    </Sheet>
  );
}

function num(text: string): number {
  const n = Number(text);
  return Number.isFinite(n) ? n : 0;
}

function RuleForm({ rule, onDone }: { rule: AlertRule; onDone: () => void }) {
  const qc = useQueryClient();
  const [enabled, setEnabled] = useState(rule.enabled);
  const [severity, setSeverity] = useState<SeverityValue>(severityName(rule.severity));
  const [warning, setWarning] = useState(String(rule.warningThreshold || ""));
  const [critical, setCritical] = useState(String(rule.criticalThreshold || ""));
  const [count, setCount] = useState(String(rule.count || 3));
  const [windowSeconds, setWindowSeconds] = useState(rule.windowSeconds || 600);
  const [forSeconds, setForSeconds] = useState(rule.forSeconds);
  const [include, setInclude] = useState(rule.includeProjects.join(", "));
  const [exclude, setExclude] = useState(rule.excludeProjects.join(", "));
  const [kinds, setKinds] = useState(rule.eventKinds.join(", "));
  const [minEvent, setMinEvent] = useState<SeverityValue>(severityName(rule.minEventSeverity));

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["alerts"] });
  const save = useMutation({
    mutationFn: () =>
      alertsClient.updateRule({
        rule: {
          id: rule.id,
          enabled,
          severity: severityFromName(severity),
          warningThreshold: num(warning),
          criticalThreshold: num(critical),
          count: Math.round(num(count)),
          windowSeconds,
          forSeconds,
          includeProjects: parseList(include),
          excludeProjects: parseList(exclude),
          eventKinds: parseList(kinds),
          minEventSeverity: severityFromName(minEvent),
        },
      }),
    onSuccess: () => {
      toast.success(`Saved ${rule.name}`);
      invalidate();
      onDone();
    },
    onError: (err) => toast.error("Could not save the rule", { description: errorMessage(err) }),
  });
  const reset = useMutation({
    mutationFn: () => alertsClient.updateRule({ rule: { id: rule.id }, reset: true }),
    onSuccess: () => {
      toast.success(`Restored the defaults for ${rule.name}`);
      invalidate();
      onDone();
    },
    onError: (err) => toast.error("Could not reset the rule", { description: errorMessage(err) }),
  });

  const isThreshold = rule.kind === AlertRuleKind.THRESHOLD;
  const isCount = rule.kind === AlertRuleKind.COUNT;
  const isForward = rule.kind === AlertRuleKind.FORWARD;

  return (
    <form
      className="flex h-full min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <SheetHeader>
        <SheetTitle>{rule.name}</SheetTitle>
        <SheetDescription>{rule.description}</SheetDescription>
      </SheetHeader>
      <SheetBody className="flex flex-col gap-4">
        <ToggleRow label="Enabled" hint={<span className="font-mono">{rule.id}</span>} control={<Switch checked={enabled} onCheckedChange={setEnabled} aria-label="Rule enabled" />} />
        {!isThreshold && !isForward && (
          <Field label="Severity">
            {() => (
              <SegmentedControl aria-label="Severity" fullWidth value={severity} onValueChange={setSeverity} options={SEVERITY_OPTIONS} />
            )}
          </Field>
        )}
        {isThreshold && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="Warning at (%)" hint="0 turns the level off">
              {(id) => <Input id={id} inputMode="decimal" className="tabular-nums" value={warning} onChange={(e) => setWarning(e.target.value)} />}
            </Field>
            <Field label="Critical at (%)" hint="Must be above warning">
              {(id) => <Input id={id} inputMode="decimal" className="tabular-nums" value={critical} onChange={(e) => setCritical(e.target.value)} />}
            </Field>
          </div>
        )}
        {isCount && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="Restarts">
              {(id) => <Input id={id} inputMode="numeric" className="tabular-nums" value={count} onChange={(e) => setCount(e.target.value)} />}
            </Field>
            <Field label="Within">{(id) => <DurationSelect id={id} value={windowSeconds} onChange={setWindowSeconds} min={60} />}</Field>
          </div>
        )}
        {isForward && (
          <>
            <Field label="Event kinds" hint="Comma separated; prefix.* matches a family (container.*). Empty forwards every kind.">
              {(id) => <Input id={id} className="font-mono" value={kinds} onChange={(e) => setKinds(e.target.value)} placeholder="container.oom, unit.failed" />}
            </Field>
            <Field label="Minimum event severity">
              {() => <SegmentedControl aria-label="Minimum event severity" fullWidth value={minEvent} onValueChange={setMinEvent} options={SEVERITY_OPTIONS} />}
            </Field>
          </>
        )}
        {!isForward && (
          <Field label="Fire after" hint="How long the condition must hold before the alert fires (pending until then)">
            {(id) => <DurationSelect id={id} value={forSeconds} onChange={setForSeconds} />}
          </Field>
        )}
        <Field label="Only these projects" hint="Names or ids, comma separated. Empty covers every project and the host.">
          {(id) => <Input id={id} className="font-mono" value={include} onChange={(e) => setInclude(e.target.value)} />}
        </Field>
        <Field label="Never these projects">
          {(id) => <Input id={id} className="font-mono" value={exclude} onChange={(e) => setExclude(e.target.value)} />}
        </Field>
      </SheetBody>
      <SheetFooter className="justify-between">
        <Button type="button" variant="ghost" disabled={rule.isDefault || reset.isPending} onClick={() => reset.mutate()}>
          Restore defaults
        </Button>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save rule"}
          </Button>
        </div>
      </SheetFooter>
    </form>
  );
}
