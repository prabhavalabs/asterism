import { useId, type ReactNode } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface FieldProps {
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  className?: string;
  /** Renders the control with the generated id (for <label htmlFor>). */
  children: (id: string) => ReactNode;
}

/** A labelled form control with an optional hint or error line. */
export function Field({ label, hint, error, className, children }: FieldProps) {
  const id = useId();
  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <label htmlFor={id} className="text-control font-medium text-foreground">
        {label}
      </label>
      {children(id)}
      {error ? (
        <p className="text-small text-destructive">{error}</p>
      ) : hint ? (
        <p className="text-small text-text-4">{hint}</p>
      ) : null}
    </div>
  );
}

/** Rows of switch + text, for boolean settings inside a form. */
export function ToggleRow({ label, hint, control }: { label: ReactNode; hint?: ReactNode; control: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-tile border border-divider px-3 py-2.5">
      <div className="flex min-w-0 flex-col">
        <span className="text-control text-foreground">{label}</span>
        {hint && <span className="text-small text-text-4">{hint}</span>}
      </div>
      {control}
    </div>
  );
}

export const DURATION_PRESETS = [
  { value: 0, label: "At once" },
  { value: 30, label: "30 seconds" },
  { value: 60, label: "1 minute" },
  { value: 120, label: "2 minutes" },
  { value: 300, label: "5 minutes" },
  { value: 600, label: "10 minutes" },
  { value: 900, label: "15 minutes" },
  { value: 1800, label: "30 minutes" },
  { value: 3600, label: "1 hour" },
  { value: 21600, label: "6 hours" },
  { value: 86400, label: "1 day" },
  { value: 604800, label: "7 days" },
] as const;

/** Select over DURATION_PRESETS (plus the current value when it is custom). */
export function DurationSelect({
  id,
  value,
  onChange,
  min = 0,
  ariaLabel,
}: {
  id?: string;
  value: number;
  onChange: (seconds: number) => void;
  min?: number;
  ariaLabel?: string;
}) {
  const presets = DURATION_PRESETS.filter((p) => p.value >= min);
  const known = presets.some((p) => p.value === value);
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger id={id} aria-label={ariaLabel} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {presets.map((p) => (
          <SelectItem key={p.value} value={String(p.value)}>
            {p.label}
          </SelectItem>
        ))}
        {!known && <SelectItem value={String(value)}>{value} seconds</SelectItem>}
      </SelectContent>
    </Select>
  );
}
