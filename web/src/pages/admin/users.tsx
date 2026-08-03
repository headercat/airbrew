import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ChevronLeft,
  ChevronRight,
  Eye,
  RotateCcw,
  Search,
  Trash2,
  UserPlus,
  Users,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { SectionHeader } from "./shared";
import {
  api,
  isApiError,
  type AdminSession,
  type AdminUser,
  type AuditEntry,
} from "@/lib/api";

type CreateUserForm = {
  email: string;
  display_name: string;
  role: "user" | "admin";
  password: string;
};

const blankCreateForm: CreateUserForm = {
  email: "",
  display_name: "",
  role: "user",
  password: "",
};

const pageSize = 50;

export default function AdminUsers() {
  const { t } = useTranslation();
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [search, setSearch] = useState("");
  const [includeDeleted, setIncludeDeleted] = useState(false);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState<CreateUserForm>(blankCreateForm);
  const [selected, setSelected] = useState<AdminUser | null>(null);
  const [detail, setDetail] = useState<{
    sessions: AdminSession[];
    activity: AuditEntry[];
  } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const params = new URLSearchParams();
      if (search) params.set("search", search);
      if (includeDeleted) params.set("include_deleted", "true");
      params.set("limit", String(pageSize));
      params.set("offset", String(offset));
      const q = params.toString() ? `?${params.toString()}` : "";
      const res = await api.get<{
        users: AdminUser[];
        total: number;
        limit: number;
        offset: number;
      }>(`/api/admin/users${q}`);
      setUsers(res.users);
      setTotal(res.total);
      setError(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }, [includeDeleted, offset, search]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const selectedTitle = useMemo(
    () => selected?.display_name || selected?.email || "",
    [selected],
  );
  const pageStart = total === 0 ? 0 : offset + 1;
  const pageEnd = Math.min(offset + (users?.length ?? 0), total);
  const canPageBack = offset > 0;
  const canPageForward = offset + pageSize < total;

  async function createUser() {
    try {
      const res = await api.post<{
        user: AdminUser;
        temp_password?: string;
      }>("/api/admin/users", createForm);
      setCreateOpen(false);
      setCreateForm(blankCreateForm);
      setNotice(
        res.temp_password
          ? t("admin.users.createdWithPassword", { pw: res.temp_password })
          : t("admin.users.created"),
      );
      await refresh();
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function updateRole(u: AdminUser, role: "user" | "admin") {
    if (u.role === role) return;
    try {
      await api.patch(`/api/admin/users/${u.id}`, { role });
      await refresh();
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function updateStatus(u: AdminUser, status: "active" | "suspended") {
    if (u.status === status) return;
    try {
      await api.patch(`/api/admin/users/${u.id}`, { status });
      await refresh();
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function resetPw(u: AdminUser) {
    if (!confirm(t("admin.users.confirmReset", { email: u.email }))) return;
    try {
      const res = await api.post<{ temp_password?: string }>(
        `/api/admin/users/${u.id}/reset-password`,
        {},
      );
      if (res.temp_password) {
        setNotice(t("admin.users.tempPassword", { pw: res.temp_password }));
      }
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function softDelete(u: AdminUser) {
    if (!confirm(t("admin.users.confirmDelete", { email: u.email }))) return;
    try {
      await api.del(`/api/admin/users/${u.id}`);
      await refresh();
      if (selected?.id === u.id) setSelected(null);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function restoreUser(u: AdminUser) {
    if (!confirm(t("admin.users.confirmRestore", { email: u.email }))) return;
    try {
      await api.post(`/api/admin/users/${u.id}/restore`, {});
      await refresh();
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function openDetail(u: AdminUser) {
    setSelected(u);
    setDetail(null);
    try {
      const [sessionsRes, activityRes] = await Promise.all([
        api.get<{ sessions: AdminSession[] }>(
          `/api/admin/users/${u.id}/sessions`,
        ),
        api.get<{ entries: AuditEntry[] }>(`/api/admin/users/${u.id}/activity`),
      ]);
      setDetail({
        sessions: sessionsRes.sessions,
        activity: activityRes.entries,
      });
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  async function revokeUserSessions(u: AdminUser) {
    if (!confirm(t("admin.users.confirmRevokeSessions", { email: u.email }))) {
      return;
    }
    try {
      await api.del(`/api/admin/users/${u.id}/sessions`);
      await openDetail(u);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }

  return (
    <>
      <SectionHeader
        icon={Users}
        titleKey="admin.users.title"
        descKey="admin.users.description"
      />
      {error && <p className="mb-4 text-sm text-destructive">{error}</p>}
      {notice && (
        <p className="mb-4 whitespace-pre-wrap text-sm text-emerald-600">
          {notice}
        </p>
      )}
      <Card>
        <CardContent className="p-0">
          <div className="flex items-center gap-2 border-b border-border p-3">
            <div className="relative flex-1">
              <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                placeholder={t("admin.users.searchPlaceholder")}
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setOffset(0);
                }}
                className="h-8 pl-8 text-[13px]"
              />
            </div>
            <label className="flex items-center gap-2 whitespace-nowrap text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={includeDeleted}
                onChange={(e) => {
                  setIncludeDeleted(e.target.checked);
                  setOffset(0);
                }}
              />
              {t("admin.users.includeDeleted")}
            </label>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <UserPlus className="h-4 w-4" />
              {t("admin.users.create")}
            </Button>
          </div>
          {users && users.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow className="border-border">
                  <TableHead className="text-xs">
                    {t("admin.users.colEmail")}
                  </TableHead>
                  <TableHead className="text-xs">
                    {t("admin.users.colRole")}
                  </TableHead>
                  <TableHead className="text-xs">
                    {t("admin.users.colStatus")}
                  </TableHead>
                  <TableHead className="text-xs">
                    {t("admin.users.colCreated")}
                  </TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <TableRow key={u.id} className="border-border">
                    <TableCell>
                      <div className="text-[13px] font-medium">{u.email}</div>
                      {u.display_name && (
                        <div className="text-xs text-muted-foreground">
                          {u.display_name}
                        </div>
                      )}
                    </TableCell>
                    <TableCell>
                      {u.status === "deleted" ? (
                        <span className="text-xs text-muted-foreground">
                          {u.role}
                        </span>
                      ) : (
                        <select
                          className="h-8 rounded-md border border-input bg-background px-2 text-xs"
                          value={u.role}
                          onChange={(e) =>
                            updateRole(u, e.target.value as "user" | "admin")
                          }
                        >
                          <option value="user">
                            {t("admin.users.roleUser")}
                          </option>
                          <option value="admin">
                            {t("admin.users.roleAdmin")}
                          </option>
                        </select>
                      )}
                    </TableCell>
                    <TableCell>
                      {u.status === "deleted" ? (
                        <div>
                          <div className="text-xs font-medium text-destructive">
                            {t("admin.users.statusDeleted")}
                          </div>
                          {u.deleted_at && (
                            <div className="text-xs text-muted-foreground">
                              {new Date(u.deleted_at).toLocaleDateString()}
                            </div>
                          )}
                        </div>
                      ) : (
                        <select
                          className="h-8 rounded-md border border-input bg-background px-2 text-xs"
                          value={u.status}
                          onChange={(e) =>
                            updateStatus(
                              u,
                              e.target.value as "active" | "suspended",
                            )
                          }
                        >
                          <option value="active">
                            {t("admin.users.statusActive")}
                          </option>
                          <option value="suspended">
                            {t("admin.users.statusSuspended")}
                          </option>
                        </select>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(u.created_at).toLocaleDateString()}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          onClick={() => openDetail(u)}
                          title={t("common.open")}
                        >
                          <Eye className="h-3 w-3" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          onClick={() => resetPw(u)}
                          title={t("admin.users.resetPassword")}
                          disabled={u.status === "deleted"}
                        >
                          <RotateCcw className="h-3 w-3" />
                        </Button>
                        {u.status === "deleted" ? (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground hover:text-primary"
                            onClick={() => restoreUser(u)}
                            title={t("admin.users.restore")}
                          >
                            <RotateCcw className="h-3 w-3" />
                          </Button>
                        ) : (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground hover:text-destructive"
                            onClick={() => softDelete(u)}
                            title={t("admin.users.delete")}
                          >
                            <Trash2 className="h-3 w-3" />
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : users ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("admin.users.noUsers")}
            </p>
          ) : (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("common.loading")}
            </p>
          )}
          {users && (
            <div className="flex flex-col gap-2 border-t border-border px-3 py-3 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
              <span>
                {t("admin.users.pageSummary", {
                  start: pageStart,
                  end: pageEnd,
                  total,
                })}
              </span>
              <div className="flex items-center gap-1">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!canPageBack}
                  onClick={() => setOffset(Math.max(0, offset - pageSize))}
                >
                  <ChevronLeft className="h-4 w-4" />
                  {t("admin.users.previousPage")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!canPageForward}
                  onClick={() => setOffset(offset + pageSize)}
                >
                  {t("admin.users.nextPage")}
                  <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title={t("admin.users.create")}
        description={t("admin.users.createDesc")}
      >
        <div className="space-y-3">
          <Input
            placeholder={t("admin.users.email")}
            value={createForm.email}
            onChange={(e) =>
              setCreateForm((f) => ({ ...f, email: e.target.value }))
            }
          />
          <Input
            placeholder={t("admin.users.displayName")}
            value={createForm.display_name}
            onChange={(e) =>
              setCreateForm((f) => ({ ...f, display_name: e.target.value }))
            }
          />
          <select
            className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={createForm.role}
            onChange={(e) =>
              setCreateForm((f) => ({
                ...f,
                role: e.target.value as "user" | "admin",
              }))
            }
          >
            <option value="user">{t("admin.users.roleUser")}</option>
            <option value="admin">{t("admin.users.roleAdmin")}</option>
          </select>
          <Input
            placeholder={t("admin.users.passwordPlaceholder")}
            type="password"
            value={createForm.password}
            onChange={(e) =>
              setCreateForm((f) => ({ ...f, password: e.target.value }))
            }
          />
          <Button className="w-full" onClick={createUser}>
            <UserPlus className="h-4 w-4" />
            {t("admin.users.create")}
          </Button>
        </div>
      </Modal>

      <Modal
        open={!!selected}
        onClose={() => setSelected(null)}
        title={selectedTitle}
        description={selected?.email}
        className="max-w-2xl"
      >
        {selected && (
          <div className="space-y-5">
            <div className="grid gap-3 text-sm sm:grid-cols-3">
              <InfoPill
                label={t("admin.users.colRole")}
                value={selected.role}
              />
              <InfoPill
                label={t("admin.users.colStatus")}
                value={selected.status}
              />
              <InfoPill
                label={t("admin.users.colCreated")}
                value={new Date(selected.created_at).toLocaleString()}
              />
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm font-medium">
                  {t("admin.users.sessions")}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => revokeUserSessions(selected)}
                >
                  <Trash2 className="h-4 w-4" />
                  {t("admin.users.revokeSessions")}
                </Button>
              </div>
              {detail?.sessions.length ? (
                <div className="max-h-40 overflow-auto rounded-md border">
                  {detail.sessions.map((s) => (
                    <div
                      key={s.id}
                      className="border-b px-3 py-2 last:border-b-0"
                    >
                      <div className="text-xs font-medium">
                        {s.ip_address || "—"}
                      </div>
                      <div className="truncate text-xs text-muted-foreground">
                        {s.user_agent || "—"}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="rounded-md border px-3 py-4 text-center text-xs text-muted-foreground">
                  {detail
                    ? t("admin.security.noSessions")
                    : t("common.loading")}
                </p>
              )}
            </div>

            <div className="space-y-2">
              <p className="text-sm font-medium">{t("admin.users.activity")}</p>
              {detail?.activity.length ? (
                <div className="max-h-52 overflow-auto rounded-md border">
                  {detail.activity.map((a) => (
                    <div
                      key={a.id}
                      className="border-b px-3 py-2 last:border-b-0"
                    >
                      <div className="flex items-center justify-between gap-3">
                        <span className="text-xs font-medium">
                          {a.event_type}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          {new Date(a.created_at).toLocaleString()}
                        </span>
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {a.actor_email || a.actor_user_id || "system"}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="rounded-md border px-3 py-4 text-center text-xs text-muted-foreground">
                  {detail ? t("admin.users.noActivity") : t("common.loading")}
                </p>
              )}
            </div>
          </div>
        )}
      </Modal>
    </>
  );
}

function InfoPill({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-1 truncate text-xs font-medium">{value}</div>
    </div>
  );
}
