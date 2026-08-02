import { useState } from "react";
import { Loader2, Upload, UserCircle } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import { useAuth } from "@/lib/auth";
import { api, isApiError, type ProfileUpdate } from "@/lib/api";

export default function ProfilePage() {
  const { user, refresh } = useAuth();
  const { t } = useTranslation();
  if (!user) return null;

  return (
    <PageWrapper>
      <PageHeader
        title={t("profile.title")}
        description={t("profile.subtitle")}
      />

      <AvatarSection onSaved={refresh} />
      <ProfileSection onSaved={refresh} />
      <PasswordSection />
    </PageWrapper>
  );
}

function AvatarSection({ onSaved }: { onSaved: () => Promise<unknown> }) {
  const { user } = useAuth();
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const initial = (user?.email ?? "?").charAt(0).toUpperCase();

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      await api.upload<{ avatar_url: string }>("/api/auth/avatar", file);
      await onSaved();
    } catch (err) {
      setError(
        isApiError(err)
          ? (err.error_description ?? err.error)
          : t("profile.avatar.uploadFailed"),
      );
    } finally {
      setBusy(false);
      e.target.value = "";
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("profile.avatar.title")}</CardTitle>
        <CardDescription>{t("profile.avatar.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex items-center gap-5">
        <Avatar className="h-20 w-20">
          {user?.avatar_url ? (
            <AvatarImage src={user.avatar_url} alt={user.email} />
          ) : null}
          <AvatarFallback className="text-2xl">
            {user?.avatar_url ? <UserCircle className="h-10 w-10" /> : initial}
          </AvatarFallback>
        </Avatar>
        <div className="space-y-2">
          <label htmlFor="avatar-upload">
            <Button variant="outline" disabled={busy} asChild>
              <span>
                {busy ? <Loader2 className="animate-spin" /> : <Upload />}
                {t("profile.avatar.upload")}
              </span>
            </Button>
          </label>
          <input
            id="avatar-upload"
            type="file"
            accept="image/png,image/jpeg,image/webp,image/gif"
            className="hidden"
            onChange={onPick}
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
      </CardContent>
    </Card>
  );
}

