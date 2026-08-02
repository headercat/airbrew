import { useEffect, useState } from "react";
import { Settings as SettingsIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { SectionHeader, SettingRow } from "./shared";
import { api, type SystemInfo } from "@/lib/api";

export default function AdminSystem() {
  const { t } = useTranslation();
  const [info, setInfo] = useState<SystemInfo | null>(null);
  useEffect(() => {
    api
      .get<SystemInfo>("/api/admin/system")
      .then(setInfo)
      .catch(() => {});
  }, []);

  return (
    <>
      <SectionHeader
        icon={SettingsIcon}
        titleKey="admin.system.title"
        descKey="admin.system.description"
      />

      <Card>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow title={t("admin.system.version")} description="Airbrew">
            <Badge variant="outline">{info ? `v${info.version}` : "—"}</Badge>
          </SettingRow>
          <SettingRow title="Go" description={t("admin.system.runtime")}>
            <Badge variant="outline">{info?.go_version ?? "—"}</Badge>
          </SettingRow>
          <SettingRow title="CPU" description={t("admin.system.cores")}>
            <span className="text-[13px] font-medium tabular-nums">
              {info?.num_cpu ?? "—"}
            </span>
          </SettingRow>
          <SettingRow title={t("admin.system.users")}>
            <span className="text-[13px] font-medium tabular-nums">
              {info?.user_count ?? "—"}
            </span>
          </SettingRow>
          <SettingRow title={t("admin.system.modules")}>
            <span className="text-[13px] font-medium tabular-nums">
              {info ? `${info.modules_enabled}/${info.modules_total}` : "—"}
            </span>
          </SettingRow>
          <SettingRow title={t("admin.security.sessions")}>
            <span className="text-[13px] font-medium tabular-nums">
              {info?.active_sessions ?? "—"}
            </span>
          </SettingRow>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow
            title={t("admin.system.dbPath")}
            description={t("admin.system.dbPathDesc")}
          >
            <Badge variant="outline">{info?.db_path ?? "—"}</Badge>
          </SettingRow>
          <SettingRow
            title={t("admin.system.dbSize")}
            description={t("admin.system.dbSizeDesc")}
          >
            <span className="text-[13px] font-medium tabular-nums">
              {info ? `${info.db_size_mb} MB` : "—"}
            </span>
          </SettingRow>
          <SettingRow
            title={t("admin.system.backup")}
            description={t("admin.system.backupDesc")}
          >
            <Badge variant="outline">{t("admin.security.comingSoon")}</Badge>
          </SettingRow>
        </CardContent>
      </Card>
    </>
  );
}
