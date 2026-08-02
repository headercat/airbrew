import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Download, File as FileIcon, Loader2, Lock } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { isApiError, type ApiError } from "@/lib/api";
import { drive, type DriveShareMeta } from "@/lib/drive";

export default function DriveSharePage() {
  const { t } = useTranslation();
  const { token = "" } = useParams();
  const [meta, setMeta] = useState<DriveShareMeta | null>(null);
  const [loading, setLoading] = useState(true);
  const [needsPw, setNeedsPw] = useState(false);
  const [pwWrong, setPwWrong] = useState(false);
  const [pw, setPw] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [downloading, setDownloading] = useState(false);

  const load = (password?: string) => {
    setLoading(true);
    setError(null);
    drive
      .shareMeta(token, password)
      .then((m) => {
        setMeta(m);
        setNeedsPw(false);
        setPwWrong(false);
      })
      .catch((e) => {
        if (isApiError(e) && (e as ApiError).error === "password_required") {
          setNeedsPw(true);
          // Distinguish a fresh prompt from a rejected attempt: if we sent a
          // password, this is a wrong-password; otherwise it's the first view.
          setPwWrong(!!password);
          setMeta(null);
        } else if (isApiError(e) && (e as ApiError).error === "expired") {
          setError(t("share.expired"));
        } else {
          setError(t("share.notFound"));
        }
      })
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  const doDownload = async () => {
    setDownloading(true);
    try {
      const url = await drive.shareDownload(token, pw || undefined);
      const a = document.createElement("a");
      a.href = url;
      a.download = meta?.name ?? "download";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch {
      setError(t("share.downloadFailed"));
    } finally {
      setDownloading(false);
    }
  };

  if (loading)
    return (
      <Shell>
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </Shell>
    );
  if (error)
    return (
      <Shell>
        <p className="text-sm text-muted-foreground">{error}</p>
      </Shell>
    );

  if (needsPw)
    return (
      <Shell>
        <Card className="w-full max-w-sm space-y-4 p-6">
          <div className="flex items-center gap-2 text-muted-foreground">
            <Lock className="h-5 w-5" />
            <span className="text-sm font-medium">
              {t("share.passwordRequired")}
            </span>
          </div>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              load(pw);
            }}
            className="space-y-3"
          >
            {pwWrong && (
              <p className="text-sm text-destructive">
                {t("share.wrongPassword")}
              </p>
            )}
            <Input
              type="password"
              autoFocus
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              placeholder={t("share.enterPassword")}
            />
            <Button type="submit" className="w-full">
              {t("share.continue")}
            </Button>
          </form>
        </Card>
      </Shell>
    );

  if (!meta) return null;
  return (
    <Shell>
      <Card className="w-full max-w-sm space-y-4 p-6 text-center">
        <FileIcon className="mx-auto h-12 w-12 text-muted-foreground" />
        <div>
          <h1 className="truncate text-lg font-semibold">{meta.name}</h1>
          <p className="text-xs text-muted-foreground">
            {meta.content_type || t("share.file")} ·{" "}
            {formatBytes(meta.size_bytes)}
          </p>
          {meta.expires_at && (
            <p className="mt-1 text-xs text-muted-foreground">
              {t("share.expiresOn", {
                date: new Date(meta.expires_at).toLocaleString(),
              })}
            </p>
          )}
        </div>
        <Button className="w-full" onClick={doDownload} disabled={downloading}>
          {downloading ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Download className="h-4 w-4" />
          )}
          {t("share.download")}
        </Button>
      </Card>
    </Shell>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-6">
      {children}
    </div>
  );
}

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(
    units.length - 1,
    Math.floor(Math.log(n) / Math.log(1024)),
  );
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
