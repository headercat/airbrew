// Password item view (read-only detail). Reached from the list by clicking an
// item. Sensitive fields reveal on demand, TOTP fields show the live rotating
// code, and the only way to change anything is the Edit button.

import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  ArrowLeft,
  Check,
  Clock,
  Copy,
  CreditCard,
  Eye,
  EyeOff,
  Globe,
  History,
  Pencil,
  StickyNote,
  Star,
  Trash2,
  UserRound,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { PageWrapper } from "@/components/page";
import { isApiError } from "@/lib/api";
import { copyAndAutoClear } from "@/lib/vault/clipboard";
import { useVault, type DecryptedItem, type Field } from "@/lib/vault/store";
import { generateTotp, type TotpCode } from "@/lib/vault/totp";
import { AttachmentsCard } from "./attachments";

function typeIcon(type: DecryptedItem["type"]) {
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

export default function PasswordView() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { status, items, deleteItem } = useVault();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const item = items.find((it) => it.id === id);

  // Redirect when the vault is no longer unlocked. Done in an effect (not
  // during render) so we don't trigger a state update mid-render.
  useEffect(() => {
    if (status !== "unlocked") navigate("/passwords");
  }, [status, navigate]);

  // Reprompt gate: items flagged reprompt hide the whole decrypted detail area
  // until the user re-enters the master password. Verified state is kept only
  // for this view session and resets on navigation away.
  const { verifyMasterPassword } = useVault();
  const [repromptVerified, setRepromptVerified] = useState(false);
  const [repromptBusy, setRepromptBusy] = useState(false);
  const [repromptError, setRepromptError] = useState<string | null>(null);
  const [repromptPw, setRepromptPw] = useState("");

  if (status !== "unlocked") return null;

  if (!item) {
    return (
      <PageWrapper>
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            {t("passwords.view.notFound")}
          </CardContent>
        </Card>
      </PageWrapper>
    );
  }

  const sensitiveGateActive = item.reprompt && !repromptVerified;

  async function onDelete() {
    if (!item) return;
    if (!confirm(t("passwords.editor.confirmDelete"))) return;
    setBusy(true);
    setError(null);
    try {
      await deleteItem(item.id, item.revision);
      navigate("/passwords");
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  const Icon = typeIcon(item.type);

  return (
    <PageWrapper>
      {/* header */}
      <div className="flex items-start justify-between gap-4">
        <div className="flex items-start gap-3">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => navigate("/passwords")}
            className="-ml-2"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="flex h-10 w-10 items-center justify-center rounded-md bg-muted">
            <Icon className="h-5 w-5 text-muted-foreground" />
          </div>
          <div className="space-y-1">
            <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
              {item.name}
              {item.favorite && (
                <Star className="h-4 w-4 fill-amber-500 text-amber-500" />
              )}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t(`passwords.types.${item.type}`)}
            </p>
          </div>
        </div>
        <Button
          variant="outline"
          onClick={() => navigate(`/passwords/${item.id}/edit`)}
        >
          <Pencil className="h-4 w-4" />
          {t("passwords.view.edit")}
        </Button>
      </div>

      {/* fields */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("passwords.view.fields")}
          </CardTitle>
          <CardDescription>
            {t("passwords.view.fieldsDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {sensitiveGateActive && (
            <div className="space-y-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-3">
              <p className="text-xs text-amber-700 dark:text-amber-400">
                {t("passwords.view.repromptRequired")}
              </p>
              <div className="flex items-center gap-2">
                <Input
                  type="password"
                  autoComplete="current-password"
                  value={repromptPw}
                  onChange={(e) => setRepromptPw(e.target.value)}
                  placeholder={t("passwords.unlock.masterPassword")}
                  className="h-8"
                />
                <Button
                  type="button"
                  size="sm"
                  disabled={repromptBusy || !repromptPw}
                  onClick={async () => {
                    setRepromptBusy(true);
                    setRepromptError(null);
                    try {
                      const ok = await verifyMasterPassword(repromptPw);
                      if (ok) setRepromptVerified(true);
                      else setRepromptError(t("passwords.unlock.wrong"));
                    } finally {
                      setRepromptBusy(false);
                      setRepromptPw("");
                    }
                  }}
                >
                  {t("passwords.view.reveal")}
                </Button>
              </div>
              {repromptError && (
                <p className="text-xs text-destructive">{repromptError}</p>
              )}
            </div>
          )}
          {sensitiveGateActive ? (
            <p className="rounded-md border border-dashed bg-muted/30 px-3 py-6 text-center text-xs text-muted-foreground">
              {t("passwords.view.lockedContent")}
            </p>
          ) : item.fields.length === 0 ? (
            <p className="rounded-md border border-dashed bg-muted/30 px-3 py-6 text-center text-xs text-muted-foreground">
              {t("passwords.view.noFields")}
            </p>
          ) : (
            item.fields.map((f) => <FieldRow key={f.id} field={f} />)
          )}
        </CardContent>
      </Card>

      {/* notes */}
      {!sensitiveGateActive && item.notes && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              {t("passwords.editor.notes")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="whitespace-pre-wrap text-sm">{item.notes}</p>
          </CardContent>
        </Card>
      )}

      {!sensitiveGateActive && (
        <AttachmentsCard itemId={item.id} editable={false} />
      )}

      {!sensitiveGateActive && <HistoryCard itemId={item.id} />}

      {error && <p className="text-sm text-destructive">{error}</p>}

      <div className="flex justify-end">
        <Button variant="destructive" onClick={onDelete} disabled={busy}>
          <Trash2 className="h-4 w-4" />
          {t("passwords.editor.delete")}
        </Button>
      </div>
    </PageWrapper>
  );
}

