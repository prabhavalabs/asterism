import { BookOpen01Icon, ComputerIcon, LinkSquare02Icon, Logout01Icon, Moon02Icon, Sun03Icon, UnfoldMoreIcon } from "@hugeicons/core-free-icons";
import { Icon, type IconData } from "@/components/ui/icon";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import { DOCS_URL } from "./nav";
import { THEME_PREFERENCES, isThemePreference, useTheme, type ThemePreference } from "@/lib/theme";

const THEME_OPTIONS: Record<ThemePreference, { label: string; icon: IconData }> = {
  light: { label: "Light", icon: Sun03Icon },
  dark: { label: "Dark", icon: Moon02Icon },
  system: { label: "System", icon: ComputerIcon },
};

function initials(source: string): string {
  const name = source.split("@")[0] ?? source;
  const parts = name.split(/[._\-\s]+/).filter(Boolean);
  const letters = parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : name.slice(0, 2);
  return letters.toUpperCase() || "A";
}

interface UserMenuProps {
  email?: string;
  onLogout: () => void;
}

/** The signed-in administrator (nav-user block style), with the theme switch, the documentation link and sign-out. */
export function UserMenu({ email, onLogout }: UserMenuProps) {
  const { preference, setPreference } = useTheme();
  const { isMobile } = useSidebar();
  const display = email || "Administrator";
  const letters = initials(display);

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="gap-2.5 px-2 data-[state=open]:bg-sidebar-accent"
              aria-label={`Account menu for ${display}`}
            >
              <Avatar className="size-8">
                <AvatarFallback>{letters}</AvatarFallback>
              </Avatar>
              <span className="grid min-w-0 flex-1 leading-tight">
                <span className="truncate text-control font-medium text-foreground">Administrator</span>
                <span className="truncate text-small text-muted-foreground">{display}</span>
              </span>
              <Icon icon={UnfoldMoreIcon} className="ml-auto group-data-[collapsible=icon]:hidden" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            side={isMobile ? "top" : "right"}
            align="end"
            sideOffset={isMobile ? 4 : 8}
            className="min-w-56"
          >
            <DropdownMenuLabel className="flex items-center gap-2.5 px-2.5 py-2 font-normal text-foreground">
              <Avatar className="size-8">
                <AvatarFallback>{letters}</AvatarFallback>
              </Avatar>
              <span className="grid min-w-0 flex-1 leading-tight">
                <span className="truncate text-control font-medium">Administrator</span>
                <span className="truncate text-small text-muted-foreground">{display}</span>
              </span>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>Theme</DropdownMenuLabel>
            <DropdownMenuRadioGroup
              value={preference}
              onValueChange={(value) => {
                if (isThemePreference(value)) setPreference(value);
              }}
            >
              {THEME_PREFERENCES.map((value) => {
                const { label, icon } = THEME_OPTIONS[value];
                return (
                  <DropdownMenuRadioItem key={value} value={value}>
                    <Icon icon={icon} />
                    {label}
                  </DropdownMenuRadioItem>
                );
              })}
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <a href={DOCS_URL} target="_blank" rel="noreferrer noopener">
                <Icon icon={BookOpen01Icon} />
                Documentation
                <Icon icon={LinkSquare02Icon} className="ml-auto size-3.5!" />
                <span className="sr-only">(opens in a new tab)</span>
              </a>
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onLogout}>
              <Icon icon={Logout01Icon} />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
