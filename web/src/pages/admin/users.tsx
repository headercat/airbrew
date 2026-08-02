import { useCallback, useEffect, useState } from "react";
import { RotateCcw, Search, Users } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { SectionHeader } from "./shared";
import { api, isApiError, type AdminUser } from "@/lib/api";

export default function AdminUsers() {
  const { t } = useTranslation();
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [search, setSearch] = useState("");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const q = search ? `?search=${encodeURIComponent(search)}` : "";
      const res = await api.get<{ users: AdminUser[] }>(`/api/admin/users${q}`);
      setUsers(res.users);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "error",
      );
    }
  }, [search]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

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
      if (res.temp_password)
        alert(t("admin.users.tempPassword", { pw: res.temp_password }));
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
      <Card>
        <CardContent className="p-0">
          <div className="flex items-center gap-2 border-b border-border p-3">
            <div className="relative flex-1">
              <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                placeholder={t("admin.users.searchPlaceholder")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-8 pl-8 text-[13px]"
              />
            </div>
          </div>
          {users && (
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
                      <select
                        className="rounded-md border border-input bg-background px-2 py-1 text-xs"
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
                    </TableCell>
                    <TableCell>
                      <select
                        className="rounded-md border border-input bg-background px-2 py-1 text-xs"
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
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(u.created_at).toLocaleDateString()}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        onClick={() => resetPw(u)}
                      >
                        <RotateCcw className="h-3 w-3" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
