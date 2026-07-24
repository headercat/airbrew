import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Copy, KeyRound, Lock, Plus, Save, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api, isApiError, type OAuthClient } from "@/lib/api";
import { SectionHeader } from "./shared";

type OAuthForm = {
  id?: string;
  name: string;
  client_type: "public" | "confidential";
  token_endpoint_auth_method: "none" | "client_secret_basic" | "client_secret_post";
  allowed_scopes: string;
  redirect_uris: string;
  post_logout_redirect_uris: string;
  is_first_party: boolean;
  require_consent: boolean;
  is_active: boolean;
};

type CreatedSecret = {
  client_id: string;
  client_secret: string;
};

const blankForm: OAuthForm = {
  name: "",
  client_type: "public",
  token_endpoint_auth_method: "none",
  allowed_scopes: "openid\nprofile\nemail",
  redirect_uris: "",
  post_logout_redirect_uris: "",
  is_first_party: false,
  require_consent: true,
  is_active: true,
};

function lines(value: string) {
  return value.split(/\r?\n/).map((v) => v.trim()).filter(Boolean);
}

function formFromClient(c: OAuthClient): OAuthForm {
  return {
    id: c.id,
    name: c.name,
    client_type: c.client_type,
    token_endpoint_auth_method: c.token_endpoint_auth_method,
    allowed_scopes: c.allowed_scopes.join("\n"),
    redirect_uris: c.redirect_uris.join("\n"),
    post_logout_redirect_uris: c.post_logout_redirect_uris.join("\n"),
    is_first_party: c.is_first_party,
    require_consent: c.require_consent,
    is_active: c.is_active,
  };
}

function HelpText({ children }: { children: React.ReactNode }) {
  return <p className="text-xs leading-5 text-muted-foreground">{children}</p>;
}

export default function AdminOAuth() {
  const { clientId } = useParams();
  if (clientId || location.pathname.endsWith("/oauth/new")) {
    return <OAuthClientSettings clientId={clientId} />;
  }
  return <OAuthClientList />;
}

