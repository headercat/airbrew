// Navigation tree data — single source of truth for the sidebar.
import {
  Activity,
  Bot,
  Boxes,
  Cloud,
  Contact,
  FileText,
  Inbox,
  LayoutDashboard,
  Lock,
  type LucideIcon,
  Mailbox,
  MessagesSquare,
  Palette,
  Send,
  Settings as SettingsIcon,
  Shield,
  ShieldCheck,
  Star,
  User as UserIcon,
  Users,
  Workflow,
} from "lucide-react";

export type ModuleKey =
  | "mail"
  | "drive"
  | "contacts"
  | "chat"
  | "ai"
  | "workflow";

export type ModuleMeta = {
  key: ModuleKey;
  labelKey: string;
  descriptionKey: string;
  icon: LucideIcon;
  path: string;
  status: "not_implemented" | "alpha" | "beta" | "stable";
};

export const MODULES: ModuleMeta[] = [
  { key: "mail", labelKey: "dashboard.modules.mail.label", descriptionKey: "dashboard.modules.mail.description", icon: Mailbox, path: "/mail", status: "not_implemented" },
  { key: "drive", labelKey: "dashboard.modules.drive.label", descriptionKey: "dashboard.modules.drive.description", icon: Cloud, path: "/drive", status: "not_implemented" },
  { key: "contacts", labelKey: "dashboard.modules.contacts.label", descriptionKey: "dashboard.modules.contacts.description", icon: Contact, path: "/contacts", status: "not_implemented" },
  { key: "chat", labelKey: "dashboard.modules.chat.label", descriptionKey: "dashboard.modules.chat.description", icon: MessagesSquare, path: "/chat", status: "not_implemented" },
  { key: "ai", labelKey: "dashboard.modules.ai.label", descriptionKey: "dashboard.modules.ai.description", icon: Bot, path: "/ai", status: "not_implemented" },
  { key: "workflow", labelKey: "dashboard.modules.workflow.label", descriptionKey: "dashboard.modules.workflow.description", icon: Workflow, path: "/workflow", status: "not_implemented" },
];

export type NavChild = {
  labelKey: string;
  to: string;
  icon: LucideIcon;
};

export type NavNode = {
  labelKey: string;
  to: string;
  icon: LucideIcon;
  badge?: "planned" | "new" | "admin";
  adminOnly?: boolean;
  children?: NavChild[];
};

// Primary nav — rendered top-to-bottom in the sidebar / rail.
export const NAV_TREE: NavNode[] = [
  { labelKey: "nav.home", to: "/", icon: LayoutDashboard },
  {
    labelKey: "dashboard.modules.mail.label",
    to: "/mail", icon: Mailbox, badge: "planned",
    children: [
      { labelKey: "nav.mail.inbox", to: "/mail", icon: Inbox },
      { labelKey: "nav.mail.sent", to: "/mail?box=sent", icon: Send },
      { labelKey: "nav.mail.drafts", to: "/mail?box=drafts", icon: FileText },
    ],
  },
  {
    labelKey: "dashboard.modules.drive.label",
    to: "/drive", icon: Cloud, badge: "planned",
    children: [
      { labelKey: "nav.drive.myFiles", to: "/drive", icon: FileText },
      { labelKey: "nav.drive.shared", to: "/drive?view=shared", icon: Users },
      { labelKey: "nav.drive.starred", to: "/drive?view=starred", icon: Star },
    ],
  },
  {
    labelKey: "dashboard.modules.contacts.label",
    to: "/contacts", icon: Contact, badge: "planned",
    children: [
      { labelKey: "nav.contacts.all", to: "/contacts", icon: Contact },
      { labelKey: "nav.contacts.groups", to: "/contacts?view=groups", icon: Boxes },
    ],
  },
  {
    labelKey: "dashboard.modules.chat.label",
    to: "/chat", icon: MessagesSquare, badge: "planned",
    children: [
      { labelKey: "nav.chat.channels", to: "/chat", icon: MessagesSquare },
      { labelKey: "nav.chat.direct", to: "/chat?type=dm", icon: UserIcon },
    ],
  },
  {
    labelKey: "dashboard.modules.ai.label",
    to: "/ai", icon: Bot, badge: "planned",
    children: [
      { labelKey: "nav.ai.agents", to: "/ai", icon: Bot },
      { labelKey: "nav.ai.history", to: "/ai?view=history", icon: Activity },
    ],
  },
  {
    labelKey: "dashboard.modules.workflow.label",
    to: "/workflow", icon: Workflow, badge: "planned",
    children: [
      { labelKey: "nav.workflow.flows", to: "/workflow", icon: Workflow },
      { labelKey: "nav.workflow.runs", to: "/workflow?view=runs", icon: Activity },
    ],
  },
];