// ---- field rendering ----

function CopyButton({
  value,
  disabled,
}: {
  value: string;
  disabled?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    if (!value) return;
    await copyAndAutoClear(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      onClick={copy}
      disabled={disabled}
      className="h-8 w-8"
    >
      {copied ? (
        <Check className="h-4 w-4 text-green-500" />
      ) : (
        <Copy className="h-4 w-4" />
      )}
    </Button>
  );
}

function FieldRow({ field }: { field: Field }) {
  const { t } = useTranslation();
  const label = field.name || t(`passwords.kinds.${field.kind}`);

  if (field.kind === "totp") {
    return <TotpField label={label} secret={field.value} />;
  }

  if (field.kind === "multiline") {
    return (
      <div className="grid gap-1">
        <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </span>
        <div className="flex items-start justify-between gap-2">
          <p className="whitespace-pre-wrap break-all rounded-md bg-muted/40 px-3 py-2 text-sm">
            {field.value || <span className="text-muted-foreground">—</span>}
          </p>
          <CopyButton value={field.value} />
        </div>
      </div>
    );
  }

  if (field.kind === "url") {
    const href = field.value.startsWith("http")
      ? field.value
      : `https://${field.value}`;
    return (
      <div className="grid gap-1">
        <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </span>
        <div className="flex items-center justify-between gap-2">
          <a
            href={href}
            target="_blank"
            rel="noreferrer"
            className="truncate text-sm text-primary hover:underline"
          >
            {field.value || <span className="text-muted-foreground">—</span>}
          </a>
          <CopyButton value={field.value} />
        </div>
      </div>
    );
  }

  if (field.kind === "password") {
    return <SecretField label={label} value={field.value} />;
  }

  // text
  return (
    <div className="grid gap-1">
      <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <div className="flex items-center justify-between gap-2">
        <span className="break-all text-sm">
          {field.value || <span className="text-muted-foreground">—</span>}
        </span>
        <CopyButton value={field.value} />
      </div>
    </div>
  );
}

function SecretField({ label, value }: { label: string; value: string }) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className="grid gap-1">
      <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <div className="flex items-center justify-between gap-2">
        <span className="break-all font-mono text-sm">
          {value ? (
            revealed ? (
              value
            ) : (
              "•".repeat(Math.min(value.length, 16))
            )
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </span>
        <div className="flex items-center">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={() => setRevealed((r) => !r)}
            disabled={!value}
            title={label}
          >
            {revealed ? (
              <EyeOff className="h-4 w-4" />
            ) : (
              <Eye className="h-4 w-4" />
            )}
          </Button>
          <CopyButton value={value} />
        </div>
      </div>
    </div>
  );
}

function TotpField({ label, secret }: { label: string; secret: string }) {
  const { t } = useTranslation();
  const [code, setCode] = useState<TotpCode | null>(null);

  useEffect(() => {
    if (!secret) {
      setCode(null);
      return;
    }
    let alive = true;
    const tick = async () => {
      try {
        const c = await generateTotp(secret);
        if (alive) setCode(c);
      } catch {
        if (alive) setCode(null);
      }
    };
    void tick();
    const i = setInterval(tick, 1000);
    return () => {
      alive = false;
      clearInterval(i);
    };
  }, [secret]);

  return (
    <div className="grid gap-1">
      <span className="flex items-center gap-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Clock className="h-3 w-3" />
        {label}
      </span>
      <div className="flex items-center justify-between gap-2">
        {code ? (
          <div className="flex items-center gap-3">
            <span className="font-mono text-2xl font-semibold tracking-[0.2em]">
              {code.code}
            </span>
            <span className="text-xs text-muted-foreground">
              {code.remaining}s
            </span>
          </div>
        ) : (
          <span className="text-sm text-muted-foreground">
            {secret
              ? t("passwords.view.totpInvalid")
              : t("passwords.view.totpEmpty")}
          </span>
        )}
        <CopyButton value={code?.code ?? ""} disabled={!code} />
      </div>
    </div>
  );
}

// ---- history ----

type RevSummary = {
  id: string;
  name: string;
  revision: number;
  createdAt: string;
};

function HistoryCard({ itemId }: { itemId: string }) {
  const { t } = useTranslation();
  const { listItemRevisions, restoreItemRevision } = useVault();
  const [revs, setRevs] = useState<RevSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setBusy(true);
    setError(null);
    try {
      setRevs(await listItemRevisions(itemId));
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  // Lazy-load on first expand.
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (open && revs === null) void load();
  }, [open, revs, load]);

  async function onRestore(revId: string) {
    if (!confirm(t("passwords.view.confirmRestore"))) return;
    setBusy(true);
    setError(null);
    try {
      await restoreItemRevision(itemId, revId);
      setRevs(null); // force reload next expand
      setOpen(false);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <History className="h-4 w-4" />
          {t("passwords.view.history")}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setOpen((o) => !o)}
          >
            {open ? t("passwords.view.hide") : t("passwords.view.show")}
          </Button>
        </CardTitle>
      </CardHeader>
      {open && (
        <CardContent className="space-y-1">
          {busy && revs === null && (
            <p className="text-xs text-muted-foreground">
              {t("common.loading")}
            </p>
          )}
          {revs?.length === 0 && (
            <p className="text-xs text-muted-foreground">
              {t("passwords.view.noHistory")}
            </p>
          )}
          {revs?.map((r) => (
            <div
              key={r.id}
              className="flex items-center justify-between gap-2 rounded-md border bg-muted/20 px-3 py-2"
            >
              <div className="min-w-0">
                <p className="truncate text-sm">{r.name}</p>
                <p className="text-xs text-muted-foreground">
                  {new Date(r.createdAt).toLocaleString()} · rev {r.revision}
                </p>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => onRestore(r.id)}
              >
                {t("passwords.view.restore")}
              </Button>
            </div>
          ))}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
      )}
    </Card>
  );
}
