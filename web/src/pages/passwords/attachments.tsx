import { useConfirm } from "@/components/ui/confirm";
// Attachments card shared by the item view (read-only) and editor (manageable).
// Attachments are large, so uploads and deletes take effect immediately rather
// than waiting for the editor's Save button.

import { useEffect, useRef, useState } from "react";
import { Download, Loader2, Paperclip, Upload, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { isApiError } from "@/lib/api";
import { useVault, type Attachment } from "@/lib/vault/store";

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function AttachmentsCard({
  itemId,
  editable,
}: {
  itemId: string;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const {
    listAttachments,
    uploadAttachment,
    deleteAttachment,
    downloadAttachment,
  } = useVault();
  const [atts, setAtts] = useState<Attachment[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let alive = true;
    setAtts(null);
    listAttachments(itemId)
      .then((a) => {
        if (alive) setAtts(a);
      })
      .catch((e) => {
        if (alive)
          setError(
            isApiError(e) ? (e.error_description ?? e.error) : String(e),
          );
      });
    return () => {
      alive = false;
    };
  }, [itemId, listAttachments]);

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const att = await uploadAttachment(itemId, file);
      setAtts((prev) => [...(prev ?? []), att]);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  async function onDownload(att: Attachment) {
    setBusy(true);
    setError(null);
    try {
      const { blob, name } = await downloadAttachment(itemId, att);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) {
      setError(
        isApiError(err) ? (err.error_description ?? err.error) : String(err),
      );
    } finally {
      setBusy(false);
    }
  }

  async function onRemove(att: Attachment) {
    if (
      !(await confirm({
        title: t("passwords.attachments.confirmDelete"),
        destructive: true,
        confirmLabel: t("common.delete"),
        cancelLabel: t("common.cancel"),
      }))
    )
      return;
    setBusy(true);
    setError(null);
    try {
      await deleteAttachment(itemId, att.id);
      setAtts((prev) => (prev ?? []).filter((a) => a.id !== att.id));
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
          <Paperclip className="h-4 w-4" />
          {t("passwords.attachments.title")}
        </CardTitle>
        <CardDescription>
          {t("passwords.attachments.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {atts === null ? (
          <div className="flex justify-center py-4">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          </div>
        ) : atts.length === 0 ? (
          <p className="rounded-md border border-dashed bg-muted/30 px-3 py-6 text-center text-xs text-muted-foreground">
            {t("passwords.attachments.empty")}
          </p>
        ) : (
          atts.map((att) => (
            <div
              key={att.id}
              className="flex items-center justify-between gap-2 rounded-md border bg-muted/20 px-3 py-2"
            >
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{att.name}</p>
                <p className="text-xs text-muted-foreground">
                  {formatBytes(att.size)}
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                onClick={() => onDownload(att)}
                disabled={busy}
                title={t("passwords.attachments.download")}
              >
                <Download className="h-4 w-4" />
              </Button>
              {editable && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => onRemove(att)}
                  disabled={busy}
                  title={t("passwords.attachments.remove")}
                >
                  <X className="h-4 w-4" />
                </Button>
              )}
            </div>
          ))
        )}

        {error && <p className="text-sm text-destructive">{error}</p>}

        {editable && (
          <>
            <input
              ref={fileInput}
              type="file"
              className="hidden"
              onChange={onPick}
              disabled={busy}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => fileInput.current?.click()}
              disabled={busy}
            >
              {busy ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Upload className="h-4 w-4" />
              )}
              {t("passwords.attachments.upload")}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  );
}
