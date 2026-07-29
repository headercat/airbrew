import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowUpRight, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { PageWrapper } from "@/components/page";
import { useAuth } from "@/lib/auth";
import { api, type ModuleStatus } from "@/lib/api";
import { MODULES } from "@/lib/modules";

export default function DashboardPage() {
  const { user } = useAuth();
  const { t } = useTranslation();

  return (
    <PageWrapper>
      <div className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">
          {t("dashboard.title")}
        </h1>
        <p className="text-sm text-muted-foreground">
          {t("dashboard.signedInAs")}{" "}
          <span className="font-medium text-foreground">{user?.email}</span>
        </p>
      </div>

      <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
        {MODULES.map((m) => (
          <ModuleCard key={m.key} moduleKey={m.key} path={m.path} icon={m.icon} />
        ))}
      </div>
    </PageWrapper>
  );
}

function ModuleCard({
  moduleKey,
  path,
  icon: Icon,
}: {
  moduleKey: string;
  path: string;
  icon: React.ComponentType<{ className?: string }>;
}) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<"loading" | string>("loading");

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const s = await api.get<ModuleStatus>(`/api/${moduleKey}/status`);
        if (alive) setStatus(s.status);
      } catch {
        if (alive) setStatus("error");
      }
    })();
    return () => { alive = false; };
  }, [moduleKey]);

  return (
    <Link to={path} className="block w-full">
      <Card className="group h-full w-full cursor-pointer transition-all hover:border-foreground/20 hover:shadow-md">
        <CardContent className="flex items-start gap-3 p-4">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <Icon className="h-4.5 w-4.5 text-muted-foreground" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex items-center justify-between gap-2">
              <p className="text-[13px] font-medium">
                {t(`dashboard.modules.${moduleKey}.label`)}
              </p>
              {status === "loading" ? (
                <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />
              ) : status === "disabled" ? (
                <Badge variant="outline" className="text-[10px]">
                  {t("dashboard.disabled")}
                </Badge>
              ) : status === "not_implemented" ? (
                <Badge variant="outline" className="text-[10px]">
                  {t("dashboard.planned")}
                </Badge>
              ) : status === "ok" ? (
                <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/20 dark:text-emerald-400 text-[10px]">
                  {t("dashboard.online")}
                </Badge>
              ) : null}
            </div>
            <p className="line-clamp-2 text-xs text-muted-foreground">
              {t(`dashboard.modules.${moduleKey}.description`)}
            </p>
          </div>
          <ArrowUpRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground/50 opacity-0 transition-opacity group-hover:opacity-100" />
        </CardContent>
      </Card>
    </Link>
  );
}
