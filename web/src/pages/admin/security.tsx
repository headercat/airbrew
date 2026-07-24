import { useCallback, useEffect, useState } from "react";
import { ShieldCheck, Users, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { SectionHeader, SettingRow } from "./shared";
import { api, isApiError, type AdminSession } from "@/lib/api";

export default function AdminSecurity() {
  const { t } = useTranslation();
  const [sessions, setSessions] = useState<AdminSession[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const res = await api.get<{ sessions: AdminSession[] }>("/api/admin/sessions");
      setSessions(res.sessions);
    } catch (err) {
      setError(isApiError(err) ? err.error_description ?? err.error : "error");
    }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);

  async function revoke(id: string) {
    try {
      await api.del(`/api/admin/sessions/${id}`);
      void refresh();
    } catch {
      void refresh();
    }
  }

  return (
    <>
      <SectionHeader icon={ShieldCheck} titleKey="admin.security.title" descKey="admin.security.description" />
      {error && <p className="text-sm text-destructive">{error}</p>}

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">{t("admin.security.sessions")}</CardTitle>
          <CardDescription className="text-xs">{t("admin.security.sessionsDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {sessions && sessions.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow className="border-border">
                  <TableHead className="text-xs">{t("admin.security.colUser")}</TableHead>
                  <TableHead className="text-xs">IP</TableHead>
                  <TableHead className="text-xs">{t("admin.security.colCreated")}</TableHead>
                  <TableHead className="text-xs">{t("admin.security.colExpires")}</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.map((s) => (
                  <TableRow key={s.id} className="border-border">
                    <TableCell>
                      <div className="text-[13px] font-medium">{s.user_email}</div>
                      <div className="max-w-[200px] truncate text-xs text-muted-foreground">{s.user_agent}</div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{s.ip_address || "—"}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(s.created_at).toLocaleString()}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{new Date(s.expires_at).toLocaleString()}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground hover:text-destructive" onClick={() => revoke(s.id)}>
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : sessions ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("admin.security.noSessions")}</p>
          ) : (
            <p className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
              <Users className="h-4 w-4 animate-pulse" /> {t("common.loading")}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">{t("admin.security.passwordPolicy")}</CardTitle>
          <CardDescription className="text-xs">{t("admin.security.passwordPolicyDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow title={t("admin.security.minLength")} description={t("admin.security.minLengthDesc")}>
            <Badge variant="outline">8</Badge>
          </SettingRow>
          <SettingRow title={t("admin.security.requireUppercase")} description={t("admin.security.comingSoon")}>
            <Switch disabled />
          </SettingRow>
          <SettingRow title={t("admin.security.sessionTimeout")} description={t("admin.security.comingSoon")}>
            <Badge variant="outline">14d</Badge>
          </SettingRow>
          <SettingRow title={t("admin.security.ipAllowlist")} description={t("admin.security.comingSoon")}>
            <Switch disabled />
          </SettingRow>
        </CardContent>
      </Card>
    </>
  );
}
