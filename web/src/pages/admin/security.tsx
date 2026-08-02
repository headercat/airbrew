import { useCallback, useEffect, useState } from "react";
import {
  ChevronLeft,
  ChevronRight,
  Download,
  History,
  Save,
  ShieldCheck,
  Trash2,
  Users,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { SectionHeader, SettingRow } from "./shared";
import {
  api,
  isApiError,
  type AdminSession,
  type IPAllowlist,
  type LoginAttempt,
  type PasswordPolicy,
} from "@/lib/api";

export default function AdminSecurity() {
  const { t } = useTranslation();
  const [sessions, setSessions] = useState<AdminSession[] | null>(null);
  const [policy, setPolicy] = useState<PasswordPolicy | null>(null);
  const [allowlist, setAllowlist] = useState<IPAllowlist | null>(null);
  const [cidrsText, setCidrsText] = useState("");
  const [trustedProxiesText, setTrustedProxiesText] = useState("");
  const [attempts, setAttempts] = useState<LoginAttempt[] | null>(null);
  const [loginResult, setLoginResult] = useState("");
  const [loginEmail, setLoginEmail] = useState("");
  const [loginFrom, setLoginFrom] = useState("");
  const [loginTo, setLoginTo] = useState("");
  const [loginTotal, setLoginTotal] = useState(0);
  const [loginOffset, setLoginOffset] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [res, nextPolicy, nextAllowlist] = await Promise.all([
        api.get<{ sessions: AdminSession[] }>("/api/admin/sessions"),
        api.get<PasswordPolicy>("/api/admin/security/password-policy"),
        api.get<IPAllowlist>("/api/admin/security/ip-allowlist"),
      ]);
      setSessions(res.sessions);
      setPolicy(nextPolicy);
      setAllowlist(nextAllowlist);
      setCidrsText(nextAllowlist.cidrs.join("\n"));
      setTrustedProxiesText((nextAllowlist.trusted_proxies ?? []).join("\n"));
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const refreshLoginHistory = useCallback(async () => {
    const params = loginHistoryParams({
      email: loginEmail,
      from: loginFrom,
      limit: loginPageSize,
      offset: loginOffset,
      result: loginResult,
      to: loginTo,
    });
    try {
      const res = await api.get<{
        entries: LoginAttempt[];
        total: number;
        limit: number;
        offset: number;
      }>(
        `/api/admin/security/login-history?${params.toString()}`,
      );
      setAttempts(res.entries);
      setLoginTotal(res.total);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }, [loginEmail, loginFrom, loginOffset, loginResult, loginTo]);

  useEffect(() => {
    void refreshLoginHistory();
  }, [refreshLoginHistory]);

  async function revoke(id: string) {
    try {
      await api.del(`/api/admin/sessions/${id}`);
      void refresh();
    } catch {
      void refresh();
    }
  }

  async function savePolicy() {
    if (!policy) return;
    try {
      const saved = await api.putRaw<PasswordPolicy>(
        "/api/admin/security/password-policy",
        policy,
      );
      setPolicy(saved);
      setNotice(t("admin.security.saved"));
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function saveAllowlist() {
    if (!allowlist) return;
    const cidrs = cidrsText
      .split(/\r?\n/)
      .map((v) => v.trim())
      .filter(Boolean);
    const trustedProxies = trustedProxiesText
      .split(/\r?\n/)
      .map((v) => v.trim())
      .filter(Boolean);
    try {
      const saved = await api.putRaw<IPAllowlist>(
        "/api/admin/security/ip-allowlist",
        { ...allowlist, cidrs, trusted_proxies: trustedProxies },
      );
      setAllowlist(saved);
      setCidrsText(saved.cidrs.join("\n"));
      setTrustedProxiesText((saved.trusted_proxies ?? []).join("\n"));
      setNotice(t("admin.security.saved"));
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function exportLoginHistoryCSV() {
    const entries = await fetchAllLoginAttempts({
      email: loginEmail,
      from: loginFrom,
      result: loginResult,
      to: loginTo,
    });
    downloadCSV("airbrew-login-history.csv", [
      [
        "created_at",
        "success",
        "email",
        "user_id",
        "ip_address",
        "user_agent",
        "failure",
      ],
      ...entries.map((a) => [
        a.created_at,
        String(a.success),
        a.email,
        a.user_id,
        a.ip_address,
        a.user_agent,
        a.failure ?? "",
      ]),
    ]);
  }

  const loginStart = loginTotal === 0 ? 0 : loginOffset + 1;
  const loginEnd = Math.min(loginOffset + (attempts?.length ?? 0), loginTotal);
  const canLoginPageBack = loginOffset > 0;
  const canLoginPageForward = loginOffset + loginPageSize < loginTotal;

  return (
    <>
      <SectionHeader
        icon={ShieldCheck}
        titleKey="admin.security.title"
        descKey="admin.security.description"
      />
      {error && <p className="text-sm text-destructive">{error}</p>}
      {notice && <p className="text-sm text-emerald-600">{notice}</p>}

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">
            {t("admin.security.sessions")}
          </CardTitle>
          <CardDescription className="text-xs">
            {t("admin.security.sessionsDesc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {sessions && sessions.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow className="border-border">
                  <TableHead className="text-xs">
                    {t("admin.security.colUser")}
                  </TableHead>
                  <TableHead className="text-xs">IP</TableHead>
                  <TableHead className="text-xs">
                    {t("admin.security.colCreated")}
                  </TableHead>
                  <TableHead className="text-xs">
                    {t("admin.security.colExpires")}
                  </TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.map((s) => (
                  <TableRow key={s.id} className="border-border">
                    <TableCell>
                      <div className="text-[13px] font-medium">
                        {s.user_email}
                      </div>
                      <div className="max-w-[200px] truncate text-xs text-muted-foreground">
                        {s.user_agent}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {s.ip_address || "—"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(s.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(s.expires_at).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        onClick={() => revoke(s.id)}
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : sessions ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("admin.security.noSessions")}
            </p>
          ) : (
            <p className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
              <Users className="h-4 w-4 animate-pulse" /> {t("common.loading")}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <div>
              <CardTitle className="text-sm">
                {t("admin.security.passwordPolicy")}
              </CardTitle>
              <CardDescription className="text-xs">
                {t("admin.security.passwordPolicyDesc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={savePolicy} disabled={!policy}>
              <Save className="h-4 w-4" />
              {t("common.save")}
            </Button>
          </div>
        </CardHeader>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow
            title={t("admin.security.minLength")}
            description={t("admin.security.minLengthDesc")}
          >
            <Input
              className="h-8 w-20"
              min={1}
              max={128}
              type="number"
              value={policy?.min_length ?? 8}
              onChange={(e) =>
                setPolicy((p) =>
                  p ? { ...p, min_length: Number(e.target.value) } : p,
                )
              }
            />
          </SettingRow>
          <PolicySwitch
            title={t("admin.security.requireUppercase")}
            checked={policy?.require_uppercase ?? false}
            onCheckedChange={(checked) =>
              setPolicy((p) => (p ? { ...p, require_uppercase: checked } : p))
            }
          />
          <PolicySwitch
            title={t("admin.security.requireLowercase")}
            checked={policy?.require_lowercase ?? false}
            onCheckedChange={(checked) =>
              setPolicy((p) => (p ? { ...p, require_lowercase: checked } : p))
            }
          />
          <PolicySwitch
            title={t("admin.security.requireDigit")}
            checked={policy?.require_digit ?? false}
            onCheckedChange={(checked) =>
              setPolicy((p) => (p ? { ...p, require_digit: checked } : p))
            }
          />
          <PolicySwitch
            title={t("admin.security.requireSymbol")}
            checked={policy?.require_symbol ?? false}
            onCheckedChange={(checked) =>
              setPolicy((p) => (p ? { ...p, require_symbol: checked } : p))
            }
          />
          <SettingRow
            title={t("admin.security.maxAge")}
            description={t("admin.security.maxAgeDesc")}
          >
            <Input
              className="h-8 w-24"
              min={0}
              max={3650}
              type="number"
              value={policy?.max_age_days ?? 0}
              onChange={(e) =>
                setPolicy((p) =>
                  p ? { ...p, max_age_days: Number(e.target.value) } : p,
                )
              }
            />
          </SettingRow>
          <SettingRow
            title={t("admin.security.history")}
            description={t("admin.security.historyDesc")}
          >
            <Input
              className="h-8 w-20"
              min={0}
              max={24}
              type="number"
              value={policy?.history_count ?? 0}
              onChange={(e) =>
                setPolicy((p) =>
                  p ? { ...p, history_count: Number(e.target.value) } : p,
                )
              }
            />
          </SettingRow>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <div>
              <CardTitle className="text-sm">
                {t("admin.security.ipAllowlist")}
              </CardTitle>
              <CardDescription className="text-xs">
                {t("admin.security.ipAllowlistDesc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={saveAllowlist} disabled={!allowlist}>
              <Save className="h-4 w-4" />
              {t("common.save")}
            </Button>
          </div>
        </CardHeader>
        <CardContent className="divide-y divide-border p-0">
          <SettingRow
            title={t("admin.security.ipAllowlistEnabled")}
            description={t("admin.security.ipAllowlistEnabledDesc")}
          >
            <Switch
              checked={allowlist?.enabled ?? false}
              onCheckedChange={(checked) =>
                setAllowlist((a) => (a ? { ...a, enabled: checked } : a))
              }
            />
          </SettingRow>
          <div className="space-y-2 px-4 py-5 lg:px-6">
            <div>
              <p className="text-[13px] font-medium">
                {t("admin.security.allowedNetworks")}
              </p>
              <p className="text-xs text-muted-foreground">
                {t("admin.security.allowedNetworksDesc")}
              </p>
            </div>
            <Textarea
              className="min-h-24 font-mono text-xs"
              placeholder={"192.168.0.0/24\n10.0.0.5"}
              value={cidrsText}
              onChange={(e) => setCidrsText(e.target.value)}
            />
          </div>
          <div className="space-y-2 px-4 py-5 lg:px-6">
            <div>
              <p className="text-[13px] font-medium">
                {t("admin.security.trustedProxies")}
              </p>
              <p className="text-xs text-muted-foreground">
                {t("admin.security.trustedProxiesDesc")}
              </p>
            </div>
            <Textarea
              className="min-h-20 font-mono text-xs"
              placeholder={"127.0.0.1\n10.0.0.0/8"}
              value={trustedProxiesText}
              onChange={(e) => setTrustedProxiesText(e.target.value)}
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <History className="h-4 w-4" />
            {t("admin.security.loginHistory")}
          </CardTitle>
          <CardDescription className="text-xs">
            {t("admin.security.loginHistoryDesc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_160px_190px_190px_auto_auto]">
            <Input
              placeholder={t("admin.security.emailFilter")}
              value={loginEmail}
              onChange={(e) => {
                setLoginEmail(e.target.value);
                setLoginOffset(0);
              }}
            />
            <select
              className="h-10 rounded-md border border-input bg-background px-3 text-sm"
              value={loginResult}
              onChange={(e) => {
                setLoginResult(e.target.value);
                setLoginOffset(0);
              }}
            >
              <option value="">{t("admin.security.allResults")}</option>
              <option value="success">{t("admin.security.success")}</option>
              <option value="failure">{t("admin.security.failure")}</option>
            </select>
            <Input
              type="datetime-local"
              value={loginFrom}
              onChange={(e) => {
                setLoginFrom(e.target.value);
                setLoginOffset(0);
              }}
              aria-label={t("admin.security.from")}
            />
            <Input
              type="datetime-local"
              value={loginTo}
              onChange={(e) => {
                setLoginTo(e.target.value);
                setLoginOffset(0);
              }}
              aria-label={t("admin.security.to")}
            />
            <Button
              variant="outline"
              onClick={() => {
                setLoginOffset(0);
                void refreshLoginHistory();
              }}
            >
              {t("common.search")}
            </Button>
            <Button variant="outline" onClick={exportLoginHistoryCSV}>
              <Download className="h-4 w-4" />
              {t("admin.security.exportCSV")}
            </Button>
          </div>
          {attempts && attempts.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow className="border-border">
                  <TableHead className="text-xs">
                    {t("admin.security.colResult")}
                  </TableHead>
                  <TableHead className="text-xs">
                    {t("admin.security.colUser")}
                  </TableHead>
                  <TableHead className="text-xs">IP</TableHead>
                  <TableHead className="text-xs">
                    {t("admin.security.colCreated")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {attempts.map((a) => (
                  <TableRow key={a.id} className="border-border">
                    <TableCell>
                      <Badge variant={a.success ? "secondary" : "destructive"}>
                        {a.success
                          ? t("admin.security.success")
                          : t("admin.security.failure")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="text-[13px] font-medium">{a.email}</div>
                      {a.failure && (
                        <div className="text-xs text-muted-foreground">
                          {a.failure}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {a.ip_address || "—"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(a.created_at).toLocaleString()}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : attempts ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("admin.security.noLoginHistory")}
            </p>
          ) : (
            <p className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
              <History className="h-4 w-4 animate-pulse" />{" "}
              {t("common.loading")}
            </p>
          )}
          {attempts && (
            <div className="flex flex-col gap-2 border-t border-border pt-3 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
              <span>
                {t("admin.security.loginPageSummary", {
                  start: loginStart,
                  end: loginEnd,
                  total: loginTotal,
                })}
              </span>
              <div className="flex items-center gap-1">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!canLoginPageBack}
                  onClick={() =>
                    setLoginOffset(Math.max(0, loginOffset - loginPageSize))
                  }
                >
                  <ChevronLeft className="h-4 w-4" />
                  {t("admin.security.previousPage")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!canLoginPageForward}
                  onClick={() => setLoginOffset(loginOffset + loginPageSize)}
                >
                  {t("admin.security.nextPage")}
                  <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>
    </>
  );
}

const loginPageSize = 50;

type LoginHistoryParamInput = {
  email: string;
  from: string;
  limit: number;
  offset: number;
  result: string;
  to: string;
};

function loginHistoryParams(input: LoginHistoryParamInput) {
  const params = new URLSearchParams({
    limit: String(input.limit),
    offset: String(input.offset),
  });
  if (input.result) params.set("result", input.result);
  if (input.email.trim()) params.set("email", input.email.trim());
  if (input.from) params.set("from", new Date(input.from).toISOString());
  if (input.to) params.set("to", new Date(input.to).toISOString());
  return params;
}

async function fetchAllLoginAttempts(input: {
  email: string;
  from: string;
  result: string;
  to: string;
}) {
  const out: LoginAttempt[] = [];
  let offset = 0;
  let total = Number.POSITIVE_INFINITY;
  while (offset < total) {
    const params = loginHistoryParams({
      ...input,
      limit: 200,
      offset,
    });
    const res = await api.get<{ entries: LoginAttempt[]; total: number }>(
      `/api/admin/security/login-history?${params.toString()}`,
    );
    out.push(...res.entries);
    total = res.total;
    if (res.entries.length === 0) break;
    offset += res.entries.length;
  }
  return out;
}

function downloadCSV(filename: string, rows: string[][]) {
  const csv = rows
    .map((row) =>
      row
        .map((cell) => `"${String(cell ?? "").replaceAll(`"`, `""`)}"`)
        .join(","),
    )
    .join("\n");
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

function PolicySwitch({
  title,
  checked,
  onCheckedChange,
}: {
  title: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <SettingRow title={title}>
      <Switch checked={checked} onCheckedChange={onCheckedChange} />
    </SettingRow>
  );
}
