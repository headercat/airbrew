import { NavLink, useLocation, useNavigate } from "react-router-dom";
import {
  Coffee,
  Globe,
  LogOut,
  Shield,
  Sun,
  UserCircle,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuPortal,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useAuth } from "@/lib/auth";
import { useTheme } from "@/lib/theme";
import { SUPPORTED_LANGUAGES } from "@/lib/i18n";
import {
  NAV_TREE,
  SETTINGS_NAV,
  ADMIN_NAV_GROUPS,
  deriveExpandedNode,
  type NavNode,
} from "@/lib/modules";
import { cn } from "@/lib/utils";

// ── Sizing constants ──────────────────────────────────────────────
// Single source of truth for rail geometry. All values in Tailwind
// units (1 unit = 0.25rem = 4px).
//
//   RAIL_W        56px   icon-only column
//   PANEL_W       208px  sub-menu column
//   BAR_H         48px   brand + panel-header height
//   BTN           32px   every clickable icon button
//   ICON          16px   every lucide icon in the rail
//   CHILD_ICON    16px   sub-panel child icon (same as rail)
//   GAP           4px    between rail items
//   PAD           8px    rail/section inner padding

export function Sidebar() {
  const location = useLocation();
  const expanded = deriveExpandedNode(location.pathname);

  return (
    <div className="flex h-full shrink-0">
      <div className="sidebar-bg flex w-14 flex-col border-r border-sidebar-border">
        <RailInner />
      </div>
      {expanded && expanded.children && expanded.to === "/admin" && (
        <AdminSubPanel />
      )}
      {expanded && expanded.children && expanded.to !== "/admin" && (
        <SubPanel node={expanded} />
      )}
    </div>
  );
}

// ---- Rail ----

function RailInner() {
  const { user } = useAuth();

  return (
    <>
      {/* Brand */}
      <div className="flex h-12 shrink-0 items-center justify-center">
        <Coffee className="h-4 w-4 text-primary" />
      </div>

      {/* Nav */}
      <nav className="flex flex-1 flex-col items-center gap-1 py-2">
        {NAV_TREE.map((node) => (
          <RailItem key={node.to} node={node} />
        ))}

        {user?.role === "admin" && (
          <>
            <div className="my-2 h-px w-7 bg-sidebar-border" />
            {SETTINGS_NAV.filter((n) => n.adminOnly).map((node) => (
              <RailItem key={node.to} node={node} />
            ))}
          </>
        )}
      </nav>

      {/* User */}
      <RailUser />
    </>
  );
}

function RailItem({ node }: { node: NavNode }) {
  const { t } = useTranslation();
  const location = useLocation();
  const Icon = node.icon;

  const isActive = (() => {
    const base = node.to.split("?")[0];
    if (base === "/") return location.pathname === "/";
    return (
      location.pathname === base || location.pathname.startsWith(base + "/")
    );
  })();

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <NavLink
          to={node.to}
          end={node.to === "/"}
          className={cn(
            "flex h-8 w-8 items-center justify-center rounded-lg transition-colors",
            isActive
              ? "bg-accent text-accent-foreground shadow-sm"
              : "text-muted-foreground hover:bg-accent/50 hover:text-foreground",
          )}
        >
          <Icon className="h-4 w-4" />
        </NavLink>
      </TooltipTrigger>
      <TooltipContent side="right">{t(node.labelKey)}</TooltipContent>
    </Tooltip>
  );
}

// ---- Admin sub-panel (grouped navigation) ----

function AdminSubPanel() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();

  return (
    <div className="sidebar-bg flex w-52 shrink-0 flex-col border-r border-sidebar-border">
      {/* Header */}
      <div className="flex h-12 shrink-0 items-center gap-2 px-3">
        <span className="flex min-w-0 flex-1 items-center gap-2 px-1.5 text-[13px] font-semibold">
          <Shield className="h-4 w-4 shrink-0 text-muted-foreground" />
          {t("admin.title")}
        </span>
        <button
          onClick={() => navigate("/")}
          className="shrink-0 rounded-md p-1 text-muted-foreground/50 transition-colors hover:bg-accent/50 hover:text-foreground"
          aria-label="Close"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Grouped nav */}
      <nav className="flex-1 overflow-y-auto px-2 py-1.5">
        {ADMIN_NAV_GROUPS.map((group) => (
          <div key={group.labelKey} className="mb-3">
            <p className="px-2.5 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/50">
              {t(group.labelKey)}
            </p>
            <div className="space-y-0.5">
              {group.items.map((item) => {
                const isExact =
                  item.to === "/admin"
                    ? location.pathname === "/admin"
                    : location.pathname === item.to ||
                      location.pathname.startsWith(item.to + "/");
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.to === "/admin"}
                    className={cn(
                      "flex items-center gap-2.5 rounded-md px-2.5 py-[7px] text-[12.5px] transition-colors",
                      isExact
                        ? "bg-accent/70 font-medium text-foreground"
                        : "text-muted-foreground/80 hover:bg-accent/40 hover:text-foreground",
                    )}
                  >
                    <item.icon className="h-3.5 w-3.5 shrink-0" />
                    <span className="truncate">{t(item.labelKey)}</span>
                  </NavLink>
                );
              })}
            </div>
          </div>
        ))}
      </nav>
    </div>
  );
}

