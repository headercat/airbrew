import { useConfirm } from "@/components/ui/confirm";
// Trash (recycle bin) dialog. Lists soft-deleted items and folders, lets the
// user restore them or permanently delete them, and offers an "empty all"
// shortcut. Soft-deleted rows are purged automatically after 30 days, so the
// dialog notes that window rather than implying indefinite retention.

import { useEffect, useState } from "react";
import { Loader2, RotateCcw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { isApiError } from "@/lib/api";
import { useVault, type TrashEntry } from "@/lib/vault/store";

export function TrashButton() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        variant="outline"
        size="icon"
        onClick={() => setOpen(true)}
        title={t("passwords.trash.open")}
      >
        <Trash2 className="h-4 w-4" />
      </Button>
      <TrashDialog open={open} onClose={() => setOpen(false)} />
    </>
  );
}

function TrashDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const {
    listTrash,
    restoreTrashItem,
    restoreTrashFolder,
    purgeTrashItem,
    purgeTrashFolder,
    emptyTrash,
  } = useVault();
  const [entries, setEntries] = useState<TrashEntry[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    let alive = true;
    setEntries(null);
    setError(null);
    listTrash()
      .then((e) => {
        if (alive) setEntries(e);
      })
      .catch((err) => {
        if (alive) setError(fmt(err));
      });
    return () => {
      alive = false;
    };
  }, [open, listTrash]);

  async function reload() {
    setEntries(null);
    setError(null);
    try {
      setEntries(await listTrash());
    } catch (err) {
      setError(fmt(err));
    }
  }

  async function onRestore(e: TrashEntry) {
    setBusy(true);
    setError(null);
    try {
      if (e.kind === "folder") await restoreTrashFolder(e.id);
      else await restoreTrashItem(e.id);
      await reload();
    } catch (err) {
      setError(fmt(err));
    } finally {
      setBusy(false);
    }
  }

  async function onPurge(e: TrashEntry) {
    if (
      !(await confirm({
        title: t("passwords.trash.confirmPurge"),
        destructive: true,
        confirmLabel: t("common.delete"),
        cancelLabel: t("common.cancel"),
      }))
    )
      return;
    setBusy(true);
    setError(null);
    try {
      if (e.kind === "folder") await purgeTrashFolder(e.id);
      else await purgeTrashItem(e.id);
      await reload();
    } catch (err) {
      setError(fmt(err));
    } finally {
      setBusy(false);
    }
  }

  async function onEmptyAll() {
    if (
      !(await confirm({
        title: t("passwords.trash.confirmEmpty"),
        destructive: true,
        confirmLabel: t("common.delete"),
        cancelLabel: t("common.cancel"),
      }))
    )
      return;
    setBusy(true);
    setError(null);
    try {
      await emptyTrash();
      await reload();
    } catch (err) {
      setError(fmt(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={busy ? () => {} : onClose}
      title={t("passwords.trash.title")}
      description={t("passwords.trash.description")}
    >
      <div className="space-y-3">
        {entries === null ? (
          <div className="flex justify-center py-6">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          </div>
        ) : entries.length === 0 ? (
          <p className="rounded-md border border-dashed bg-muted/30 px-3 py-6 text-center text-xs text-muted-foreground">
            {t("passwords.trash.empty")}
          </p>
        ) : (
          <>
            <div className="space-y-1">
              {entries.map((e) => (
                <div
                  key={`${e.kind}-${e.id}`}
                  className="flex items-center justify-between gap-2 rounded-md border bg-muted/20 px-3 py-2"
                >
                  <div className="min-w-0">
                    <p className="truncate text-sm font-medium">{e.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {e.kind === "folder"
                        ? t("passwords.trash.folder")
                        : t(`passwords.types.${e.type ?? "secure_note"}`)}
                      {" · "}
                      {new Date(e.deletedAt).toLocaleDateString()}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() => onRestore(e)}
                      disabled={busy}
                      title={t("passwords.trash.restore")}
                    >
                      <RotateCcw className="h-4 w-4" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-destructive"
                      onClick={() => onPurge(e)}
                      disabled={busy}
                      title={t("passwords.trash.purge")}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
            <div className="flex items-center justify-between">
              <Label className="text-xs text-muted-foreground">
                {t("passwords.trash.retentionHint")}
              </Label>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={onEmptyAll}
                disabled={busy}
              >
                {t("passwords.trash.emptyAll")}
              </Button>
            </div>
          </>
        )}
        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
    </Modal>
  );
}

function fmt(err: unknown): string {
  if (isApiError(err)) return err.error_description ?? err.error;
  return err instanceof Error ? err.message : String(err);
}
