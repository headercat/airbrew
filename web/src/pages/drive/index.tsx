import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ChevronRight,
  Cloud,
  Download,
  File as FileIcon,
  FileText,
  Folder as FolderIcon,
  Home,
  Image as ImageIcon,
  Link2,
  Loader2,
  Pencil,
  Search,
  Share2,
  Star,
  Trash2,
  Upload,
  Users,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { PageHeader, PageWrapper } from "@/components/page";
import { isApiError, type ApiError } from "@/lib/api";
import { drive, type DriveNode, type DriveShare } from "@/lib/drive";
import { cn } from "@/lib/utils";

type Crumb = { id: string; name: string };

export default function DrivePage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();

  const view = params.get("view") ?? "files";
  const parent = params.get("parent") ?? "";
  const query = params.get("q") ?? "";
  const sort = params.get("sort") ?? "name";
  const order = params.get("order") ?? "asc";

  const [status, setStatus] = useState<{
    enabled: boolean;
    quota: number;
    maxUpload: number;
  } | null>(null);
  const [nodes, setNodes] = useState<DriveNode[]>([]);
  const [total, setTotal] = useState(0);
  const [crumbs, setCrumbs] = useState<Crumb[]>([]);
  const [usage, setUsage] = useState<{ used: number; quota: number } | null>(
    null,
  );
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Modals
  const [newFolderOpen, setNewFolderOpen] = useState(false);
  const [renameNode, setRenameNode] = useState<DriveNode | null>(null);
  const [shareNode, setShareNode] = useState<DriveNode | null>(null);
  const [moveNode, setMoveNode] = useState<DriveNode | null>(null);

  const fileInput = useRef<HTMLInputElement>(null);
  const [dragOver, setDragOver] = useState(false);

  const setParam = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(params);
      if (value) next.set(key, value);
      else next.delete(key);
      setParams(next, { replace: true });
    },
    [params, setParams],
  );

  // Status (public).
  useEffect(() => {
    drive
      .status()
      .then((s) =>
        setStatus({
          enabled: s.enabled,
          quota: s.quota_bytes,
          maxUpload: s.max_upload_bytes,
        }),
      )
      .catch(() => {});
  }, []);

  const listArgs = useCallback(
    (offset: number) => {
      const base = { sort, order, limit: 100, offset };
      if (view === "starred") return { ...base, folder: "starred" };
      if (view === "trash") return { ...base, folder: "trash" };
      if (view === "search") return { ...base, folder: "search", q: query };
      return { ...base, parent };
    },
    [view, parent, query, sort, order],
  );

  const refresh = useCallback(async () => {
    if (view === "shared") return; // owned by <SharedView />
    setLoading(true);
    setError(null);
    try {
      const res = await drive.list(listArgs(0));
      setNodes(res.nodes ?? []);
      setTotal(res.total ?? 0);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, [view, listArgs]);

  const loadMore = useCallback(async () => {
    try {
      const res = await drive.list(listArgs(nodes.length));
      setNodes((prev) => [...prev, ...(res.nodes ?? [])]);
      setTotal(res.total ?? 0);
    } catch (e) {
      setError(errMsg(e));
    }
  }, [listArgs, nodes.length]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Breadcrumbs: rebuild when parent changes, in a single round trip.
  useEffect(() => {
    let alive = true;
    (async () => {
      if (!parent) {
        setCrumbs([]);
        return;
      }
      try {
        const res = await drive.path(parent);
        if (!alive) return;
        setCrumbs((res.nodes ?? []).map((n) => ({ id: n.id, name: n.name })));
      } catch {
        if (alive) setCrumbs([]);
      }
    })();
    return () => {
      alive = false;
    };
  }, [parent]);

  const refreshUsage = useCallback(async () => {
    try {
      setUsage(await drive.usage());
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    void refreshUsage();
  }, [refreshUsage]);

  // --- actions ---

  const openFolder = (n: DriveNode) => {
    const next = new URLSearchParams(params);
    next.set("view", "files");
    next.set("parent", n.id);
    next.delete("q");
    setParams(next, { replace: false });
  };

  const onUploadFiles = useCallback(
    async (files: FileList | File[]) => {
      if (!files || (files as FileList).length === 0) return;
      setBusy(true);
      setError(null);
      try {
        for (const f of Array.from(files)) {
          await drive.upload(f, parent);
        }
        await Promise.all([refresh(), refreshUsage()]);
      } catch (e) {
        setError(errMsg(e));
      } finally {
        setBusy(false);
        if (fileInput.current) fileInput.current.value = "";
      }
    },
    [parent, refresh, refreshUsage],
  );

  const toggleStar = async (n: DriveNode) => {
    try {
      await drive.patch(n.id, { starred: !n.is_starred });
      await refresh();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const remove = async (n: DriveNode, permanent = false) => {
    const verb = permanent ? t("drive.confirmDelete") : t("drive.confirmTrash");
    if (!confirm(`${verb}\n${n.name}`)) return;
    try {
      await drive.remove(n.id, permanent);
      await Promise.all([refresh(), refreshUsage()]);
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const restore = async (n: DriveNode) => {
    try {
      await drive.restore(n.id);
      await refresh();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const emptyTrash = async () => {
    if (!confirm(t("drive.confirmEmptyTrash"))) return;
    try {
      await drive.emptyTrash();
      await refreshUsage();
      await refresh();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const download = async (n: DriveNode) => {
    try {
      const url = await drive.downloadBlob(n.id);
      const a = document.createElement("a");
      a.href = url;
      a.download = n.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) {
      setError(errMsg(e));
    }
  };

  if (status && !status.enabled) {
    return (
      <PageWrapper>
        <PageHeader title={t("dashboard.modules.drive.label")} />
        <Card className="p-6 text-sm text-muted-foreground">
          {t("drive.disabled")}
        </Card>
      </PageWrapper>
    );
  }

  const titles: Record<string, string> = {
    files: t("drive.myFiles"),
    starred: t("drive.starred"),
    trash: t("drive.trash"),
    shared: t("drive.shared"),
    search: t("drive.searchResults"),
  };

  return (
    <PageWrapper className="max-w-6xl">
      <PageHeader
        title={titles[view] ?? t("dashboard.modules.drive.label")}
        description={t("dashboard.modules.drive.description")}
        actions={
          view === "files" && (
            <div className="flex gap-2">
              <input
                ref={fileInput}
                type="file"
                multiple
                className="hidden"
                onChange={(e) =>
                  e.target.files && onUploadFiles(e.target.files)
                }
              />
              <Button
                variant="outline"
                size="sm"
                onClick={() => setNewFolderOpen(true)}
              >
                <FolderIcon className="h-4 w-4" />
                {t("drive.newFolder")}
              </Button>
              <Button size="sm" onClick={() => fileInput.current?.click()}>
                <Upload className="h-4 w-4" />
                {t("drive.upload")}
              </Button>
            </div>
          )
        }
      />

      {/* View tabs + search */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-1">
          {(
            [
              ["files", Home],
              ["starred", Star],
              ["shared", Users],
              ["trash", Trash2],
            ] as const
          ).map(([v, Icon]) => (
            <Button
              key={v}
              variant={view === v ? "default" : "outline"}
              size="sm"
              onClick={() => {
                const next = new URLSearchParams(params);
                next.set("view", v);
                next.delete("parent");
                next.delete("q");
                setParams(next, { replace: false });
              }}
            >
              <Icon className="h-4 w-4" />
              {t(`drive.views.${v}`)}
            </Button>
          ))}
        </div>
        <div className="relative ml-auto">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            className="h-9 w-56 pl-8"
            placeholder={t("drive.searchPlaceholder")}
            value={query}
            onChange={(e) => setParam("q", e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                const next = new URLSearchParams(params);
                next.set("view", query.trim() ? "search" : "files");
                setParams(next, { replace: false });
              }
            }}
          />
          {query && (
            <button
              className="absolute right-2 top-2 text-muted-foreground"
              onClick={() => setParam("q", "")}
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>

      {/* Breadcrumbs */}
      {view === "files" && (
        <div className="flex items-center gap-1 text-sm text-muted-foreground">
          <button
            className="hover:text-foreground"
            onClick={() => {
              const next = new URLSearchParams(params);
              next.set("view", "files");
              next.delete("parent");
              setParams(next, { replace: false });
            }}
          >
            {t("drive.myFiles")}
          </button>
          {crumbs.map((c) => (
            <span key={c.id} className="flex items-center gap-1">
              <ChevronRight className="h-3.5 w-3.5" />
              <button
                className="hover:text-foreground"
                onClick={() => {
                  const next = new URLSearchParams(params);
                  next.set("view", "files");
                  next.set("parent", c.id);
                  setParams(next, { replace: false });
                }}
              >
                {c.name}
              </button>
            </span>
          ))}
        </div>
      )}

      {/* Quota */}
      {usage && usage.quota > 0 && (
        <QuotaBar used={usage.used} quota={usage.quota} t={t} />
      )}

      {error && (
        <p className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}

      {/* Dropzone / list */}
      <Card
        className={cn(
          "overflow-hidden",
          view === "files" &&
            "ring-2 ring-transparent transition data-[drag=true]:ring-primary",
        )}
        onDragOver={(e) => {
          if (view !== "files") return;
          e.preventDefault();
          setDragOver(true);
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          if (view !== "files") return;
          e.preventDefault();
          setDragOver(false);
          if (e.dataTransfer.files.length)
            void onUploadFiles(e.dataTransfer.files);
        }}
        data-drag={dragOver}
      >
        {dragOver && (
          <div className="border-b bg-primary/5 px-4 py-2 text-sm text-primary">
            {t("drive.dropToUpload")}
          </div>
        )}
        {loading ? (
          <div className="flex items-center justify-center gap-2 p-12 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            {t("common.loading")}
          </div>
        ) : view === "shared" ? (
          <SharedView />
        ) : nodes.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 p-12 text-center text-sm text-muted-foreground">
            <Cloud className="h-8 w-8 opacity-50" />
            {view === "trash"
              ? t("drive.emptyTrash")
              : view === "starred"
                ? t("drive.emptyStarred")
                : query
                  ? t("drive.noSearch")
                  : t("drive.empty")}
          </div>
        ) : (
          <NodeTable
            nodes={nodes}
            view={view}
            t={t}
            onOpen={openFolder}
            onDownload={download}
            onStar={toggleStar}
            onRename={setRenameNode}
            onShare={setShareNode}
            onMove={setMoveNode}
            onTrash={(n) => remove(n, false)}
            onDelete={(n) => remove(n, true)}
            onRestore={restore}
            sort={sort}
            order={order}
            onSort={(col) => {
              const next = new URLSearchParams(params);
              // Clicking the active column toggles direction; a new column
              // defaults to ascending.
              if (sort === col) {
                next.set("order", order === "asc" ? "desc" : "asc");
              } else {
                next.set("sort", col);
                next.set("order", "asc");
              }
              setParams(next, { replace: false });
            }}
          />
        )}
      </Card>

      {view !== "shared" && !loading && total > nodes.length && (
        <div className="flex justify-center">
          <Button variant="outline" size="sm" onClick={loadMore}>
            {t("drive.loadMore")} ({nodes.length} / {total})
          </Button>
        </div>
      )}

      {view === "trash" && nodes.length > 0 && (
        <div className="flex justify-end">
          <Button variant="outline" size="sm" onClick={emptyTrash}>
            <Trash2 className="h-4 w-4" />
            {t("drive.emptyTrashBtn")}
          </Button>
        </div>
      )}

      {/* Modals */}
      <NewFolderModal
        open={newFolderOpen}
        onClose={() => setNewFolderOpen(false)}
        parent={parent}
        onDone={async () => {
          setNewFolderOpen(false);
          await refresh();
        }}
        t={t}
      />
      <RenameModal
        node={renameNode}
        onClose={() => setRenameNode(null)}
        onDone={async () => {
          setRenameNode(null);
          await refresh();
        }}
        t={t}
      />
      <MoveModal
        node={moveNode}
        onClose={() => setMoveNode(null)}
        onDone={async () => {
          setMoveNode(null);
          await refresh();
        }}
        t={t}
      />
      <ShareModal node={shareNode} onClose={() => setShareNode(null)} t={t} />

      {busy && (
        <div className="fixed bottom-4 right-4 flex items-center gap-2 rounded-md border bg-background px-3 py-2 text-sm shadow-md">
          <Loader2 className="h-4 w-4 animate-spin" />
          {t("drive.uploading")}
        </div>
      )}
    </PageWrapper>
  );
}

// --- node table -----------------------------------------------------------

function NodeTable(props: {
  nodes: DriveNode[];
  view: string;
  t: (k: string) => string;
  onOpen: (n: DriveNode) => void;
  onDownload: (n: DriveNode) => void;
  onStar: (n: DriveNode) => void;
  onRename: (n: DriveNode) => void;
  onShare: (n: DriveNode) => void;
  onMove: (n: DriveNode) => void;
  onTrash: (n: DriveNode) => void;
  onDelete: (n: DriveNode) => void;
  onRestore: (n: DriveNode) => void;
  sort: string;
  order: string;
  onSort: (col: string) => void;
}) {
  const {
    nodes,
    view,
    t,
    onOpen,
    onDownload,
    onStar,
    onRename,
    onShare,
    onMove,
    onTrash,
    onDelete,
    onRestore,
    sort,
    order,
    onSort,
  } = props;
  const isTrash = view === "trash";
  const cols = [
    { key: "name", label: t("drive.colName"), className: "flex-1" },
    {
      key: "size",
      label: t("drive.colSize"),
      className: "hidden w-24 sm:block",
    },
    {
      key: "updated",
      label: t("drive.colModified"),
      className: "hidden w-32 md:block",
    },
  ];
  return (
    <div className="divide-y divide-border">
      <div className="flex items-center gap-3 border-b bg-muted/30 px-4 py-1.5 text-xs font-medium text-muted-foreground">
        {cols.map((c) => (
          <button
            key={c.key}
            className={cn(c.className, "text-left hover:text-foreground")}
            onClick={() => onSort(c.key)}
          >
            {c.label}
            {sort === c.key && (
              <span className="ml-1">{order === "desc" ? "▼" : "▲"}</span>
            )}
          </button>
        ))}
        <span className="w-40" />
      </div>
      {nodes.map((n) => {
        const Icon = iconFor(n);
        return (
          <div
            key={n.id}
            className="group flex items-center gap-3 px-4 py-2.5 hover:bg-accent/40"
          >
            <button
              className="shrink-0 text-muted-foreground"
              onClick={() => (n.kind === "folder" ? onOpen(n) : onDownload(n))}
            >
              <Icon className="h-5 w-5" />
            </button>
            <button
              className="min-w-0 flex-1 truncate text-left text-sm font-medium"
              onClick={() => (n.kind === "folder" ? onOpen(n) : onDownload(n))}
              title={n.name}
            >
              {n.name}
            </button>
            {n.kind === "file" && (
              <span className="hidden shrink-0 text-xs text-muted-foreground sm:block">
                {formatBytes(n.size_bytes)}
              </span>
            )}
            <span className="hidden shrink-0 text-xs text-muted-foreground md:block">
              {new Date(n.updated_at).toLocaleDateString()}
            </span>
            <div className="flex shrink-0 items-center gap-0.5">
              {!isTrash && (
                <>
                  <IconBtn
                    label={n.is_starred ? t("drive.unstar") : t("drive.star")}
                    onClick={() => onStar(n)}
                  >
                    <Star
                      className={cn(
                        "h-4 w-4",
                        n.is_starred && "fill-yellow-400 text-yellow-400",
                      )}
                    />
                  </IconBtn>
                  {n.kind === "file" && (
                    <IconBtn
                      label={t("drive.download")}
                      onClick={() => onDownload(n)}
                    >
                      <Download className="h-4 w-4" />
                    </IconBtn>
                  )}
                  <IconBtn
                    label={t("drive.rename")}
                    onClick={() => onRename(n)}
                  >
                    <Pencil className="h-4 w-4" />
                  </IconBtn>
                  <IconBtn label={t("drive.move")} onClick={() => onMove(n)}>
                    <FolderIcon className="h-4 w-4" />
                  </IconBtn>
                  {n.kind === "file" && (
                    <IconBtn
                      label={t("drive.share")}
                      onClick={() => onShare(n)}
                    >
                      <Share2 className="h-4 w-4" />
                    </IconBtn>
                  )}
                  <IconBtn
                    label={t("drive.moveToTrash")}
                    onClick={() => onTrash(n)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </IconBtn>
                </>
              )}
              {isTrash && (
                <>
                  <IconBtn
                    label={t("drive.restore")}
                    onClick={() => onRestore(n)}
                  >
                    <Pencil className="h-4 w-4" />
                  </IconBtn>
                  <IconBtn
                    label={t("drive.deleteForever")}
                    onClick={() => onDelete(n)}
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </IconBtn>
                </>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function IconBtn({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      className="rounded-md p-1.5 text-muted-foreground opacity-0 transition hover:bg-accent hover:text-foreground group-hover:opacity-100"
      onClick={onClick}
      title={label}
      aria-label={label}
    >
      {children}
    </button>
  );
}

// --- shared view -----------------------------------------------------------

function SharedView() {
  const { t } = useTranslation();
  const [shares, setShares] = useState<DriveShare[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let alive = true;
    drive
      .listShares()
      .then((r) => alive && setShares(r.shares ?? []))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, []);
  const [copied, setCopied] = useState<string | null>(null);
  if (loading)
    return (
      <div className="flex items-center justify-center gap-2 p-12 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        {t("common.loading")}
      </div>
    );
  if (shares.length === 0)
    return (
      <div className="p-12 text-center text-sm text-muted-foreground">
        {t("drive.noShares")}
      </div>
    );
  return (
    <div className="divide-y divide-border">
      {shares.map((s) => (
        <div
          key={s.id}
          className={cn(
            "flex items-center gap-3 px-4 py-2.5",
            (s.node_trashed || !s.is_active) && "opacity-60",
          )}
        >
          <Link2 className="h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium">
                {s.node_name || t("drive.untitled")}
              </span>
              {s.node_trashed && (
                <Badge variant="destructive" className="text-[10px]">
                  {t("drive.inTrash")}
                </Badge>
              )}
              {!s.is_active && !s.node_trashed && (
                <Badge variant="secondary" className="text-[10px]">
                  {t("drive.disabled")}
                </Badge>
              )}
              {s.has_password && (
                <Badge variant="secondary" className="text-[10px]">
                  PW
                </Badge>
              )}
            </div>
            <div className="flex gap-2 text-xs text-muted-foreground">
              {s.expires_at && (
                <span>
                  {t("drive.expires")}{" "}
                  {new Date(s.expires_at).toLocaleDateString()}
                </span>
              )}
              <span>
                {s.downloads} {t("drive.downloads")}
              </span>
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={s.node_trashed}
            onClick={async () => {
              await navigator.clipboard.writeText(
                window.location.origin + s.url,
              );
              setCopied(s.id);
              setTimeout(() => setCopied(null), 1500);
            }}
          >
            {copied === s.id ? t("drive.copied") : t("drive.copyLink")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={async () => {
              if (!confirm(t("drive.confirmRevoke"))) return;
              await drive.deleteShare(s.id);
              drive.listShares().then((r) => setShares(r.shares ?? []));
            }}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ))}
    </div>
  );
}

// --- quota bar -------------------------------------------------------------

function QuotaBar({
  used,
  quota,
  t,
}: {
  used: number;
  quota: number;
  t: (k: string) => string;
}) {
  const pct = quota > 0 ? Math.min(100, (used / quota) * 100) : 0;
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs text-muted-foreground">
        <span>{t("drive.storage")}</span>
        <span>
          {formatBytes(used)} / {formatBytes(quota)}
        </span>
      </div>
      <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={cn(
            "h-full rounded-full transition-all",
            pct > 90 ? "bg-destructive" : "bg-primary",
          )}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

// --- modals ----------------------------------------------------------------

function NewFolderModal({
  open,
  onClose,
  parent,
  onDone,
  t,
}: {
  open: boolean;
  onClose: () => void;
  parent: string;
  onDone: () => void;
  t: (k: string) => string;
}) {
  const [name, setName] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setName("");
      setErr(null);
    }
  }, [open]);
  const submit = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      await drive.createFolder(name.trim(), parent);
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={t("drive.newFolder")}>
      <Input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && submit()}
        placeholder={t("drive.folderName")}
      />
      {err && <p className="mt-2 text-sm text-destructive">{err}</p>}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t("common.back")}
        </Button>
        <Button onClick={submit} disabled={busy || !name.trim()}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          {t("drive.create")}
        </Button>
      </div>
    </Modal>
  );
}

function RenameModal({
  node,
  onClose,
  onDone,
  t,
}: {
  node: DriveNode | null;
  onClose: () => void;
  onDone: () => void;
  t: (k: string) => string;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (node) {
      setName(node.name);
      setErr(null);
    }
  }, [node]);
  if (!node) return null;
  const submit = async () => {
    if (!name.trim() || name.trim() === node.name) {
      onClose();
      return;
    }
    setBusy(true);
    try {
      await drive.patch(node.id, { name: name.trim() });
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={!!node} onClose={onClose} title={t("drive.rename")}>
      <Input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && submit()}
      />
      {err && <p className="mt-2 text-sm text-destructive">{err}</p>}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t("common.back")}
        </Button>
        <Button onClick={submit} disabled={busy}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          {t("common.save")}
        </Button>
      </div>
    </Modal>
  );
}

function MoveModal({
  node,
  onClose,
  onDone,
  t,
}: {
  node: DriveNode | null;
  onClose: () => void;
  onDone: () => void;
  t: (k: string) => string;
}) {
  const [folders, setFolders] = useState<DriveNode[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (node) {
      setErr(null);
      // List every folder at any depth (parent:"*" = any parent) so the user
      // can move into nested folders, not just top-level ones.
      drive
        .list({ kind: "folder", parent: "*" })
        .then((r) => setFolders(r.nodes ?? []))
        .catch(() => setFolders([]));
    }
  }, [node]);
  if (!node) return null;
  const move = async (target: string) => {
    setBusy(true);
    try {
      await drive.patch(node.id, { parent_id: target });
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const opts = folders.filter(
    (f) => f.id !== node.id && f.id !== node.parent_id,
  );
  return (
    <Modal open={!!node} onClose={onClose} title={t("drive.move")}>
      {err && <p className="mb-2 text-sm text-destructive">{err}</p>}
      <div className="max-h-72 space-y-1 overflow-auto">
        <button
          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent"
          disabled={busy}
          onClick={() => move("")}
        >
          <Home className="h-4 w-4" />
          {t("drive.myFiles")}
        </button>
        {opts.map((f) => (
          <button
            key={f.id}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent"
            disabled={busy}
            onClick={() => move(f.id)}
          >
            <FolderIcon className="h-4 w-4" />
            {f.name}
          </button>
        ))}
        {opts.length === 0 && (
          <p className="px-2 py-4 text-sm text-muted-foreground">
            {t("drive.noFolders")}
          </p>
        )}
      </div>
    </Modal>
  );
}

function ShareModal({
  node,
  onClose,
  t,
}: {
  node: DriveNode | null;
  onClose: () => void;
  t: (k: string) => string;
}) {
  const [shares, setShares] = useState<DriveShare[]>([]);
  const [pw, setPw] = useState("");
  const [expiry, setExpiry] = useState("0");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  useEffect(() => {
    if (node) {
      setPw("");
      setExpiry("0");
      setErr(null);
      drive
        .listNodeShares(node.id)
        .then((r) => setShares(r.shares ?? []))
        .catch(() => setShares([]));
    }
  }, [node]);
  if (!node) return null;
  const create = async () => {
    setBusy(true);
    setErr(null);
    try {
      const secs = parseInt(expiry) || 0;
      await drive.createShare(node.id, {
        password: pw || undefined,
        expires_in_seconds: secs > 0 ? secs : undefined,
      });
      setPw("");
      setExpiry("0");
      const r = await drive.listNodeShares(node.id);
      setShares(r.shares ?? []);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open={!!node}
      onClose={onClose}
      title={t("drive.share")}
      description={node.name}
      className="max-w-lg"
    >
      {err && <p className="mb-2 text-sm text-destructive">{err}</p>}
      <div className="space-y-3">
        <div>
          <label className="mb-1 block text-xs text-muted-foreground">
            {t("drive.sharePassword")}
          </label>
          <Input
            type="password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            placeholder={t("drive.optional")}
          />
        </div>
        <div>
          <label className="mb-1 block text-xs text-muted-foreground">
            {t("drive.shareExpiry")}
          </label>
          <select
            className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm"
            value={expiry}
            onChange={(e) => setExpiry(e.target.value)}
          >
            <option value="0">{t("drive.never")}</option>
            <option value="3600">1h</option>
            <option value="86400">24h</option>
            <option value="604800">7d</option>
            <option value="2592000">30d</option>
          </select>
        </div>
        <Button onClick={create} disabled={busy} className="w-full">
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          {t("drive.createLink")}
        </Button>
      </div>
      {shares.length > 0 && (
        <div className="mt-4 space-y-2 border-t pt-3">
          {shares.map((s) => (
            <div key={s.id} className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-2 py-1 text-xs">
                {window.location.origin + s.url}
              </code>
              <Button
                variant="outline"
                size="sm"
                onClick={async () => {
                  await navigator.clipboard.writeText(
                    window.location.origin + s.url,
                  );
                  setCopied(s.id);
                  setTimeout(() => setCopied(null), 1500);
                }}
              >
                {copied === s.id ? t("drive.copied") : t("drive.copy")}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={async () => {
                  await drive.deleteShare(s.id);
                  const r = await drive.listNodeShares(node.id);
                  setShares(r.shares ?? []);
                }}
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

// --- helpers ---------------------------------------------------------------

function iconFor(n: DriveNode) {
  if (n.kind === "folder") return FolderIcon;
  if (n.content_type.startsWith("image/")) return ImageIcon;
  if (n.content_type.startsWith("text/")) return FileText;
  return FileIcon;
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

function errMsg(e: unknown): string {
  if (isApiError(e))
    return (e as ApiError).error_description ?? (e as ApiError).error;
  return String(e);
}
