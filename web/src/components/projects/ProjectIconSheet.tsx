import { useEffect, useId, useState } from "react";
import type * as React from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { toast } from "sonner";
import { ImageUpload01Icon, RefreshIcon, Undo02Icon } from "@hugeicons/core-free-icons";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ProjectIcon, iconSourceLabel } from "@/components/common";
import { errorMessage } from "@/components/common/errors";
import { FaviconSource, type Project, type ProjectIconResponse } from "@/gen/asterism/v1/asterism_pb";
import { formatBytes, formatRelativeTime } from "@/lib/format";
import { projectSettingsClient } from "@/lib/rpc";
import { cn } from "@/lib/utils";
import { ICON_ACCEPT, iconContentType, iconFileProblem } from "./iconUpload";

// Queries that render project icons.
const ICON_QUERY_ROOTS = new Set(["project", "projects", "overview", "graph", "events"]);

function failureText(err: unknown): string {
  if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
    return "Only a signed-in admin session can change project icons; API tokens are read-only here.";
  }
  return errorMessage(err);
}

interface Picked {
  file: File;
  previewUrl: string;
}

export interface ProjectIconSheetProps {
  project: Project;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Change, refresh or reset a project's icon (admin sessions only). */
export function ProjectIconSheet({ project, open, onOpenChange }: ProjectIconSheetProps) {
  const qc = useQueryClient();
  const inputId = useId();
  const [picked, setPicked] = useState<Picked | null>(null);
  const [problem, setProblem] = useState<string | undefined>(undefined);
  const [dragging, setDragging] = useState(false);

  // Revoke the preview URL when it is replaced or the sheet unmounts.
  useEffect(() => {
    if (!picked) return;
    return () => URL.revokeObjectURL(picked.previewUrl);
  }, [picked]);

  useEffect(() => {
    if (!open) {
      setPicked(null);
      setProblem(undefined);
      setDragging(false);
    }
  }, [open]);

  const done = (title: string) => (res: ProjectIconResponse) => {
    void qc.invalidateQueries({ predicate: (q) => ICON_QUERY_ROOTS.has(String(q.queryKey[0])) });
    toast.success(title, { description: `${iconSourceLabel(res.icon)} · recorded in Events` });
  };
  const failed = (title: string) => (err: unknown) => toast.error(title, { description: failureText(err) });

  const upload = useMutation({
    mutationFn: async (file: File) => {
      const image = new Uint8Array(await file.arrayBuffer());
      return projectSettingsClient.setProjectIcon({ project: project.id, image, contentType: iconContentType(file) ?? "" });
    },
    onSuccess: (res) => {
      setPicked(null);
      done(`Updated the icon of ${project.name}`)(res);
    },
    onError: failed("Couldn't upload the icon"),
  });
  const refresh = useMutation({
    mutationFn: () => projectSettingsClient.refreshProjectIcon({ project: project.id }),
    onSuccess: (res) => {
      const found = (res.icon?.faviconSource ?? FaviconSource.UNSPECIFIED) !== FaviconSource.UNSPECIFIED;
      if (found) done(`Refreshed the favicon of ${project.name}`)(res);
      else {
        void qc.invalidateQueries({ predicate: (q) => ICON_QUERY_ROOTS.has(String(q.queryKey[0])) });
        toast.info(`No favicon found for ${project.name}`, { description: res.icon?.faviconStatus || undefined });
      }
    },
    onError: failed("Couldn't refresh the favicon"),
  });
  const reset = useMutation({
    mutationFn: () => projectSettingsClient.clearProjectIcon({ project: project.id }),
    onSuccess: done(`Reset the icon of ${project.name}`),
    onError: failed("Couldn't reset the icon"),
  });
  const busy = upload.isPending || refresh.isPending || reset.isPending;

  const pick = (file: File | undefined) => {
    if (!file) return;
    const why = iconFileProblem(file);
    setProblem(why);
    setPicked(why ? null : { file, previewUrl: URL.createObjectURL(file) });
  };

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragging(false);
    pick(e.dataTransfer.files[0]);
  };

  const icon = project.icon;
  const origin = icon?.faviconOrigin;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Project icon</SheetTitle>
          <SheetDescription>Shown next to {project.name} in lists, the sidebar and the topology.</SheetDescription>
        </SheetHeader>
        <SheetBody className="flex flex-col gap-5">
          <section aria-label="Current icon" className="flex items-center gap-3">
            <ProjectIcon icon={icon} name={project.name} size={40} decorative={false} />
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className="text-control text-foreground">{iconSourceLabel(icon)}</span>
              {origin ? (
                <span className="truncate font-mono text-small text-muted-foreground" title={origin}>
                  {origin}
                </span>
              ) : icon?.faviconStatus && !icon.hasUpload ? (
                <span className="text-small text-muted-foreground">
                  {icon.faviconStatus}
                  {icon.faviconCheckedMs ? ` · checked ${formatRelativeTime(icon.faviconCheckedMs)}` : ""}
                </span>
              ) : null}
            </div>
          </section>

          <section aria-label="Upload a custom icon" className="flex flex-col gap-2">
            <label
              htmlFor={inputId}
              onDragOver={(e) => {
                e.preventDefault();
                setDragging(true);
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={onDrop}
              className={cn(
                "flex cursor-pointer flex-col items-center gap-2 rounded-card border border-dashed border-border-strong bg-surface-subtle px-4 py-6 text-center transition-colors",
                "hover:border-primary/50 focus-within:border-primary focus-within:ring-2 focus-within:ring-ring/40",
                dragging && "border-primary bg-primary/6",
              )}
            >
              {picked ? (
                <span className="flex size-16 items-center justify-center rounded-avatar bg-card p-1.5 ring-1 ring-border">
                  <img src={picked.previewUrl} alt="Preview of the selected icon" className="size-full object-contain" />
                </span>
              ) : (
                <Icon icon={ImageUpload01Icon} className="size-6 text-text-4" />
              )}
              <span className="text-control text-foreground">
                {picked ? picked.file.name : "Drop an image here or choose a file"}
              </span>
              <span className="text-small text-muted-foreground tabular-nums">
                {picked ? formatBytes(picked.file.size) : "PNG, SVG, ICO, WebP or JPEG · up to 512 KiB · square works best"}
              </span>
              <input
                id={inputId}
                type="file"
                accept={ICON_ACCEPT}
                className="sr-only"
                onChange={(e) => {
                  pick(e.target.files?.[0]);
                  e.target.value = "";
                }}
              />
            </label>
            {problem && (
              <p role="alert" className="text-small text-destructive">
                {problem}
              </p>
            )}
            <div className="flex justify-end gap-2">
              {picked && (
                <Button variant="ghost" onClick={() => setPicked(null)} disabled={busy}>
                  Clear
                </Button>
              )}
              <Button onClick={() => picked && upload.mutate(picked.file)} disabled={!picked || busy}>
                {upload.isPending ? "Uploading…" : "Use this icon"}
              </Button>
            </div>
          </section>
        </SheetBody>
        <SheetFooter className="justify-between">
          <Button variant="outline" onClick={() => refresh.mutate()} disabled={busy}>
            <Icon icon={RefreshIcon} data-icon="inline-start" className={cn(refresh.isPending && "motion-safe:animate-spin")} />
            Refresh from favicon
          </Button>
          <Button variant="outline" onClick={() => reset.mutate()} disabled={busy || !icon?.hasUpload}>
            <Icon icon={Undo02Icon} data-icon="inline-start" />
            Reset to automatic
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
