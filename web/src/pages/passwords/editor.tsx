// Password item editor. Handles both create (/passwords/new) and edit
// (/passwords/:id/edit). Fields are free-form: the item "type" only picks an
// initial preset and the list icon — every field can be renamed, retyped,
// added or removed. All fields are encrypted by the store before upload.

import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  Eye,
  EyeOff,
  Loader2,
  Plus,
  RefreshCw,
  Star,
  Trash2,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";

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
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import { isApiError } from "@/lib/api";
import { isConflict } from "@/lib/vault/api";
import { generatePassword } from "@/lib/vault/crypto";
import {
  newField,
  PRESETS,
  WrongMasterPassword,
  useVault,
  type DraftItem,
  type Field,
  type FieldKind,
} from "@/lib/vault/store";
import type { VaultItem, VaultItemType } from "@/lib/vault/api";
import { AttachmentsCard } from "./attachments";

const TYPES: VaultItemType[] = ["login", "secure_note", "card", "identity"];
const KINDS: FieldKind[] = ["text", "password", "totp", "url", "multiline"];

const selectClass =
  "h-10 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2";

function clonePreset(type: VaultItemType): Field[] {
  return PRESETS[type].map((f) => newField(f.kind, f.name));
}

export default function PasswordEditor() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const isEdit = Boolean(id);
  const navigate = useNavigate();
  const {
    status,
    items,
    folders,
    createItem,
    updateItem,
    deleteItem,
    refresh,
    verifyMasterPassword,
  } = useVault();

  const existing = items.find((it) => it.id === id);

  const [type, setType] = useState<VaultItemType>("login");
  const [name, setName] = useState("");
  const [fields, setFields] = useState<Field[]>(() => clonePreset("login"));
  const [notes, setNotes] = useState("");
  const [folderId, setFolderId] = useState("");
  const [favorite, setFavorite] = useState(false);
  const [reprompt, setReprompt] = useState(false);

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<VaultItem | null>(null);
  const [repromptVerified, setRepromptVerified] = useState(false);

  // Hydrate form when editing.
  useEffect(() => {
    if (!existing) return;
    setType(existing.type);
    setName(existing.name);
    setFields(
      existing.fields.length ? existing.fields.map((f) => ({ ...f })) : [],
    );
    setNotes(existing.notes);
    setFolderId(existing.folderId);
    setFavorite(existing.favorite);
    setReprompt(existing.reprompt);
  }, [existing]);

  // Redirect when the vault is no longer unlocked. Done in an effect (not
  // during render) to avoid a state update mid-render.
  useEffect(() => {
    if (status !== "unlocked") navigate("/passwords");
  }, [status, navigate]);

  if (status !== "unlocked") return null;

  if (isEdit && !existing) {
    return (
      <PageWrapper>
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            {t("passwords.editor.notFound")}
          </CardContent>
        </Card>
      </PageWrapper>
    );
  }

  if (isEdit && existing?.reprompt && !repromptVerified) {
    return (
      <RepromptEditorGate
        onCancel={() => navigate(`/passwords/${existing.id}`)}
        onVerify={async (password) => {
          const ok = await verifyMasterPassword(password);
          if (!ok) throw new WrongMasterPassword();
          setRepromptVerified(true);
        }}
      />
    );
  }

  function patchField(idx: number, patch: Partial<Field>) {
    setFields((prev) =>
      prev.map((f, i) => (i === idx ? { ...f, ...patch } : f)),
    );
  }
  function removeField(idx: number) {
    setFields((prev) => prev.filter((_, i) => i !== idx));
  }
  function addField() {
    setFields((prev) => [...prev, newField("text")]);
  }

  // Changing the type swaps in that preset's fields (the type only seeds
  // fields + picks the icon). If the user already typed something, confirm
  // before discarding it.
  function applyType(next: VaultItemType) {
    if (next === type) return;
    const hasContent = fields.some((f) => f.name.trim() || f.value.trim());
    if (hasContent && !confirm(t("passwords.editor.confirmReplacePreset"))) {
      return;
    }
    setType(next);
    setFields(clonePreset(next));
  }

  function buildDraft(): DraftItem {
    const cleanFields = fields.filter((f) => f.name.trim() || f.value.trim());
    return {
      type,
      folderId,
      name,
      notes,
      fields: cleanFields,
      favorite,
      reprompt,
    };
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setConflict(null);
    setBusy(true);
    try {
      if (isEdit && existing) {
        await updateItem(existing.id, buildDraft(), existing.revision);
      } else {
        await createItem(buildDraft());
      }
      navigate("/passwords");
    } catch (err) {
      if (isConflict(err)) {
        // A 409 means the row changed under us. Show the server's current row
        // and offer to re-sync + re-hydrate so the user can reconcile instead
        // of guessing at a stale revision.
        const cur = (err as { current?: VaultItem }).current;
        setConflict(cur ?? null);
        setError(t("passwords.editor.conflict"));
      } else if (isApiError(err)) {
        setError(err.error_description ?? err.error);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(false);
    }
  }

  async function onDelete() {
    if (!existing) return;
    if (!confirm(t("passwords.editor.confirmDelete"))) return;
    setBusy(true);
    setError(null);
    try {
      await deleteItem(existing.id, existing.revision);
      navigate("/passwords");
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <PageWrapper>
      <PageHeader
        title={
          isEdit
            ? t("passwords.editor.editTitle")
            : t("passwords.editor.newTitle")
        }
        description={t("passwords.editor.subtitle")}
      />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("passwords.editor.formTitle")}
          </CardTitle>
          <CardDescription>
            {t("passwords.editor.formDescription")}
          </CardDescription>
        </CardHeader>
        <form onSubmit={onSubmit}>
          <CardContent className="space-y-4">
            {conflict && (
              <div className="space-y-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs">
                <p className="text-amber-700 dark:text-amber-400">
                  {t("passwords.editor.conflict")}
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={async () => {
                    setBusy(true);
                    try {
                      await refresh();
                      setConflict(null);
                      setError(null);
                    } finally {
                      setBusy(false);
                    }
                  }}
                >
                  <RefreshCw className="h-4 w-4" />
                  {t("passwords.editor.resync")}
                </Button>
              </div>
            )}
            <div className="grid gap-2">
              <Label htmlFor="name">{t("passwords.editor.name")}</Label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                autoFocus
              />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="folder">{t("passwords.editor.folder")}</Label>
              <select
                id="folder"
                value={folderId}
                onChange={(e) => setFolderId(e.target.value)}
                className={selectClass}
              >
                <option value="">{t("passwords.editor.noFolder")}</option>
                {folders.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.name}
                  </option>
                ))}
              </select>
            </div>

            <div className="grid gap-2">
              <Label>{t("passwords.editor.type")}</Label>
              <div className="flex flex-wrap gap-2">
                {TYPES.map((ty) => (
                  <Button
                    key={ty}
                    type="button"
                    size="sm"
                    variant={type === ty ? "default" : "outline"}
                    onClick={() => applyType(ty)}
                  >
                    {t(`passwords.types.${ty}`)}
                  </Button>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                {t("passwords.editor.typeHint")}
              </p>
            </div>

            {/* custom fields */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>{t("passwords.editor.fields")}</Label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={addField}
                >
                  <Plus className="h-4 w-4" />
                  {t("passwords.editor.addField")}
                </Button>
              </div>
              <div className="space-y-3">
                {fields.length === 0 ? (
                  <p className="rounded-md border border-dashed bg-muted/30 px-3 py-6 text-center text-xs text-muted-foreground">
                    {t("passwords.editor.noFields")}
                  </p>
                ) : (
                  fields.map((f, idx) => (
                    <FieldEditor
                      key={f.id}
                      field={f}
                      onChange={(patch) => patchField(idx, patch)}
                      onRemove={() => removeField(idx)}
                    />
                  ))
                )}
              </div>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="notes">{t("passwords.editor.notes")}</Label>
              <Textarea
                id="notes"
                value={notes}
                onChange={(e) => setNotes(e.target.value)}
                rows={3}
              />
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <label
                htmlFor="favorite"
                className="flex items-center gap-2 text-sm"
              >
                <Switch
                  id="favorite"
                  checked={favorite}
                  onCheckedChange={setFavorite}
                />
                <Star className="h-4 w-4" />
                {t("passwords.editor.favorite")}
              </label>
              <label
                htmlFor="reprompt"
                className="flex items-center gap-2 text-sm"
              >
                <Switch
                  id="reprompt"
                  checked={reprompt}
                  onCheckedChange={setReprompt}
                />
                {t("passwords.editor.reprompt")}
              </label>
            </div>
          </CardContent>
          <CardFooter className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-sm">
              {isEdit && (
                <Button
                  type="button"
                  variant="destructive"
                  onClick={onDelete}
                  disabled={busy}
                >
                  <Trash2 className="h-4 w-4" />
                  {t("passwords.editor.delete")}
                </Button>
              )}
              {error && <span className="text-destructive">{error}</span>}
            </div>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="ghost"
                onClick={() => navigate("/passwords")}
                disabled={busy}
              >
                {t("passwords.editor.cancel")}
              </Button>
              <Button type="submit" disabled={busy}>
                {busy && <Loader2 className="animate-spin" />}
                {t("passwords.editor.save")}
              </Button>
            </div>
          </CardFooter>
        </form>
      </Card>

      {isEdit && existing && <AttachmentsCard itemId={existing.id} editable />}
    </PageWrapper>
  );
}

function RepromptEditorGate({
  onCancel,
  onVerify,
}: {
  onCancel: () => void;
  onVerify: (password: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [pw, setPw] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onVerify(pw);
    } catch (err) {
      setError(
        err instanceof WrongMasterPassword
          ? t("passwords.unlock.wrong")
          : err instanceof Error
            ? err.message
            : String(err),
      );
    } finally {
      setBusy(false);
      setPw("");
    }
  }

  return (
    <PageWrapper>
      <Card className="mx-auto max-w-md">
        <CardHeader>
          <CardTitle className="text-base">
            {t("passwords.editor.repromptEditTitle")}
          </CardTitle>
          <CardDescription>
            {t("passwords.editor.repromptEditDescription")}
          </CardDescription>
        </CardHeader>
        <form onSubmit={submit}>
          <CardContent className="space-y-3">
            <div className="grid gap-2">
              <Label htmlFor="edit-reprompt">
                {t("passwords.unlock.masterPassword")}
              </Label>
              <Input
                id="edit-reprompt"
                type="password"
                autoComplete="current-password"
                autoFocus
                value={pw}
                onChange={(e) => setPw(e.target.value)}
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
          </CardContent>
          <CardFooter className="justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={onCancel}
              disabled={busy}
            >
              {t("passwords.editor.cancel")}
            </Button>
            <Button type="submit" disabled={busy || !pw}>
              {busy && <Loader2 className="h-4 w-4 animate-spin" />}
              {t("passwords.view.reveal")}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </PageWrapper>
  );
}

// ---- single field editor row ----

function FieldEditor({
  field,
  onChange,
  onRemove,
}: {
  field: Field;
  onChange: (patch: Partial<Field>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const [reveal, setReveal] = useState(false);

  return (
    <div className="grid gap-2 rounded-md border bg-muted/20 p-3">
      <div className="flex items-center gap-2">
        <Input
          value={field.name}
          placeholder={t("passwords.editor.fieldName")}
          onChange={(e) => onChange({ name: e.target.value })}
          className="flex-1"
        />
        <select
          value={field.kind}
          onChange={(e) => onChange({ kind: e.target.value as FieldKind })}
          className={selectClass}
          aria-label={t("passwords.editor.fieldKind")}
        >
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`passwords.kinds.${k}`)}
            </option>
          ))}
        </select>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onRemove}
          aria-label={t("passwords.editor.removeField")}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>

      {field.kind === "multiline" ? (
        <Textarea
          value={field.value}
          placeholder={t("passwords.editor.fieldValue")}
          onChange={(e) => onChange({ value: e.target.value })}
          rows={2}
        />
      ) : (
        <div className="flex gap-2">
          <Input
            type={field.kind === "password" && !reveal ? "password" : "text"}
            value={field.value}
            placeholder={t("passwords.editor.fieldValue")}
            onChange={(e) => onChange({ value: e.target.value })}
            className={
              field.kind === "totp" || field.kind === "password"
                ? "font-mono"
                : ""
            }
          />
          {field.kind === "password" && (
            <Button
              type="button"
              variant="outline"
              size="icon"
              onClick={() => setReveal((r) => !r)}
              title={t("passwords.editor.toggleVisibility")}
            >
              {reveal ? (
                <EyeOff className="h-4 w-4" />
              ) : (
                <Eye className="h-4 w-4" />
              )}
            </Button>
          )}
          {field.kind === "password" && (
            <Button
              type="button"
              variant="outline"
              size="icon"
              onClick={() => onChange({ value: generatePassword() })}
              title={t("passwords.editor.generate")}
            >
              <RefreshCw className="h-4 w-4" />
            </Button>
          )}
        </div>
      )}

      {field.kind === "totp" && (
        <p className="text-xs text-muted-foreground">
          {t("passwords.editor.totpHint")}
        </p>
      )}
    </div>
  );
}
