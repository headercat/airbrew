import { useEffect, useState } from "react";
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
    api.get<{ modules: AdminModule[] }>("/api/admin/modules").then((r) => setMods(r.modules)).catch(() => {});
  }, []);

  if (!mods)
    return (
      <>
        <SectionHeader icon={Boxes} titleKey="admin.modules.title" descKey="admin.modules.description" />
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </>
    );

  async function toggle(mod: AdminModule, enabled: boolean) {
    if (mod.system) return;
    setMods((prev) => prev?.map((m) => (m.key === mod.key ? { ...m, enabled } : m)) ?? null);
    try { await api.patch(`/api/admin/modules/${mod.key}`, { enabled }); }
    catch { setMods((prev) => prev?.map((m) => (m.key === mod.key ? { ...m, enabled: !enabled } : m)) ?? null); }
  }

  return (
    <>
      <SectionHeader icon={Boxes} titleKey="admin.modules.title" descKey="admin.modules.description" />
      <Card>
        <CardContent className="divide-y divide-border p-0">
          {mods.map((m) => (
            <SettingRow key={m.key} title={m.name} description={m.description}>
              <div className="flex items-center gap-2">
                {m.system && <Badge variant="secondary" className="text-[10px]">{t("admin.modules.system")}</Badge>}
                <Switch checked={m.enabled} disabled={m.system} onCheckedChange={(v) => toggle(m, v)} />
              </div>
            </SettingRow>
          ))}
        </CardContent>
      </Card>
    </>
  );
}