function OAuthClientList() {
  const { t } = useTranslation();
  const [clients, setClients] = useState<OAuthClient[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const res = await api.get<{ clients: OAuthClient[] }>("/api/admin/oauth/clients");
      setClients(res.clients);
    } catch (err) {
      setError(isApiError(err) ? err.error_description ?? err.error : "error");
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  const sortedClients = useMemo(() => clients ?? [], [clients]);

  return (
    <>
      <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <SectionHeader icon={Lock} titleKey="admin.oauth.title" descKey="admin.oauth.description" />
        <Button asChild size="sm">
          <Link to="/admin/oauth/new">
            <Plus className="mr-2 h-4 w-4" />
            {t("admin.oauth.register")}
          </Link>
        </Button>
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Card>
        <CardContent className="p-0">
          {clients === null ? (
            <div className="p-6 text-sm text-muted-foreground">{t("admin.oauth.loading")}</div>
          ) : sortedClients.length === 0 ? (
            <div className="py-12 text-center">
              <Lock className="mx-auto mb-3 h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm font-medium">{t("admin.oauth.noClients")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("admin.oauth.emptyHint")}</p>
              <Button asChild variant="outline" size="sm" className="mt-4">
                <Link to="/admin/oauth/new">{t("admin.oauth.register")}</Link>
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="border-border">
                  <TableHead className="text-xs">{t("admin.oauth.colClient")}</TableHead>
                  <TableHead className="text-xs">{t("admin.oauth.colType")}</TableHead>
                  <TableHead className="text-xs">{t("admin.oauth.colRedirects")}</TableHead>
                  <TableHead className="text-xs">{t("admin.oauth.colScopes")}</TableHead>
                  <TableHead className="text-xs">{t("admin.oauth.colStatus")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedClients.map((c) => (
                  <TableRow key={c.id} className="border-border">
                    <TableCell>
                      <Link to={`/admin/oauth/${c.id}`} className="block">
                        <div className="text-[13px] font-medium">{c.name}</div>
                        <div className="mt-0.5 max-w-[360px] truncate font-mono text-xs text-muted-foreground">{c.client_id}</div>
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary" className="text-[10px]">{t(`admin.oauth.${c.client_type}`)}</Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{c.redirect_uris.length}</TableCell>
                    <TableCell className="max-w-[260px] truncate text-xs text-muted-foreground">
                      {c.allowed_scopes.join(" ") || "-"}
                    </TableCell>
                    <TableCell>
                      <Badge variant={c.is_active ? "default" : "outline"} className="text-[10px]">
                        {c.is_active ? t("admin.oauth.active") : t("admin.oauth.inactive")}
                      </Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}

function OAuthClientSettings({ clientId }: { clientId?: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const isEditing = Boolean(clientId);
  const [form, setForm] = useState<OAuthForm>(blankForm);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(Boolean(clientId));
  const [saving, setSaving] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<CreatedSecret | null>(null);

  useEffect(() => {
    if (!clientId) return;
    let cancelled = false;
    setLoading(true);
    api.get<OAuthClient>(`/api/admin/oauth/clients/${clientId}`)
      .then((client) => {
        if (!cancelled) setForm(formFromClient(client));
      })
      .catch((err) => {
        if (!cancelled) setError(isApiError(err) ? err.error_description ?? err.error : "error");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [clientId]);

  function setClientType(value: "public" | "confidential") {
    setForm((prev) => ({
      ...prev,
      client_type: value,
      token_endpoint_auth_method: value === "public" ? "none" : "client_secret_basic",
    }));
  }

  async function save() {
    setSaving(true);
    setError(null);
    const payload = {
      name: form.name,
      token_endpoint_auth_method: form.token_endpoint_auth_method,
      allowed_scopes: lines(form.allowed_scopes),
      redirect_uris: lines(form.redirect_uris),
      post_logout_redirect_uris: lines(form.post_logout_redirect_uris),
      is_first_party: form.is_first_party,
      require_consent: form.require_consent,
      is_active: form.is_active,
    };
    try {
      if (clientId) {
        await api.patch(`/api/admin/oauth/clients/${clientId}`, payload);
        navigate("/admin/oauth");
      } else {
        const res = await api.post<{ client: OAuthClient; client_secret?: string }>("/api/admin/oauth/clients", {
          ...payload,
          client_type: form.client_type,
        });
        if (res.client_secret) {
          setCreatedSecret({ client_id: res.client.client_id, client_secret: res.client_secret });
        } else {
          navigate("/admin/oauth");
        }
      }
    } catch (err) {
      setError(isApiError(err) ? err.error_description ?? err.error : "error");
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!clientId || !confirm(t("admin.oauth.confirmDelete", { name: form.name }))) return;
    try {
      await api.del(`/api/admin/oauth/clients/${clientId}`);
      navigate("/admin/oauth");
    } catch (err) {
      setError(isApiError(err) ? err.error_description ?? err.error : "error");
    }
  }

  async function copy(value: string) {
    await navigator.clipboard?.writeText(value);
  }

  return (
    <>
      <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <SectionHeader icon={KeyRound} titleKey={isEditing ? "admin.oauth.settingsTitle" : "admin.oauth.newTitle"} descKey="admin.oauth.settingsDescription" />
        <Button asChild variant="outline" size="sm">
          <Link to="/admin/oauth">
            <ArrowLeft className="mr-2 h-4 w-4" />
            {t("admin.oauth.backToList")}
          </Link>
        </Button>
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      {createdSecret && (
        <Card className="border-primary/30 bg-primary/5">
          <CardContent className="flex flex-col gap-3 p-4 md:flex-row md:items-center md:justify-between">
            <div className="min-w-0">
              <p className="text-sm font-medium">{t("admin.oauth.secretCreated")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("admin.oauth.secretCreatedDesc")}</p>
              <p className="mt-2 break-all font-mono text-xs text-muted-foreground">
                {createdSecret.client_id} / {createdSecret.client_secret}
              </p>
            </div>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => copy(createdSecret.client_secret)}>
                <Copy className="mr-2 h-3.5 w-3.5" />
                {t("admin.oauth.copySecret")}
              </Button>
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => navigate("/admin/oauth")}>
                <X className="h-4 w-4" />
              </Button>
            </div>
          </CardContent>
        </Card>
      )}
      <Card>
        <CardContent className="space-y-6 p-5 lg:p-6">
          {loading ? (
            <p className="text-sm text-muted-foreground">{t("admin.oauth.loading")}</p>
          ) : (
            <>
              <section className="grid gap-5 lg:grid-cols-[220px_minmax(0,1fr)]">
                <div>
                  <h2 className="text-sm font-medium">{t("admin.oauth.identitySection")}</h2>
                  <HelpText>{t("admin.oauth.identitySectionDesc")}</HelpText>
                </div>
                <div className="space-y-4">
                  <div className="space-y-2">
                    <Label htmlFor="oauth-name">{t("admin.oauth.name")}</Label>
                    <Input id="oauth-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
                    <HelpText>{t("admin.oauth.nameDesc")}</HelpText>
                  </div>
                  <div className="space-y-2">
                    <Label>{t("admin.oauth.clientType")}</Label>
                    <div className="grid grid-cols-2 gap-2">
                      <Button type="button" variant={form.client_type === "public" ? "default" : "outline"} disabled={isEditing}
                        onClick={() => setClientType("public")}>
                        {t("admin.oauth.public")}
                      </Button>
                      <Button type="button" variant={form.client_type === "confidential" ? "default" : "outline"} disabled={isEditing}
                        onClick={() => setClientType("confidential")}>
                        {t("admin.oauth.confidential")}
                      </Button>
                    </div>
                    <HelpText>{t(isEditing ? "admin.oauth.clientTypeLockedDesc" : "admin.oauth.clientTypeDesc")}</HelpText>
                  </div>
                  {form.client_type === "confidential" && (
                    <div className="space-y-2">
                      <Label htmlFor="oauth-auth">{t("admin.oauth.authMethod")}</Label>
                      <select id="oauth-auth" className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                        value={form.token_endpoint_auth_method}
                        onChange={(e) => setForm({ ...form, token_endpoint_auth_method: e.target.value as OAuthForm["token_endpoint_auth_method"] })}>
                        <option value="client_secret_basic">client_secret_basic</option>
                        <option value="client_secret_post">client_secret_post</option>
                      </select>
                      <HelpText>{t("admin.oauth.authMethodDesc")}</HelpText>
                    </div>
                  )}
                </div>
              </section>

              <section className="grid gap-5 border-t border-border pt-6 lg:grid-cols-[220px_minmax(0,1fr)]">
                <div>
                  <h2 className="text-sm font-medium">{t("admin.oauth.flowSection")}</h2>
                  <HelpText>{t("admin.oauth.flowSectionDesc")}</HelpText>
                </div>
                <div className="space-y-4">
                  <div className="space-y-2">
                    <Label htmlFor="oauth-redirects">{t("admin.oauth.redirectUris")}</Label>
                    <Textarea id="oauth-redirects" className="min-h-[104px] font-mono text-xs" value={form.redirect_uris}
                      onChange={(e) => setForm({ ...form, redirect_uris: e.target.value })} />
                    <HelpText>{t("admin.oauth.redirectUrisDesc")}</HelpText>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="oauth-logout-redirects">{t("admin.oauth.logoutRedirectUris")}</Label>
                    <Textarea id="oauth-logout-redirects" className="min-h-[86px] font-mono text-xs" value={form.post_logout_redirect_uris}
                      onChange={(e) => setForm({ ...form, post_logout_redirect_uris: e.target.value })} />
                    <HelpText>{t("admin.oauth.logoutRedirectUrisDesc")}</HelpText>
                  </div>
                </div>
              </section>

              <section className="grid gap-5 border-t border-border pt-6 lg:grid-cols-[220px_minmax(0,1fr)]">
                <div>
                  <h2 className="text-sm font-medium">{t("admin.oauth.accessSection")}</h2>
                  <HelpText>{t("admin.oauth.accessSectionDesc")}</HelpText>
                </div>
                <div className="space-y-4">
                  <div className="space-y-2">
                    <Label htmlFor="oauth-scopes">{t("admin.oauth.scopes")}</Label>
                    <Textarea id="oauth-scopes" className="min-h-[104px] font-mono text-xs" value={form.allowed_scopes}
                      onChange={(e) => setForm({ ...form, allowed_scopes: e.target.value })} />
                    <HelpText>{t("admin.oauth.scopesDesc")}</HelpText>
                  </div>
                  <div className="space-y-3 rounded-md border border-border p-3">
                    <label className="flex items-start justify-between gap-4 text-sm">
                      <span>
                        <span className="block font-medium">{t("admin.oauth.firstParty")}</span>
                        <span className="mt-1 block text-xs leading-5 text-muted-foreground">{t("admin.oauth.firstPartyDesc")}</span>
                      </span>
                      <Switch checked={form.is_first_party} onCheckedChange={(v) => setForm({ ...form, is_first_party: v })} />
                    </label>
                    <label className="flex items-start justify-between gap-4 border-t border-border pt-3 text-sm">
                      <span>
                        <span className="block font-medium">{t("admin.oauth.requireConsent")}</span>
                        <span className="mt-1 block text-xs leading-5 text-muted-foreground">{t("admin.oauth.requireConsentDesc")}</span>
                      </span>
                      <Switch checked={form.require_consent} onCheckedChange={(v) => setForm({ ...form, require_consent: v })} />
                    </label>
                    <label className="flex items-start justify-between gap-4 border-t border-border pt-3 text-sm">
                      <span>
                        <span className="block font-medium">{t("admin.oauth.active")}</span>
                        <span className="mt-1 block text-xs leading-5 text-muted-foreground">{t("admin.oauth.activeDesc")}</span>
                      </span>
                      <Switch checked={form.is_active} onCheckedChange={(v) => setForm({ ...form, is_active: v })} />
                    </label>
                  </div>
                </div>
              </section>

              <div className="flex flex-col-reverse gap-2 border-t border-border pt-5 sm:flex-row sm:justify-between">
                {isEditing ? (
                  <Button variant="outline" onClick={remove}>
                    <Trash2 className="mr-2 h-4 w-4" />
                    {t("admin.oauth.delete")}
                  </Button>
                ) : <span />}
                <Button onClick={save} disabled={saving}>
                  <Save className="mr-2 h-4 w-4" />
                  {saving ? t("admin.oauth.saving") : t("admin.oauth.save")}
                </Button>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </>
  );
}
