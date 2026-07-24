import { type LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

// SectionHeader renders the page title + description at the top of each
// admin section. No margin-bottom — the parent's space-y-8 handles spacing.
export function SectionHeader({
  icon: Icon,
  titleKey,
  descKey,
}: {
  icon: LucideIcon;
  titleKey: string;
  descKey: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1">
      <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
        <Icon className="h-5 w-5 text-primary" />
        {t(titleKey)}
      </h1>
      <p className="text-sm text-muted-foreground">{t(descKey)}</p>
    </div>
  );
}

// SettingRow is a reusable label/control row used inside settings cards.
// The parent CardContent should use `divide-y divide-border` to draw the
// separators between rows. SettingRow itself does NOT draw borders to
// avoid double-border conflicts.
export function SettingRow({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-4 px-4 py-5 lg:px-6">
      <div className="min-w-0 space-y-0.5">
        <p className="text-[13px] font-medium">{title}</p>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}