// Settings section — bottom of sidebar. Profile is intentionally omitted:
// it is reached via the user-avatar dropdown instead.
export const SETTINGS_NAV: NavNode[] = [
  {
    labelKey: "nav.admin",
    to: "/admin",
    icon: Shield,
    adminOnly: true,
    badge: "admin",
    // children is intentionally empty — the admin panel uses its own
    // grouped navigation defined in ADMIN_NAV_GROUPS below.
    children: [],
  },
];

// ── Admin sub-panel grouped navigation ────────────────────────────
// Unlike other modules which have a flat children list, the admin panel
// groups its navigation into sections: workspace-level settings, per-module
// settings, and developer tools. This scales as new modules are added.

export type AdminNavItem = {
  labelKey: string;
  to: string;
  icon: LucideIcon;
};

export type AdminNavGroup = {
  labelKey: string;
  items: AdminNavItem[];
};

export const ADMIN_NAV_GROUPS: AdminNavGroup[] = [
  {
    labelKey: "admin.groups.general",
    items: [
      { labelKey: "admin.overview.title", to: "/admin", icon: LayoutDashboard },
      { labelKey: "admin.users.title", to: "/admin/users", icon: Users },
      { labelKey: "admin.security.title", to: "/admin/security", icon: ShieldCheck },
      { labelKey: "admin.branding.title", to: "/admin/branding", icon: Palette },
      { labelKey: "admin.system.title", to: "/admin/system", icon: SettingsIcon },
      { labelKey: "admin.audit.title", to: "/admin/audit", icon: Activity },
    ],
  },
  {
    labelKey: "admin.groups.modules",
    items: [
      { labelKey: "dashboard.modules.mail.label", to: "/admin/modules/mail", icon: Mailbox },
      { labelKey: "dashboard.modules.drive.label", to: "/admin/modules/drive", icon: Cloud },
      { labelKey: "dashboard.modules.contacts.label", to: "/admin/modules/contacts", icon: Contact },
      { labelKey: "dashboard.modules.chat.label", to: "/admin/modules/chat", icon: MessagesSquare },
      { labelKey: "dashboard.modules.ai.label", to: "/admin/modules/ai", icon: Bot },
      { labelKey: "dashboard.modules.workflow.label", to: "/admin/modules/workflow", icon: Workflow },
    ],
  },
  {
    labelKey: "admin.groups.developers",
    items: [
      { labelKey: "admin.oauth.title", to: "/admin/oauth", icon: Lock },
    ],
  },
];

// All nodes combined for lookups.
export const ALL_NAV_NODES: NavNode[] = [...NAV_TREE, ...SETTINGS_NAV];

// deriveExpandedNode returns the NavNode whose base path the current route
// is under. When non-null, the sidebar collapses to an icon rail and a
// sub-panel opens showing the node's children.
export function deriveExpandedNode(pathname: string): NavNode | null {
  if (pathname === "/" || pathname === "/profile" || pathname === "/login" || pathname === "/register") {
    return null;
  }
  for (const node of ALL_NAV_NODES) {
    const base = node.to.split("?")[0];
    if (base !== "/" && (pathname === base || pathname.startsWith(base + "/"))) {
      return node;
    }
  }
  return null;
}
