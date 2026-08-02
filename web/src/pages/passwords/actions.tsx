// Vault-level actions: export/import the encrypted backup bundle, change the
// master password, and create folders. Reached from the vault list header.
//
// Export/import move ciphertext only: the downloaded bundle is encrypted with
// the vault key, so it is safe to store anywhere; importing re-inserts the
// ciphertext (re-encrypted by the client if it came from another vault).

import { useRef, useState } from "react";
import { Download, FolderPlus, KeyRound, Loader2, Upload } from "lucide-react";
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
import { Label } from "@/components/ui/label";
import { isApiError } from "@/lib/api";
import type { ExportBundle } from "@/lib/vault/api";
import {
  evaluateMasterPassword,
  readVaultSecuritySettings,
  vaultSecurityOptions,
  writeVaultSecuritySettings,
} from "@/lib/vault/security";
import { WrongMasterPassword, useVault } from "@/lib/vault/store";
import { PasswordStrengthHint } from "./password-strength";

export function VaultActions() {
  const { t } = useTranslation();
  const { exportBundle, importBundle, changeMasterPassword, createFolder } =
    useVault();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);
  const importInput = useRef<HTMLInputElement>(null);

  async function onExport() {
    setBusy(true);
    setError(null);
    setInfo(null);
    try {
      const bundle = await exportBundle();
      const text = JSON.stringify(bundle, null, 2);
      const url = URL.createObjectURL(
        new Blob([text], { type: "application/json" }),
      );
      const a = document.createElement("a");
      a.href = url;
      a.download = "airbrew-vault-export.json";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      setInfo(t("passwords.actions.exported"));
    } catch (err) {
      setError(fmt(err));
    } finally {
      setBusy(false);
    }
  }

  async function onImport(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    setBusy(true);
    setError(null);
    setInfo(null);
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as ExportBundle;
      const counts = await importBundle(parsed);
      setInfo(
        t("passwords.actions.imported", {
          folders: counts.folders,
          items: counts.items,
        }),
      );
    } catch (err) {
      setError(fmt(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <KeyRound className="h-4 w-4" />
          {t("passwords.actions.title")}
        </CardTitle>
        <CardDescription>{t("passwords.actions.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onExport}
            disabled={busy}
          >
            {busy ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Download className="h-4 w-4" />
            )}
            {t("passwords.actions.export")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => importInput.current?.click()}
            disabled={busy}
          >
            <Upload className="h-4 w-4" />
            {t("passwords.actions.import")}
          </Button>
          <input
            ref={importInput}
            type="file"
            accept="application/json,.json"
            className="hidden"
            onChange={onImport}
          />
        </div>
        <CreateFolder
          onCreate={createFolder}
          disabled={busy}
          onError={setError}
        />
        <ChangeMasterPassword
          onChange={changeMasterPassword}
          disabled={busy}
          onError={setError}
          onSuccess={() => setInfo(t("passwords.actions.passwordChanged"))}
        />
        <SecuritySettings />
        {error && <p className="text-sm text-destructive">{error}</p>}
        {info && (
          <p className="text-sm text-green-600 dark:text-green-500">{info}</p>
        )}
      </CardContent>
    </Card>
  );
}

function CreateFolder({
  onCreate,
  disabled,
  onError,
}: {
  onCreate: (name: string) => Promise<unknown>;
  disabled: boolean;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="flex items-end gap-2">
      <div className="grid flex-1 gap-1">
        <Label htmlFor="newfolder">{t("passwords.actions.newFolder")}</Label>
        <Input
          id="newfolder"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("passwords.actions.newFolderPlaceholder")}
        />
      </div>
      <Button
        type="button"
        size="sm"
        disabled={disabled || busy || !name.trim()}
        onClick={async () => {
          setBusy(true);
          try {
            await onCreate(name.trim());
            setName("");
          } catch (err) {
            onError(fmt(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <FolderPlus className="h-4 w-4" />
        {t("passwords.actions.add")}
      </Button>
    </div>
  );
}

function ChangeMasterPassword({
  onChange,
  disabled,
  onError,
  onSuccess,
}: {
  onChange: (current: string, next: string) => Promise<void>;
  disabled: boolean;
  onError: (msg: string) => void;
  onSuccess: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const strength = evaluateMasterPassword(next);

  if (!open) {
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(true)}
      >
        <KeyRound className="h-4 w-4" />
        {t("passwords.actions.changePassword")}
      </Button>
    );
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!strength.acceptable) return onError(t("passwords.setup.tooWeak"));
    if (next !== confirm) return onError(t("passwords.setup.mismatch"));
    setBusy(true);
    try {
      await onChange(cur, next);
      setOpen(false);
      setCur("");
      setNext("");
      setConfirm("");
      onSuccess();
    } catch (err) {
      onError(
        err instanceof WrongMasterPassword
          ? t("passwords.unlock.wrong")
          : fmt(err),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={submit}
      className="grid gap-2 rounded-md border bg-muted/20 p-3"
    >
      <div className="grid gap-1">
        <Label htmlFor="curmp">{t("passwords.actions.currentPassword")}</Label>
        <Input
          id="curmp"
          type="password"
          autoComplete="current-password"
          value={cur}
          onChange={(e) => setCur(e.target.value)}
        />
      </div>
      <div className="grid gap-1">
        <Label htmlFor="newmp">{t("passwords.actions.newPassword")}</Label>
        <Input
          id="newmp"
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
        <PasswordStrengthHint password={next} />
      </div>
      <div className="grid gap-1">
        <Label htmlFor="confmp">{t("passwords.setup.confirm")}</Label>
        <Input
          id="confmp"
          type="password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
      </div>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={disabled || busy}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          {t("passwords.actions.save")}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setOpen(false)}
        >
          {t("passwords.editor.cancel")}
        </Button>
      </div>
    </form>
  );
}

function SecuritySettings() {
  const { t } = useTranslation();
  const [settings, setSettings] = useState(() => readVaultSecuritySettings());
  const options = vaultSecurityOptions();

  function update(next: Partial<typeof settings>) {
    setSettings(writeVaultSecuritySettings({ ...settings, ...next }));
  }

  return (
    <div className="grid gap-3 rounded-md border bg-muted/20 p-3 sm:grid-cols-2">
      <div className="grid gap-1">
        <Label htmlFor="vault-auto-lock">
          {t("passwords.actions.autoLock")}
        </Label>
        <select
          id="vault-auto-lock"
          className="h-9 rounded-md border border-input bg-background px-2 text-sm"
          value={settings.autoLockMinutes}
          onChange={(e) => update({ autoLockMinutes: Number(e.target.value) })}
        >
          {options.autoLockMinutes.map((m) => (
            <option key={m} value={m}>
              {t("passwords.actions.minutes", { count: m })}
            </option>
          ))}
        </select>
      </div>
      <div className="grid gap-1">
        <Label htmlFor="vault-clipboard-clear">
          {t("passwords.actions.clipboardClear")}
        </Label>
        <select
          id="vault-clipboard-clear"
          className="h-9 rounded-md border border-input bg-background px-2 text-sm"
          value={settings.clipboardClearSeconds}
          onChange={(e) =>
            update({ clipboardClearSeconds: Number(e.target.value) })
          }
        >
          {options.clipboardClearSeconds.map((s) => (
            <option key={s} value={s}>
              {t("passwords.actions.seconds", { count: s })}
            </option>
          ))}
        </select>
      </div>
    </div>
  );
}

function fmt(err: unknown): string {
  if (isApiError(err)) return err.error_description ?? err.error;
  return err instanceof Error ? err.message : String(err);
}
