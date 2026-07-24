import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

export default function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-6 text-center">
      <p className="text-6xl font-bold tracking-tight">{t("notFound.title")}</p>
      <p className="text-muted-foreground">{t("notFound.description")}</p>
      <Button asChild>
        <Link to="/">{t("notFound.goHome")}</Link>
      </Button>
    </div>
  );
}
