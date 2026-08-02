// Password vault entry page. Acts as a state-machine gate driven by the vault
// store: loading → setup (first run) → unlock (locked) → list (unlocked).
// The editor lives on its own routes (/passwords/new, /passwords/:id).

import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  CreditCard,
  Globe,
  KeyRound,
  Loader2,
  Lock,
  Plus,
  RefreshCw,
  Search,
  Star,
  StickyNote,
  UserRound,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader, PageWrapper } from "@/components/page";
import {
  WrongMasterPassword,
  useVault,
  type DecryptedItem,
} from "@/lib/vault/store";
import { evaluateMasterPassword } from "@/lib/vault/security";
import { VaultActions } from "./actions";
import { PasswordStrengthHint } from "./password-strength";

export default function PasswordsPage() {
  const { status, error } = useVault();

  if (status === "unknown") {
    return (
      <div className="flex h-full items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (error && status !== "unlocked") {
    return (
      <PageWrapper>
        <Card>
          <CardHeader>
            <CardTitle>{error}</CardTitle>
            <CardDescription>Try again in a moment.</CardDescription>
          </CardHeader>
        </Card>
      </PageWrapper>
    );
  }
  if (status === "not_setup") return <SetupView />;
  if (status === "locked") return <UnlockView />;
  return <VaultListView />;
}

// ---- first-time setup ----

function SetupView() {
  const { t } = useTranslation();
  const { setupAndUnlock, busy } = useVault();
  const [pw, setPw] = useState("");
  const [confirm, setConfirm] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const strength = useMemo(() => evaluateMasterPassword(pw), [pw]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    if (!strength.acceptable) return setErr(t("passwords.setup.tooWeak"));
    if (pw !== confirm) return setErr(t("passwords.setup.mismatch"));
    try {
      await setupAndUnlock(pw);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <div className="flex h-full items-center justify-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-2 flex h-10 w-10 items-center justify-center rounded-full bg-primary/10">
            <KeyRound className="h-5 w-5 text-primary" />
          </div>
          <CardTitle>{t("passwords.setup.title")}</CardTitle>
          <CardDescription>{t("passwords.setup.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="mp">{t("passwords.setup.masterPassword")}</Label>
              <Input
                id="mp"
                type="password"
                autoComplete="new-password"
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                placeholder={t("passwords.setup.masterPasswordPlaceholder")}
              />
              <PasswordStrengthHint password={pw} meter />
            </div>
            <div className="space-y-2">
              <Label htmlFor="mpc">{t("passwords.setup.confirm")}</Label>
              <Input
                id="mpc"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </div>
            <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
              {t("passwords.setup.warning")}
            </p>
            {err && <p className="text-sm text-destructive">{err}</p>}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("passwords.setup.submit")}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// ---- unlock (locked state) ----

function UnlockView() {
  const { t } = useTranslation();
  const { unlock, busy } = useVault();
  const [pw, setPw] = useState("");
  const [err, setErr] = useState<string | null>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    try {
      await unlock(pw);
    } catch (e) {
      if (e instanceof WrongMasterPassword) setErr(t("passwords.unlock.wrong"));
      else setErr(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <div className="flex h-full items-center justify-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-2 flex h-10 w-10 items-center justify-center rounded-full bg-primary/10">
            <Lock className="h-5 w-5 text-primary" />
          </div>
          <CardTitle>{t("passwords.unlock.title")}</CardTitle>
          <CardDescription>{t("passwords.unlock.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="ulpw">
                {t("passwords.unlock.masterPassword")}
              </Label>
              <Input
                id="ulpw"
                type="password"
                autoComplete="current-password"
                autoFocus
                value={pw}
                onChange={(e) => setPw(e.target.value)}
              />
            </div>
            {err && <p className="text-sm text-destructive">{err}</p>}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("passwords.unlock.submit")}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// ---- unlocked list ----

function itemIcon(type: DecryptedItem["type"]) {
  switch (type) {
    case "login":
      return Globe;
    case "card":
      return CreditCard;
    case "identity":
      return UserRound;
    default:
      return StickyNote;
  }
}

// itemSubtitle returns the first displayable field value (text or url) so the
// list row previews something useful; falls back to the type label.
function itemSubtitle(it: DecryptedItem, t: TFunction): string {
  if (it.reprompt) return t("passwords.list.protected");
  const first = it.fields.find(
    (f) => (f.kind === "text" || f.kind === "url") && f.value,
  );
  if (first) return first.value;
  return t(`passwords.types.${it.type}`);
}

function VaultListView() {
  const { t } = useTranslation();
  const { items, folders, busy, lock, refresh } = useVault();
  const [query, setQuery] = useState("");
  const navigate = useNavigate();

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter((it) => {
      if (it.name.toLowerCase().includes(q)) return true;
      if (it.reprompt) {
        return it.fields.some((f) => f.name.toLowerCase().includes(q));
      }
      return it.fields.some(
        (f) =>
          f.name.toLowerCase().includes(q) || f.value.toLowerCase().includes(q),
      );
    });
  }, [items, query]);

  return (
    <PageWrapper>
      <PageHeader
        title={t("passwords.list.title")}
        description={t("passwords.list.description", { count: items.length })}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="icon"
              onClick={() => refresh()}
              disabled={busy}
              title={t("passwords.list.refresh")}
            >
              {busy ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="h-4 w-4" />
              )}
            </Button>
            <Button
              variant="outline"
              size="icon"
              onClick={lock}
              title={t("passwords.list.lock")}
            >
              <Lock className="h-4 w-4" />
            </Button>
            <Button onClick={() => navigate("/passwords/new")}>
              <Plus className="h-4 w-4" />
              {t("passwords.list.add")}
            </Button>
          </div>
        }
      />

      <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          className="pl-9"
          placeholder={t("passwords.list.search")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      {folders.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {folders.map((f) => (
            <Badge key={f.id} variant="secondary" className="gap-1">
              <KeyRound className="h-3 w-3" />
              {f.name}
            </Badge>
          ))}
        </div>
      )}

      <VaultActions />

      {filtered.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-2 py-12 text-center">
            <KeyRound className="h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              {query
                ? t("passwords.list.noMatches")
                : t("passwords.list.empty")}
            </p>
            {!query && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => navigate("/passwords/new")}
              >
                <Plus className="h-4 w-4" />
                {t("passwords.list.add")}
              </Button>
            )}
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-1">
          {filtered.map((it) => {
            const Icon = itemIcon(it.type);
            return (
              <Link
                key={it.id}
                to={`/passwords/${it.id}`}
                className="flex items-center gap-3 rounded-lg border bg-card px-3 py-2.5 transition-colors hover:bg-accent/50"
              >
                <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted">
                  <Icon className="h-4 w-4 text-muted-foreground" />
                </div>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{it.name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {itemSubtitle(it, t)}
                  </p>
                </div>
                {it.favorite && (
                  <Star className="h-3.5 w-3.5 fill-amber-500 text-amber-500" />
                )}
              </Link>
            );
          })}
        </div>
      )}
    </PageWrapper>
  );
}