function ProfileSection({ onSaved }: { onSaved: () => Promise<unknown> }) {
  const { user } = useAuth();
  const { t } = useTranslation();
  const [displayName, setDisplayName] = useState(user?.display_name ?? "");
  const [description, setDescription] = useState(user?.description ?? "");
  const [birthday, setBirthday] = useState(user?.birthday ?? "");
  const [phoneNumber, setPhoneNumber] = useState(user?.phone_number ?? "");
  const [fields, setFields] = useState<[string, string][]>(
    Object.entries(user?.custom_fields ?? {}).map(([k, v]) => [k, v]),
  );
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  function setField(i: number, key: string, value: string) {
    setFields((prev) => prev.map((f, idx) => (idx === i ? [key, value] : f)));
  }
  function addField() {
    setFields((prev) => [...prev, ["", ""]]);
  }
  function removeField(i: number) {
    setFields((prev) => prev.filter((_, idx) => idx !== i));
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMessage(null);
    setError(null);
    const payload: ProfileUpdate = {
      display_name: displayName,
      description,
      birthday,
      phone_number: phoneNumber,
      custom_fields: Object.fromEntries(
        fields.filter(([k]) => k.trim() !== ""),
      ),
    };
    try {
      await api.patch("/api/auth/me", payload);
      await onSaved();
      setMessage(t("profile.form.saved"));
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : "Save failed",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("profile.form.title")}</CardTitle>
        <CardDescription>{t("profile.form.description")}</CardDescription>
      </CardHeader>
      <form onSubmit={save}>
        <CardContent className="space-y-4">
          <div className="grid gap-2">
            <Label htmlFor="email">{t("profile.form.email")}</Label>
            <Input id="email" value={user?.email ?? ""} disabled />
            <p className="text-xs text-muted-foreground">
              {t("profile.form.emailNote")}
            </p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="display_name">
              {t("profile.form.displayName")}
            </Label>
            <Input
              id="display_name"
              value={displayName}
              maxLength={100}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="description">
              {t("profile.form.descriptionLabel")}
            </Label>
            <Textarea
              id="description"
              value={description}
              rows={3}
              maxLength={500}
              placeholder={t("profile.form.descriptionPlaceholder")}
              onChange={(e) => setDescription(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              {description.length}/500
            </p>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="birthday">{t("profile.form.birthday")}</Label>
              <Input
                id="birthday"
                type="date"
                value={birthday}
                onChange={(e) => setBirthday(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="phone">{t("profile.form.phone")}</Label>
              <Input
                id="phone"
                type="tel"
                value={phoneNumber}
                onChange={(e) => setPhoneNumber(e.target.value)}
                placeholder={t("profile.form.phonePlaceholder")}
              />
            </div>
          </div>

          <Separator />

          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <div>
                <Label>{t("profile.form.customFields")}</Label>
                <p className="text-xs text-muted-foreground">
                  {t("profile.form.customFieldsHint")}
                </p>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={addField}
              >
                {t("profile.form.addField")}
              </Button>
            </div>
            <div className="space-y-2">
              {fields.length === 0 ? (
                <p className="rounded-md border border-dashed bg-muted/30 px-3 py-4 text-center text-xs text-muted-foreground">
                  {t("profile.form.noFields")}
                </p>
              ) : (
                fields.map((f, i) => (
                  <div key={i} className="grid grid-cols-[1fr_2fr_auto] gap-2">
                    <Input
                      value={f[0]}
                      placeholder={t("profile.form.keyPlaceholder")}
                      maxLength={100}
                      onChange={(e) => setField(i, e.target.value, f[1])}
                    />
                    <Input
                      value={f[1]}
                      placeholder={t("profile.form.valuePlaceholder")}
                      maxLength={500}
                      onChange={(e) => setField(i, f[0], e.target.value)}
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      onClick={() => removeField(i)}
                      aria-label={t("profile.form.remove")}
                    >
                      ×
                    </Button>
                  </div>
                ))
              )}
            </div>
          </div>
        </CardContent>
        <CardFooter className="flex items-center justify-between gap-2">
          <div className="space-x-2 text-sm">
            {user?.role === "admin" && <Badge variant="secondary">admin</Badge>}
            {message && (
              <span className="text-muted-foreground">{message}</span>
            )}
            {error && <span className="text-destructive">{error}</span>}
          </div>
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {t("profile.form.saveProfile")}
          </Button>
        </CardFooter>
      </form>
    </Card>
  );
}

function PasswordSection() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMessage(null);
    setError(null);
    if (next !== confirm) {
      setError(t("auth.register.passwordsDoNotMatch"));
      return;
    }
    setBusy(true);
    try {
      await api.post("/api/auth/password", {
        current_password: current,
        new_password: next,
      });
      setMessage(t("profile.password.changed"));
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      setError(
        isApiError(err)
          ? (err.error_description ?? err.error)
          : "Change failed",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">
          {t("profile.password.title")}
        </CardTitle>
        <CardDescription>{t("profile.password.description")}</CardDescription>
      </CardHeader>
      <form onSubmit={submit}>
        <CardContent className="space-y-4">
          <div className="grid gap-2">
            <Label htmlFor="current">{t("profile.password.current")}</Label>
            <Input
              id="current"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              required
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="new">{t("profile.password.new")}</Label>
              <Input
                id="new"
                type="password"
                autoComplete="new-password"
                minLength={8}
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="confirm">{t("profile.password.confirm")}</Label>
              <Input
                id="confirm"
                type="password"
                autoComplete="new-password"
                minLength={8}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
              />
            </div>
          </div>
        </CardContent>
        <CardFooter className="flex items-center justify-between">
          <p className="text-sm">
            {message && (
              <span className="text-muted-foreground">{message}</span>
            )}
            {error && <span className="text-destructive">{error}</span>}
          </p>
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {t("profile.password.submit")}
          </Button>
        </CardFooter>
      </form>
    </Card>
  );
}
