import { Routes, Route } from "react-router-dom";

import { AppLayout } from "@/components/layout/app-layout";
import { RedirectIfSignedIn, RequireAuth } from "@/components/auth-route";
import AdminLayout from "@/pages/admin";
import AdminOverview from "@/pages/admin/overview";
import AdminUsers from "@/pages/admin/users";
import AdminModules from "@/pages/admin/modules";
import AdminModuleSettings from "@/pages/admin/module-settings";
import AdminAudit from "@/pages/admin/audit";
import AdminSecurity from "@/pages/admin/security";
import AdminBranding from "@/pages/admin/branding";
import AdminOAuth from "@/pages/admin/oauth";
import AdminSystem from "@/pages/admin/system";
import DashboardPage from "@/pages/dashboard";
import LoginPage from "@/pages/login";
import ModuleStubPage from "@/pages/module-stub";
import NotFoundPage from "@/pages/not-found";
import ProfilePage from "@/pages/profile";
import RegisterPage from "@/pages/register";

export default function App() {
  return (
    <Routes>
      <Route
        path="/login"
        element={<RedirectIfSignedIn><LoginPage /></RedirectIfSignedIn>}
      />
      <Route
        path="/register"
        element={<RedirectIfSignedIn><RegisterPage /></RedirectIfSignedIn>}
      />

      <Route element={<RequireAuth><AppLayout /></RequireAuth>}>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/profile" element={<ProfilePage />} />

        {/* Admin — nested layout with grouped sidebar */}
        <Route path="/admin" element={<AdminLayout />}>
          <Route index element={<AdminOverview />} />
          <Route path="users" element={<AdminUsers />} />
          <Route path="modules" element={<AdminModules />} />
          <Route path="modules/:moduleKey" element={<AdminModuleSettings />} />
          <Route path="audit" element={<AdminAudit />} />
          <Route path="security" element={<AdminSecurity />} />
          <Route path="branding" element={<AdminBranding />} />
          <Route path="oauth" element={<AdminOAuth />} />
          <Route path="oauth/new" element={<AdminOAuth />} />
          <Route path="oauth/:clientId" element={<AdminOAuth />} />
          <Route path="system" element={<AdminSystem />} />
        </Route>

        <Route path="/:module" element={<ModuleStubPage />} />
      </Route>

      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
}
