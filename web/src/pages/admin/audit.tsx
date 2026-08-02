import { useCallback, useEffect, useState } from "react";
import { Activity, ChevronLeft, ChevronRight } from "lucide-react";
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
import { api, type AuditEntry } from "@/lib/api";

export default function AdminAudit() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [eventType, setEventType] = useState("");
  const [actor, setActor] = useState("");
  const [targetType, setTargetType] = useState("");
  const [targetID, setTargetID] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const refresh = useCallback(async () => {
    const params = new URLSearchParams({
      limit: String(auditPageSize),
      offset: String(offset),
    });
    if (eventType.trim()) params.set("event_type", eventType.trim());
    if (actor.trim()) params.set("actor", actor.trim());
    if (targetType.trim()) params.set("target_type", targetType.trim());
    if (targetID.trim()) params.set("target_id", targetID.trim());
    if (from) params.set("from", new Date(from).toISOString());
    if (to) params.set("to", new Date(to).toISOString());
    try {
      const res = await api.get<{
        entries: AuditEntry[];
        total: number;
        limit: number;
        offset: number;
      }>(
        `/api/admin/audit?${params.toString()}`,
      );
      setEntries(res.entries);
      setTotal(res.total);
    } catch {
      setEntries([]);
    }
  }, [actor, eventType, from, offset, targetID, targetType, to]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const pageStart = total === 0 ? 0 : offset + 1;
  const pageEnd = Math.min(offset + (entries?.length ?? 0), total);
  const canPageBack = offset > 0;
  const canPageForward = offset + auditPageSize < total;

  function resetFilter(update: () => void) {
    update();
    setOffset(0);
  }

  return (
    <>
      <SectionHeader
        icon={Activity}
        titleKey="admin.audit.title"
        descKey="admin.audit.description"
      />
      <Card>
        <CardContent className="p-0">
          <div className="grid gap-2 border-b border-border p-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_150px_minmax(0,1fr)_180px_180px_auto]">
            <Input
              placeholder={t("admin.audit.filterPlaceholder")}
              value={eventType}
              onChange={(e) =>
                resetFilter(() => setEventType(e.target.value))
              }
              className="h-8 text-[13px]"
            />
            <Input
              placeholder={t("admin.audit.actorPlaceholder")}
              value={actor}
              onChange={(e) => resetFilter(() => setActor(e.target.value))}
              className="h-8 text-[13px]"
            />
            <Input
              placeholder={t("admin.audit.targetTypePlaceholder")}
              value={targetType}
              onChange={(e) =>
                resetFilter(() => setTargetType(e.target.value))
              }
              className="h-8 text-[13px]"
            />
            <Input
              placeholder={t("admin.audit.targetIDPlaceholder")}
              value={targetID}
              onChange={(e) => resetFilter(() => setTargetID(e.target.value))}
              className="h-8 text-[13px]"
            />
            <Input
              type="datetime-local"
              value={from}
              onChange={(e) => resetFilter(() => setFrom(e.target.value))}
              aria-label={t("admin.audit.from")}
              className="h-8 text-[13px]"
            />
            <Input
              type="datetime-local"
              value={to}
              onChange={(e) => resetFilter(() => setTo(e.target.value))}
              aria-label={t("admin.audit.to")}
              className="h-8 text-[13px]"
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setOffset(0);
                void refresh();
              }}
            >
              {t("common.search")}
            </Button>
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
                      {t("admin.audit.colIP")}
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
                        <pre className="mt-2 max-w-[360px] whitespace-pre-wrap break-words rounded-md bg-muted px-2 py-1 text-[11px] text-muted-foreground">
                          {formatMetadata(e.metadata)}
                        </pre>
                      </TableCell>
                      <TableCell className="text-[13px]">
                        <div>{e.actor_email || e.actor_user_id || "system"}</div>
                        {e.actor_client_id && (
                          <div className="font-mono text-xs text-muted-foreground">
                            {e.actor_client_id}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {e.target_type
                          ? `${e.target_type}/${e.target_id.slice(0, 8)}`
                          : "—"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        <div>{e.ip_address || "—"}</div>
                        <div className="max-w-[260px] truncate">
                          {e.user_agent || "—"}
                        </div>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {new Date(e.created_at).toLocaleString()}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ))}
          {entries && (
            <div className="flex flex-col gap-2 border-t border-border px-3 py-3 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
              <span>
                {t("admin.audit.pageSummary", {
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
                  onClick={() => setOffset(Math.max(0, offset - auditPageSize))}
                >
                  <ChevronLeft className="h-4 w-4" />
                  {t("admin.audit.previousPage")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!canPageForward}
                  onClick={() => setOffset(offset + auditPageSize)}
                >
                  {t("admin.audit.nextPage")}
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

const auditPageSize = 50;

function formatMetadata(raw: string) {
  try {
    return JSON.stringify(JSON.parse(raw || "{}"), null, 2);
  } catch {
    return raw || "{}";
  }
}
