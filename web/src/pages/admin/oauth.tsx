import { Lock } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { SectionHeader } from "./shared";

export default function AdminOAuth() {
  const { t } = useTranslation();
  return (
    <>
      <SectionHeader icon={Lock} titleKey="admin.oauth.title" descKey="admin.oauth.description" />
      <Card>
        <CardContent className="py-12 text-center">
          <Lock className="mx-auto mb-3 h-8 w-8 text-muted-foreground/40" />
          <p className="text-sm font-medium">{t("admin.oauth.noClients")}</p>
          <p className="mt-1 text-xs text-muted-foreground">{t("admin.oauth.description")}</p>
          <Button variant="outline" size="sm" className="mt-4" disabled>
            {t("admin.oauth.register")}
          </Button>
        </CardContent>
      </Card>
    </>
  );
}
