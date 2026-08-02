import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { SectionHeader, SettingRow } from "./shared";
import { api, isApiError, type AdminModule } from "@/lib/api";
import { MODULES } from "@/lib/modules";

export default function AdminModuleSettings() {
  const { moduleKey } = useParams<{ moduleKey: string }>();
  const { t } = useTranslation();
  const meta = MODULES.find((m) => m.key === moduleKey);
  const [mod, setMod] = useState<AdminModule | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!moduleKey) return;
    api
      .get<{ modules: AdminModule[] }>("/api/admin/modules")
      .then((res) => {
        const found = res.modules.find((m) => m.key === moduleKey);
        setMod(found ?? null);
      })
      .catch(() => {});
  }, [moduleKey]);

  async function toggle(enabled: boolean) {
    if (!mod || mod.system || !moduleKey) return;
    setMod((prev) => (prev ? { ...prev, enabled } : null));
    try {
      await api.patch(`/api/admin/modules/${moduleKey}`, { enabled });
    } catch (err) {
      setMod((prev) => (prev ? { ...prev, enabled: !enabled } : null));
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  if (!meta) {
    return (
      <p className="py-12 text-center text-sm text-muted-foreground">
        Unknown module: {moduleKey}
      </p>
    );
  }

  const Icon = meta.icon;

  return (
    <>
      <SectionHeader
        icon={Icon}
        titleKey={meta.labelKey}
        descKey={meta.descriptionKey}
      />
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Card>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow
            title={t("admin.moduleSettings.enabled")}
            description={t("admin.moduleSettings.enabledDesc")}
          >
            <div className="flex items-center gap-2">
              {mod?.system && (
                <Badge variant="secondary" className="text-[10px]">
                  {t("admin.modules.system")}
                </Badge>
              )}
              {mod ? (
                <Switch
                  checked={mod.enabled}
                  disabled={mod.system}
                  onCheckedChange={toggle}
                />
              ) : (
                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              )}
            </div>
          </SettingRow>
          <SettingRow
            title={t("admin.moduleSettings.status")}
            description={t("admin.moduleSettings.statusDesc")}
          >
            <div className="text-right">
              <Badge
                variant={mod?.health === "ok" ? "default" : "outline"}
                className="text-[10px]"
              >
                {mod ? t(`admin.modules.health.${mod.health}`) : "—"}
              </Badge>
              <p className="mt-1 max-w-[320px] text-xs text-muted-foreground">
                {mod?.status_message ?? "—"}
              </p>
            </div>
          </SettingRow>
          <SettingRow
            title={t("admin.moduleSettings.adminOnly")}
            description={t("admin.moduleSettings.adminOnlyDesc")}
          >
            <Badge
              variant={mod?.admin_only ? "default" : "outline"}
              className="text-[10px]"
            >
              {mod?.admin_only ? "Admin" : "All users"}
            </Badge>
          </SettingRow>
          <SettingRow
            title={t("admin.moduleSettings.dependencies")}
            description={t("admin.moduleSettings.dependenciesDesc")}
          >
            <div className="flex max-w-[320px] flex-wrap justify-end gap-1">
              {(mod?.dependencies ?? []).map((dep) => (
                <Badge key={dep} variant="outline" className="text-[10px]">
                  {dep}
                </Badge>
              ))}
            </div>
          </SettingRow>
          <SettingRow
            title={t("admin.moduleSettings.disableImpact")}
            description={t("admin.moduleSettings.disableImpactDesc")}
          >
            <p className="max-w-[340px] text-right text-xs text-muted-foreground">
              {mod?.disable_impact ?? "—"}
            </p>
          </SettingRow>
        </CardContent>
      </Card>
    </>
  );
}
