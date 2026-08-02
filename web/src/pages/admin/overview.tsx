import { useEffect, useState } from "react";
import { Boxes } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SectionHeader } from "./shared";
import { api, type AdminDashboard } from "@/lib/api";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";

export default function AdminOverview() {
  const { t } = useTranslation();
  const [data, setData] = useState<AdminDashboard | null>(null);
  useEffect(() => {
    api
      .get<AdminDashboard>("/api/admin/dashboard")
      .then(setData)
      .catch(() => {});
  }, []);

  if (!data)
    return (
      <>
        <SectionHeader
          icon={Boxes}
          titleKey="admin.overview.title"
          descKey="admin.overview.description"
        />
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </>
    );

  return (
    <>
      <SectionHeader
        icon={Boxes}
        titleKey="admin.overview.title"
        descKey="admin.overview.description"
      />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {[
          { label: t("admin.dashboard.users"), value: data.users },
          { label: t("admin.dashboard.admins"), value: data.admins },
          {
            label: t("admin.dashboard.activeSessions"),
            value: data.active_sessions,
          },
          {
            label: t("admin.dashboard.modules"),
            value: `${data.modules_enabled}/${data.modules_total}`,
          },
        ].map((s) => (
          <Card key={s.label}>
            <CardContent className="p-4">
              <p className="text-xs text-muted-foreground">{s.label}</p>
              <p className="mt-1 text-2xl font-semibold tabular-nums">
                {s.value}
              </p>
            </CardContent>
          </Card>
        ))}
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">
            {t("admin.dashboard.recentActivity")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {data.recent_events?.length ? (
            <div className="space-y-1">
              {data.recent_events.map((e) => (
                <div
                  key={e.id}
                  className="flex items-center gap-3 rounded-md px-2 py-1.5 text-[13px]"
                >
                  <code className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                    {e.event_type}
                  </code>
                  <span className="flex-1 truncate">
                    {e.actor_email || "system"}
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {new Date(e.created_at).toLocaleTimeString()}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t("admin.dashboard.noActivity")}
            </p>
          )}
        </CardContent>
      </Card>
    </>
  );
}
