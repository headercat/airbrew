import { Outlet, Routes, Route } from "react-router-dom";

import { AppLayout } from "@/components/layout/app-layout";
import {
  RedirectIfSignedIn,
  RequireAdmin,
  RequireAuth,
} from "@/components/auth-route";
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
import AdminAI from "@/pages/admin/ai";
import ChatPage from "@/pages/chat";
import ContactsPage from "@/pages/contacts";
import DashboardPage from "@/pages/dashboard";
import DrivePage from "@/pages/drive";
import DriveSharePage from "@/pages/drive/share";
import LoginPage from "@/pages/login";
import MailPage from "@/pages/mail";
import ModuleStubPage from "@/pages/module-stub";
import NotFoundPage from "@/pages/not-found";
import PasswordsPage from "@/pages/passwords";
import PasswordEditor from "@/pages/passwords/editor";
import PasswordView from "@/pages/passwords/view";
import ProfilePage from "@/pages/profile";
import RegisterPage from "@/pages/register";
import AIPage from "@/pages/ai";
import { VaultProvider } from "@/lib/vault/store";

export default function App() {
  return (
    <Routes>
      <Route
        path="/login"
        element={
          <RedirectIfSignedIn>
            <LoginPage />
          </RedirectIfSignedIn>
        }
      />
      <Route
        path="/register"
        element={
          <RedirectIfSignedIn>
            <RegisterPage />
          </RedirectIfSignedIn>
        }
      />

      <Route path="/s/:token" element={<DriveSharePage />} />

      <Route
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<DashboardPage />} />
        <Route path="/profile" element={<ProfilePage />} />

        {/* Admin — nested layout with grouped sidebar */}
        <Route
          path="/admin"
          element={
            <RequireAdmin>
              <AdminLayout />
            </RequireAdmin>
          }
        >
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
          <Route path="ai" element={<AdminAI />} />
        </Route>

        {/* AI module — conversation list + chat surface */}
        <Route path="/ai" element={<AIPage />} />
        <Route path="/ai/:id" element={<AIPage />} />

        {/* Mail — mailbox folders, reading, composing, and attachments. */}
        <Route path="/mail" element={<MailPage />} />

        {/* Password vault — shared VaultProvider so unlock state persists
            across the list and editor routes. */}
        <Route path="/passwords" element={<VaultLayout />}>
          <Route index element={<PasswordsPage />} />
          <Route path="new" element={<PasswordEditor />} />
          <Route path=":id" element={<PasswordView />} />
          <Route path=":id/edit" element={<PasswordEditor />} />
        </Route>

        {/* Drive — file storage with folders, shares, trash. */}
        <Route path="/drive" element={<DrivePage />} />

        {/* Contacts — address book with groups and vCard import/export. */}
        <Route path="/contacts" element={<ContactsPage />} />

        {/* Chat — real-time direct and group messaging. */}
        <Route path="/chat" element={<ChatPage />} />
        <Route path="/chat/:roomId" element={<ChatPage />} />

        <Route path="/:module" element={<ModuleStubPage />} />
      </Route>

      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
}

// VaultLayout wraps the password routes in the vault store so navigation
// between the list and the editor keeps the unlocked vault key in memory.
function VaultLayout() {
  return (
    <VaultProvider>
      <Outlet />
    </VaultProvider>
  );
}
