import { useEffect, useRef, useState } from "react";
import {
  CheckCircle2,
  Download,
  FileCheck2,
  Plus,
  Settings as SettingsIcon,
  Trash2,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { SectionHeader, SettingRow } from "./shared";
import {
  api,
  isApiError,
  type BackupVerification,
  type RestoreStage,
  type StoredBackup,
  type SystemInfo,
} from "@/lib/api";

export default function AdminSystem() {
  const { t } = useTranslation();
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [backupCheck, setBackupCheck] = useState<BackupVerification | null>(
    null,
  );
  const [restoreStage, setRestoreStage] = useState<RestoreStage | null>(null);
  const [storedBackups, setStoredBackups] = useState<StoredBackup[]>([]);
  const [verifying, setVerifying] = useState(false);
  const [creatingBackup, setCreatingBackup] = useState(false);
  const [restoreMode, setRestoreMode] = useState<"dry-run" | "stage">(
    "dry-run",
  );
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const restoreInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    api
      .get<SystemInfo>("/api/admin/system")
      .then(setInfo)
      .catch(() => {});
    void refreshStoredBackups();
  }, []);

  async function refreshStoredBackups() {
    try {
      const res = await api.get<{ backups: StoredBackup[]; retention: number }>(
        "/api/admin/system/backups",
      );
      setStoredBackups(res.backups);
    } catch {
      setStoredBackups([]);
    }
  }

  async function createStoredBackup() {
    setCreatingBackup(true);
    try {
      await api.post<StoredBackup>("/api/admin/system/backups");
      await refreshStoredBackups();
      setNotice(t("admin.system.storedBackupCreated"));
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    } finally {
      setCreatingBackup(false);
    }
  }

  async function deleteStoredBackup(name: string) {
    if (!window.confirm(t("admin.system.confirmDeleteBackup", { name }))) {
      return;
    }
    try {
      await api.del(`/api/admin/system/backups/${encodeURIComponent(name)}`);
      await refreshStoredBackups();
      setNotice(t("admin.system.storedBackupDeleted"));
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

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

  async function restoreDryRun(file?: File) {
    if (!file) return;
    setVerifying(true);
    try {
      if (restoreMode === "stage") {
        const res = await api.upload<RestoreStage>(
          "/api/admin/system/backup/restore-stage",
          file,
        );
        setRestoreStage(res);
        setBackupCheck(res.verification);
        setNotice(t("admin.system.restoreStaged"));
        setError(null);
      } else {
        const res = await api.upload<BackupVerification>(
          "/api/admin/system/backup/restore-dry-run",
          file,
        );
        setBackupCheck(res);
      }
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    } finally {
      setVerifying(false);
      if (restoreInput.current) restoreInput.current.value = "";
    }
  }

  return (
    <>
      <SectionHeader
        icon={SettingsIcon}
        titleKey="admin.system.title"
        descKey="admin.system.description"
      />
      {error && <p className="text-sm text-destructive">{error}</p>}
      {notice && <p className="text-sm text-emerald-600">{notice}</p>}

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
            title={t("admin.system.diskFree")}
            description={t("admin.system.diskFreeDesc")}
          >
            <span className="text-[13px] font-medium tabular-nums">
              {info
                ? `${formatBytes(info.disk_free_bytes)} / ${formatBytes(info.disk_total_bytes)}`
                : "—"}
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
              <input
                ref={restoreInput}
                type="file"
                accept=".sqlite,.sqlite3,.db,application/vnd.sqlite3"
                className="hidden"
                onChange={(e) => void restoreDryRun(e.currentTarget.files?.[0])}
              />
              <Button
                size="sm"
                variant="outline"
                disabled={verifying}
                onClick={() => {
                  setRestoreMode("dry-run");
                  restoreInput.current?.click();
                }}
              >
                <FileCheck2 className="h-4 w-4" />
                {t("admin.system.restoreDryRun")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={creatingBackup}
                onClick={createStoredBackup}
              >
                <Plus className="h-4 w-4" />
                {creatingBackup
                  ? t("common.loading")
                  : t("admin.system.createStoredBackup")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={verifying}
                onClick={() => {
                  if (!window.confirm(t("admin.system.confirmStageRestore"))) {
                    return;
                  }
                  setRestoreMode("stage");
                  restoreInput.current?.click();
                }}
              >
                <FileCheck2 className="h-4 w-4" />
                {t("admin.system.stageRestore")}
              </Button>
            </div>
          </SettingRow>
          {restoreStage && (
            <SettingRow
              title={t("admin.system.pendingRestore")}
              description={new Date(restoreStage.staged_at).toLocaleString()}
            >
              <div className="max-w-[520px] space-y-1 text-right text-xs text-muted-foreground">
                <div className="font-medium text-foreground">
                  {restoreStage.restart_message}
                </div>
                <div className="font-mono break-all">
                  {restoreStage.pending_path}
                </div>
                <div className="font-mono break-all">
                  {restoreStage.manifest_path}
                </div>
              </div>
            </SettingRow>
          )}
          <SettingRow
            title={t("admin.system.storedBackups")}
            description={t("admin.system.storedBackupsDesc")}
          >
            <div className="flex max-w-[560px] flex-col items-end gap-2">
              {storedBackups.length === 0 ? (
                <span className="text-xs text-muted-foreground">
                  {t("admin.system.noStoredBackups")}
                </span>
              ) : (
                storedBackups.map((backup) => (
                  <div
                    key={backup.name}
                    className="flex flex-wrap items-center justify-end gap-2 text-right text-xs text-muted-foreground"
                  >
                    <span className="font-mono text-foreground">
                      {backup.name}
                    </span>
                    <span>{formatBytes(backup.size_bytes)}</span>
                    <span>{new Date(backup.created_at).toLocaleString()}</span>
                    <Button asChild size="icon" variant="ghost">
                      <a
                        href={`/api/admin/system/backups/${encodeURIComponent(
                          backup.name,
                        )}`}
                        title={t("admin.system.downloadBackup")}
                      >
                        <Download className="h-4 w-4" />
                      </a>
                    </Button>
                    <Button
                      size="icon"
                      variant="ghost"
                      title={t("common.delete")}
                      onClick={() => void deleteStoredBackup(backup.name)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))
              )}
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
                <div>
                  {t("admin.system.backupSchema", {
                    schema: backupCheck.schema_check || "—",
                  })}
                </div>
                <div>
                  {t("admin.system.backupMigration", {
                    version: backupCheck.migration_version || "—",
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
