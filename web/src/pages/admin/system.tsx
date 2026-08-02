import { useEffect, useState } from "react";
import { CheckCircle2, Download, Settings as SettingsIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { SectionHeader, SettingRow } from "./shared";
import { api, type BackupVerification, type SystemInfo } from "@/lib/api";

export default function AdminSystem() {
  const { t } = useTranslation();
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [backupCheck, setBackupCheck] = useState<BackupVerification | null>(
    null,
  );
  const [verifying, setVerifying] = useState(false);
  useEffect(() => {
    api
      .get<SystemInfo>("/api/admin/system")
      .then(setInfo)
      .catch(() => {});
  }, []);

  async function verifyBackup() {
    setVerifying(true);
    try {
      const res = await api.get<BackupVerification>(
        "/api/admin/system/backup/verify",
      );
      setBackupCheck(res);
    } finally {
      setVerifying(false);
    }
  }

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
          <SettingRow title={t("admin.system.startedAt")}>
            <span className="text-[13px] font-medium">
              {info ? new Date(info.started_at).toLocaleString() : "—"}
            </span>
          </SettingRow>
          <SettingRow title={t("admin.system.uptime")}>
            <span className="text-[13px] font-medium tabular-nums">
              {info ? formatUptime(info.uptime_seconds) : "—"}
            </span>
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
            title={t("admin.system.dataDir")}
            description={t("admin.system.dataDirDesc")}
          >
            <span className="max-w-[360px] truncate text-[13px] font-medium">
              {info?.data_dir || "—"}
            </span>
          </SettingRow>
          <SettingRow title={t("admin.system.migrationVersion")}>
            <Badge variant="outline">{info?.migration_version || "—"}</Badge>
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
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={verifyBackup}
                disabled={verifying}
              >
                <CheckCircle2 className="h-4 w-4" />
                {verifying
                  ? t("common.loading")
                  : t("admin.system.verifyBackup")}
              </Button>
              <Button asChild size="sm" variant="outline">
                <a href="/api/admin/system/backup">
                  <Download className="h-4 w-4" />
                  {t("admin.system.downloadBackup")}
                </a>
              </Button>
            </div>
          </SettingRow>
          {backupCheck && (
            <SettingRow
              title={t("admin.system.backupVerification")}
              description={new Date(backupCheck.generated_at).toLocaleString()}
            >
              <div className="max-w-[420px] space-y-1 text-right text-xs text-muted-foreground">
                <div className="font-medium text-foreground">
                  {backupCheck.ok
                    ? t("admin.system.backupOK")
                    : t("admin.system.backupFailed")}
                </div>
                <div>
                  {t("admin.system.backupChecks", {
                    integrity: backupCheck.integrity_check,
                    quick: backupCheck.quick_check,
                  })}
                </div>
                <div className="font-mono break-all">{backupCheck.sha256}</div>
                <div>{formatBytes(backupCheck.size_bytes)}</div>
              </div>
            </SettingRow>
          )}
        </CardContent>
      </Card>
    </>
  );
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h ${minutes}m`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}

function formatBytes(bytes: number) {
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
}
