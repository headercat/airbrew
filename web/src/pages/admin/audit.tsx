import { useCallback, useEffect, useState } from "react";
import { Activity } from "lucide-react";
import { useTranslation } from "react-i18next";

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
import { api, type AuditEntry } from "@/lib/api";

export default function AdminAudit() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [filter, setFilter] = useState("");
  const refresh = useCallback(async () => {
    const q = filter ? `?event_type=${encodeURIComponent(filter)}` : "";
    try {
      const res = await api.get<{ entries: AuditEntry[] }>(
        `/api/admin/audit${q}`,
      );
      setEntries(res.entries);
    } catch {
      setEntries([]);
    }
  }, [filter]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <>
      <SectionHeader
        icon={Activity}
        titleKey="admin.audit.title"
        descKey="admin.audit.description"
      />
      <Card>
        <CardContent className="p-0">
          <div className="border-b border-border p-3">
            <Input
              placeholder={t("admin.audit.filterPlaceholder")}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="h-8 text-[13px]"
            />
          </div>
          {entries &&
            (entries.length === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">
                {t("admin.audit.noEntries")}
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="border-border">
                    <TableHead className="text-xs">
                      {t("admin.audit.colEvent")}
                    </TableHead>
                    <TableHead className="text-xs">
                      {t("admin.audit.colActor")}
                    </TableHead>
                    <TableHead className="text-xs">
                      {t("admin.audit.colTarget")}
                    </TableHead>
                    <TableHead className="text-xs">
                      {t("admin.audit.colTime")}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {entries.map((e) => (
                    <TableRow key={e.id} className="border-border">
                      <TableCell>
                        <code className="text-[11px] text-muted-foreground">
                          {e.event_type}
                        </code>
                      </TableCell>
                      <TableCell className="text-[13px]">
                        {e.actor_email || "system"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {e.target_type
                          ? `${e.target_type}/${e.target_id.slice(0, 8)}`
                          : "—"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {new Date(e.created_at).toLocaleString()}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ))}
        </CardContent>
      </Card>
    </>
  );
}