// ---- Sub-panel (non-admin modules) ----

function SubPanel({ node }: { node: NavNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const Icon = node.icon;

  return (
    <div className="sidebar-bg flex w-52 shrink-0 flex-col border-r border-sidebar-border">
      {/* Header — same height as the rail brand row */}
      <div className="flex h-12 shrink-0 items-center gap-2 px-3">
        <button
          onClick={() => navigate(node.to)}
          className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1 text-left hover:bg-accent/50"
        >
          <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-[13px] font-semibold">
            {t(node.labelKey)}
          </span>
        </button>
        <button
          onClick={() => navigate("/")}
          className="shrink-0 rounded-md p-1 text-muted-foreground/50 transition-colors hover:bg-accent/50 hover:text-foreground"
          aria-label="Close"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Children */}
      <nav className="flex-1 overflow-y-auto px-2 py-1.5">
        <div className="space-y-0.5">
          {node.children?.map((child) => {
            const childBase = child.to.split("?")[0];
            const isExact =
              childBase === location.pathname &&
              (location.search === "" || child.to.includes("?"));
            return (
              <NavLink
                key={child.to}
                to={child.to}
                className={({ isActive: navActive }) =>
                  cn(
                    "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] transition-colors",
                    isExact || navActive
                      ? "bg-accent/70 font-medium text-foreground"
                      : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
                  )
                }
              >
                <child.icon className="h-4 w-4 shrink-0" />
                <span className="truncate">{t(child.labelKey)}</span>
              </NavLink>
            );
          })}
        </div>
      </nav>
    </div>
  );
}

// ---- User avatar ----

function RailUser() {
  const { t } = useTranslation();
  const { user, signOut } = useAuth();
  const navigate = useNavigate();
  const { theme, setTheme } = useTheme();
  const { i18n } = useTranslation();

  if (!user) return null;
  const initial = user.email.charAt(0).toUpperCase();

  return (
    <div className="flex shrink-0 justify-center pb-2">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button className="flex h-8 w-8 items-center justify-center rounded-lg transition-colors hover:bg-accent/50">
            <Avatar className="h-6 w-6">
              {user.avatar_url && <AvatarImage src={user.avatar_url} />}
              <AvatarFallback className="bg-muted text-[10px] font-medium">
                {initial}
              </AvatarFallback>
            </Avatar>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-56" sideOffset={8}>
          <DropdownMenuLabel className="truncate text-xs text-muted-foreground">
            {user.email}
          </DropdownMenuLabel>
          <DropdownMenuSeparator />

          <DropdownMenuItem onClick={() => navigate("/profile")}>
            <UserCircle className="mr-2 h-4 w-4" />
            {t("header.profile")}
          </DropdownMenuItem>
          {user.role === "admin" && (
            <DropdownMenuItem onClick={() => navigate("/admin")}>
              <Shield className="mr-2 h-4 w-4" />
              {t("admin.title")}
            </DropdownMenuItem>
          )}

          <DropdownMenuSeparator />

          {/* Color mode sub-menu */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <Sun className="mr-2 h-4 w-4" />
              {t("theme.title")}
            </DropdownMenuSubTrigger>
            <DropdownMenuPortal>
              <DropdownMenuSubContent>
                <DropdownMenuRadioGroup
                  value={theme}
                  onValueChange={(v) =>
                    setTheme(v as "light" | "dark" | "system")
                  }
                >
                  <DropdownMenuRadioItem value="light">
                    {t("theme.light")}
                  </DropdownMenuRadioItem>
                  <DropdownMenuRadioItem value="dark">
                    {t("theme.dark")}
                  </DropdownMenuRadioItem>
                  <DropdownMenuRadioItem value="system">
                    {t("theme.system")}
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
              </DropdownMenuSubContent>
            </DropdownMenuPortal>
          </DropdownMenuSub>

          {/* Language sub-menu */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <Globe className="mr-2 h-4 w-4" />
              {t("language.label")}
            </DropdownMenuSubTrigger>
            <DropdownMenuPortal>
              <DropdownMenuSubContent>
                <DropdownMenuRadioGroup
                  value={i18n.language?.slice(0, 2) ?? "en"}
                  onValueChange={(v) => i18n.changeLanguage(v)}
                >
                  {SUPPORTED_LANGUAGES.map((lang) => (
                    <DropdownMenuRadioItem key={lang.code} value={lang.code}>
                      {lang.label}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuSubContent>
            </DropdownMenuPortal>
          </DropdownMenuSub>

          <DropdownMenuSeparator />

          <DropdownMenuItem
            onClick={async () => {
              await signOut();
              navigate("/login");
            }}
          >
            <LogOut className="mr-2 h-4 w-4" />
            {t("header.signOut")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
