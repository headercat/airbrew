import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router-dom";
import { Menu as MenuIcon, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Sidebar } from "@/components/layout/sidebar";
import { cn } from "@/lib/utils";

export function AppLayout() {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();

  // Auto-close the drawer whenever the route changes.
  useEffect(() => {
    setMobileOpen(false);
  }, [location.pathname]);

  return (
    <div className="flex h-screen overflow-hidden">
      {/* Skip link so keyboard users can jump past the sidebar rail. */}
      <a
        href="#main-content"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-sm text-primary-foreground focus:not-sr-only focus:absolute focus:left-4 focus:top-4"
      >
        {t("common.skipToContent")}
      </a>

      {/* Persistent sidebar on lg+; slide-in drawer below lg. */}
      <div className="hidden shrink-0 lg:flex">
        <Sidebar />
      </div>

      {/* Mobile drawer + backdrop. */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div
            className="absolute inset-0 bg-black/40"
            onClick={() => setMobileOpen(false)}
            aria-hidden
          />
          <div className="absolute inset-y-0 left-0 flex shadow-xl">
            <Sidebar />
            <button
              onClick={() => setMobileOpen(false)}
              aria-label={t("common.close")}
              className={cn(
                "self-start rounded-md p-2 text-muted-foreground hover:bg-accent hover:text-foreground",
              )}
            >
              <X className="h-5 w-5" />
            </button>
          </div>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile top bar with hamburger. */}
        <header className="flex h-12 items-center gap-2 border-b bg-background px-3 lg:hidden">
          <button
            onClick={() => setMobileOpen(true)}
            aria-label={t("nav.openMenu")}
            className="rounded-md p-2 hover:bg-accent"
          >
            <MenuIcon className="h-5 w-5" />
          </button>
          <span className="text-sm font-semibold">{t("app.name")}</span>
        </header>
        <main
          id="main-content"
          tabIndex={-1}
          className="min-h-0 flex-1 overflow-y-auto outline-none"
        >
          <Outlet />
        </main>
      </div>
    </div>
  );
}
