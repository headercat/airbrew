import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Download, File as FileIcon, Loader2, Lock } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { isApiError, type ApiError } from "@/lib/api";
import { drive, type DriveShareMeta } from "@/lib/drive";

export default function DriveSharePage() {
  const { token = "" } = useParams();
  const [meta, setMeta] = useState<DriveShareMeta | null>(null);
  const [loading, setLoading] = useState(true);
  const [needsPw, setNeedsPw] = useState(false);
  const [pw, setPw] = useState("");
  const [error, setError] = useState<string | null>(null);

  const load = (password?: string) => {
    setLoading(true);
    setError(null);
    drive
      .shareMeta(token, password)
      .then((m) => {
        setMeta(m);
        setNeedsPw(false);
      })
      .catch((e) => {
        if (isApiError(e) && (e as ApiError).error === "password_required") {
          setNeedsPw(true);
          setMeta(null);
        } else if (isApiError(e) && (e as ApiError).error === "expired") {
          setError("This share link has expired.");
        } else {
          setError("Share link not found or has been revoked.");
        }
      })
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

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
            <span className="text-sm font-medium">Password required</span>
          </div>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              load(pw);
            }}
            className="space-y-3"
          >
            <Input
              type="password"
              autoFocus
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              placeholder="Enter password"
            />
            <Button type="submit" className="w-full">
              Continue
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
            {meta.content_type || "file"} · {formatBytes(meta.size_bytes)}
          </p>
        </div>
        <a href={drive.shareDownloadURL(token, pw)}>
          <Button className="w-full">
            <Download className="h-4 w-4" />
            Download
          </Button>
        </a>
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
