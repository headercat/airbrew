import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Construction } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { PageHeader, PageWrapper } from "@/components/page";
import { api, isApiError, type ModuleStatus } from "@/lib/api";
import { MODULES, type ModuleKey } from "@/lib/modules";

export default function ModuleStubPage() {
  const params = useParams<{ module: string }>();
  const key = params.module as ModuleKey | undefined;
  const meta = MODULES.find((m) => m.key === key);
  const { t } = useTranslation();

  if (!meta) {
    return (
      <PageWrapper>
        <PageHeader
          title={t("moduleStub.unknownTitle")}
          description={t("moduleStub.unknownDescription", { module: params.module })}
        />
      </PageWrapper>
    );
  }

  const Icon = meta.icon;
  const [status, setStatus] = useState<ModuleStatus | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const s = await api.get<ModuleStatus>(`/api/${meta.key}/status`);
        if (alive) setStatus(s);
      } catch (err) {
        if (alive) {
          setError(
            isApiError(err) ? err.error_description ?? err.error : "fetch failed"
          );
        }
      }
    })();
    return () => { alive = false; };
  }, [meta.key]);

  return (
    <PageWrapper>
      <div className="space-y-1">
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <Icon className="h-5 w-5 text-primary" />
          {t(meta.labelKey)}
        </h1>
        <p className="text-sm text-muted-foreground">
          {t(meta.descriptionKey)}
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Construction className="h-4 w-4" />
            {t("moduleStub.underConstruction")}
          </CardTitle>
          <CardDescription>{t("moduleStub.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-center justify-between rounded-md border bg-muted/30 px-3 py-2 text-sm">
            <span className="font-mono text-xs">GET /api/{meta.key}/status</span>
            <code className="text-xs text-muted-foreground">
              {status ? JSON.stringify(status) : error ?? "loading…"}
            </code>
          </div>
        </CardContent>
      </Card>
    </PageWrapper>
  );
}
