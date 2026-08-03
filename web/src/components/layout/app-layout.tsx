import { Outlet } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Sidebar } from "@/components/layout/sidebar";

export function AppLayout() {
  const { t } = useTranslation();
  return (
    <div className="flex h-screen overflow-hidden">
      {/* Skip link so keyboard users can jump past the sidebar rail. */}
      <a
        href="#main-content"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-sm text-primary-foreground focus:not-sr-only focus:absolute focus:left-4 focus:top-4"
      >
        {t("common.skipToContent")}
      </a>
      <Sidebar />
      <main id="main-content" tabIndex={-1} className="flex-1 overflow-y-auto outline-none">
        <Outlet />
      </main>
    </div>
  );
}
