import { Toaster as Sonner, type ToasterProps } from "sonner";
import { Alert02Icon, CheckmarkCircle02Icon, InformationCircleIcon, Loading03Icon } from "@hugeicons/core-free-icons";
import { Icon } from "@/components/ui/icon";
import { buttonVariants } from "@/components/ui/button";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

/**
 * Toasts for action results (sonner), restyled to the lyra surfaces: square
 * popover, 1px ring elevation, status-coloured icon, 13px text. Mounted once
 * in the signed-in shell; call `toast.success()` / `toast.error()`.
 */
function Toaster(props: ToasterProps) {
  const { resolvedTheme } = useTheme();
  return (
    <Sonner
      theme={resolvedTheme}
      position="bottom-right"
      gap={8}
      visibleToasts={4}
      duration={6000}
      icons={{
        success: <Icon icon={CheckmarkCircle02Icon} className="size-4 text-status-healthy" />,
        error: <Icon icon={Alert02Icon} className="size-4 text-status-critical" />,
        warning: <Icon icon={Alert02Icon} className="size-4 text-status-warning" />,
        info: <Icon icon={InformationCircleIcon} className="size-4 text-status-info" />,
        loading: <Icon icon={Loading03Icon} className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />,
      }}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast:
            "group/toast flex w-full items-start gap-2.5 rounded-card bg-popover px-3.5 py-3 text-control text-popover-foreground shadow-pop sm:w-[380px]",
          icon: "mt-px flex size-4 shrink-0 items-center justify-center",
          content: "flex min-w-0 flex-1 flex-col gap-0.5",
          title: "font-medium text-foreground",
          description: "text-small text-muted-foreground text-pretty break-words",
          actionButton: cn(buttonVariants({ variant: "outline", size: "sm" }), "shrink-0"),
          cancelButton: cn(buttonVariants({ variant: "ghost", size: "sm" }), "shrink-0"),
          closeButton: "text-muted-foreground",
        },
      }}
      {...props}
    />
  );
}

export { Toaster };
