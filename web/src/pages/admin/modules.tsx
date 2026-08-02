import { type ReactNode, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Boxes, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { SettingRow } from "./shared";
import { SectionHeader } from "./shared";
import { api, type AdminModule } from "@/lib/api";

export default function AdminModules() {
  const { t } = useTranslation();
  const [mods, setMods] = useState<AdminModule[] | null>(null);
  useEffect(() => {
    api
      .get<{ modules: AdminModule[] }>("/api/admin/modules")
      .then((r) => setMods(r.modules))
      .catch(() => {});
  }, []);

  if (!mods)
    return (
      <>
        <SectionHeader
          icon={Boxes}
          titleKey="admin.modules.title"
          descKey="admin.modules.description"
        />
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </>
    );

  async function toggle(mod: AdminModule, enabled: boolean) {
    if (mod.system) return;
    setMods(
      (prev) =>
        prev?.map((m) => (m.key === mod.key ? { ...m, enabled } : m)) ?? null,
    );
    try {
      await api.patch(`/api/admin/modules/${mod.key}`, { enabled });
    } catch {
      setMods(
        (prev) =>
          prev?.map((m) =>
            m.key === mod.key ? { ...m, enabled: !enabled } : m,
          ) ?? null,
      );
    }
  }

  return (
    <>
      <SectionHeader
        icon={Boxes}
        titleKey="admin.modules.title"
        descKey="admin.modules.description"
      />
      <Card>
        <CardContent className="divide-y divide-border p-0">
          {mods.map((m) => (
            <SettingRow key={m.key} title={m.name} description={m.description}>
              <div className="flex min-w-0 flex-col items-end gap-2">
                <div className="flex flex-wrap items-center justify-end gap-2">
                  <Badge
                    variant={m.health === "ok" ? "default" : "outline"}
                    className="text-[10px]"
                  >
                    {t(`admin.modules.health.${m.health}`)}
                  </Badge>
                  {m.system && (
                    <Badge variant="secondary" className="text-[10px]">
                      {t("admin.modules.system")}
                    </Badge>
                  )}
                  <ButtonLink to={m.settings_path}>
                    {t("admin.modules.settings")}
                  </ButtonLink>
                  <Switch
                    checked={m.enabled}
                    disabled={m.system}
                    onCheckedChange={(v) => toggle(m, v)}
                  />
                </div>
                {m.health_checks.length > 0 && (
                  <div className="max-w-[420px] space-y-1 text-right text-[11px] text-muted-foreground">
                    {m.health_checks.map((check) => (
                      <div key={check.key}>
                        <span className="font-medium text-foreground">
                          {check.key}
                        </span>
                        {": "}
                        {check.message}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </SettingRow>
          ))}
        </CardContent>
      </Card>
    </>
  );
}

function ButtonLink({
  to,
  children,
}: {
  to: string;
  children: ReactNode;
}) {
  return (
    <Link
      to={to}
      className="inline-flex h-8 items-center rounded-md border border-input px-3 text-xs font-medium hover:bg-accent"
    >
      {children}
    </Link>
  );
}
