import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  FileEdit,
  Forward,
  Inbox,
  Loader2,
  MailPlus,
  Paperclip,
  RefreshCcw,
  Reply,
  RotateCw,
  Send,
  Star,
  Trash2,
  Upload,
  X,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader, PageWrapper } from "@/components/page";
import { isApiError } from "@/lib/api";
import {
  formatAddress,
  mail,
  parseAddressList,
  type FolderCounts,
  type MailAttachment,
  type Mailbox,
  type MailMessage,
} from "@/lib/mail";
import { sanitizeMailHtml } from "@/lib/sanitize-mail-html";
import { cn } from "@/lib/utils";

const folders = [
  { id: "inbox", label: "받은편지함", icon: Inbox },
  { id: "sent", label: "보낸메일", icon: Send },
  { id: "draft", label: "임시보관", icon: FileEdit },
  { id: "outbox", label: "전송 실패", icon: AlertTriangle },
  { id: "unread", label: "읽지 않음", icon: Inbox },
  { id: "starred", label: "중요", icon: Star },
] as const;

type ComposeState = {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  text: string;
  inReplyTo: string;
  references: string[];
  attachments: MailAttachment[];
  draftId: string;
};

const emptyCompose: ComposeState = {
  to: "",
  cc: "",
  bcc: "",
  subject: "",
  text: "",
  inReplyTo: "",
  references: [],
  attachments: [],
  draftId: "",
};

export default function MailPage() {
  const [params, setParams] = useSearchParams();
  const folder = normalizeFolder(params.get("box"));
  const messageID = params.get("message") ?? "";
  const mailboxID = params.get("mailbox") ?? "";
  const searchQuery = params.get("q") ?? "";

  const [status, setStatus] = useState<{ enabled: boolean } | null>(null);
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [messages, setMessages] = useState<MailMessage[]>([]);
  const [selected, setSelected] = useState<MailMessage | null>(null);
  const [counts, setCounts] = useState<FolderCounts | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [composeOpen, setComposeOpen] = useState(false);
  const [compose, setCompose] = useState<ComposeState>(emptyCompose);
  const [mailboxOpen, setMailboxOpen] = useState(false);
  const [newAddress, setNewAddress] = useState("");
  const [newDisplayName, setNewDisplayName] = useState("");
  const [searchInput, setSearchInput] = useState(searchQuery);
  const fileInput = useRef<HTMLInputElement>(null);

  const activeMailbox =
    mailboxes.find((mb) => mb.id === mailboxID) ?? mailboxes[0];

  useEffect(() => {
    mail
      .status()
      .then((s) => setStatus({ enabled: Boolean(s.enabled) }))
      .catch(() => {});
  }, []);

  const refreshMailboxes = useCallback(async () => {
    const res = await mail.mailboxes();
    setMailboxes(res.mailboxes ?? []);
  }, []);

  const refreshCounts = useCallback(async () => {
    try {
      const c = await mail.counts(activeMailbox?.id);
      setCounts(c);
    } catch {
      setCounts(null);
    }
  }, [activeMailbox?.id]);

  const refreshMessages = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await mail.messages({
        folder,
        mailbox: activeMailbox?.id,
        q: searchQuery || undefined,
        limit: 100,
      });
      setMessages(res.messages ?? []);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [activeMailbox?.id, folder, searchQuery]);

  useEffect(() => {
    void refreshMailboxes().catch((e) => setError(errorText(e)));
  }, [refreshMailboxes]);

  useEffect(() => {
    void refreshMessages();
  }, [refreshMessages]);

  useEffect(() => {
    void refreshCounts();
  }, [refreshCounts, refreshMessages]);

  useEffect(() => {
    setSearchInput(searchQuery);
  }, [searchQuery]);

  useEffect(() => {
    if (!messageID) {
      setSelected(null);
      return;
    }
    let alive = true;
    mail
      .message(messageID)
      .then((m) => {
        if (alive) setSelected(m);
      })
      .catch((e) => {
        if (alive) setError(errorText(e));
      });
    return () => {
      alive = false;
    };
  }, [messageID]);

  const folderBadge = (id: string): number => {
    if (!counts) return 0;
    switch (id) {
      case "inbox":
        return counts.inbox;
      case "sent":
        return counts.sent;
      case "draft":
        return counts.draft;
      case "outbox":
        return counts.outbox;
      case "unread":
        return counts.unread;
      case "starred":
        return counts.starred;
      default:
        return 0;
    }
  };

  const submitSearch = (value: string) => {
    const next = new URLSearchParams(params);
    if (value.trim()) next.set("q", value.trim());
    else next.delete("q");
    next.delete("message");
    setParams(next, { replace: false });
  };

  const openMessage = async (m: MailMessage) => {
    const next = new URLSearchParams(params);
    next.set("message", m.id);
    setParams(next, { replace: false });
    setSelected(m);
    if (!m.is_read && m.direction === "inbound") {
      await mail.patchMessage(m.id, { is_read: true }).catch(() => {});
      setMessages((prev) =>
        prev.map((item) =>
          item.id === m.id ? { ...item, is_read: true } : item,
        ),
      );
      void refreshCounts();
    }
  };

  const selectFolder = (nextFolder: string) => {
    const next = new URLSearchParams(params);
    if (nextFolder === "inbox") next.delete("box");
    else next.set("box", nextFolder);
    next.delete("message");
    next.delete("q");
    setParams(next, { replace: false });
  };

  const selectMailbox = (id: string) => {
    const next = new URLSearchParams(params);
    if (id) next.set("mailbox", id);
    else next.delete("mailbox");
    next.delete("message");
    setParams(next, { replace: false });
  };

  const toggleStar = async (m: MailMessage) => {
    await mail.patchMessage(m.id, { is_starred: !m.is_starred });
    setMessages((prev) =>
      prev.map((item) =>
        item.id === m.id ? { ...item, is_starred: !item.is_starred } : item,
      ),
    );
    if (selected?.id === m.id) {
      setSelected({ ...selected, is_starred: !selected.is_starred });
    }
    void refreshCounts();
  };

  const removeMessage = async (m: MailMessage) => {
    if (!confirm("이 메일을 삭제할까요?")) return;
    await mail.deleteMessage(m.id);
    setMessages((prev) => prev.filter((item) => item.id !== m.id));
    if (selected?.id === m.id) setSelected(null);
    void refreshCounts();
  };

  const replyTo = (m: MailMessage) => {
    const to =
      m.direction === "inbound"
        ? formatAddress(m.from)
        : m.to.map(formatAddress).join(", ");
    setCompose({
      ...emptyCompose,
      to,
      subject: m.subject.toLowerCase().startsWith("re:")
        ? m.subject
        : `Re: ${m.subject}`,
      text: `\n\nOn ${formatDate(m.created_at)}, ${formatAddress(m.from)} wrote:\n${quote(m.body_text)}`,
      inReplyTo: m.message_id,
      references: [...(m.references ?? []), m.message_id].filter(Boolean),
    });
    setComposeOpen(true);
  };

  const forwardTo = (m: MailMessage) => {
    const fwd = `\n\n--- 원본 메일 ---\nFrom: ${formatAddress(m.from)}\nSubject: ${m.subject}\n\n${m.body_text}`;
    setCompose({
      ...emptyCompose,
      subject: m.subject.toLowerCase().startsWith("fwd:")
        ? m.subject
        : `Fwd: ${m.subject}`,
      text: fwd,
    });
    setComposeOpen(true);
  };

  const editDraft = async (m: MailMessage) => {
    // The list endpoint omits attachments; if the user clicked edit on a row
    // whose detail has not been fetched yet, fetch it so we do not silently
    // drop the draft's attachments on save.
    let detail = m;
    if (!detail.attachments) {
      try {
        detail = await mail.message(m.id);
      } catch {
        // fall back to the row we already have
      }
    }
    setCompose({
      ...emptyCompose,
      draftId: detail.id,
      to: detail.to.map(formatAddress).join(", "),
      cc: (detail.cc ?? []).map(formatAddress).join(", "),
      bcc: (detail.bcc ?? []).map(formatAddress).join(", "),
      subject: detail.subject,
      text: detail.body_text,
      inReplyTo: detail.in_reply_to,
      references: detail.references ?? [],
      attachments: detail.attachments ?? [],
    });
    setComposeOpen(true);
  };

  const uploadFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const uploaded: MailAttachment[] = [];
      for (const file of Array.from(files)) {
        uploaded.push(await mail.uploadAttachment(file));
      }
      setCompose((prev) => ({
        ...prev,
        attachments: [...prev.attachments, ...uploaded],
      }));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
      if (fileInput.current) fileInput.current.value = "";
    }
  };

  const discardAttachment = async (id: string) => {
    await mail.deleteAttachment(id).catch(() => {});
    setCompose((prev) => ({
      ...prev,
      attachments: prev.attachments.filter((a) => a.id !== id),
    }));
  };

  const composeInput = () => ({
    mailbox_id: activeMailbox?.id ?? "",
    to: parseAddressList(compose.to),
    cc: parseAddressList(compose.cc),
    bcc: parseAddressList(compose.bcc),
    subject: compose.subject,
    text: compose.text,
    in_reply_to: compose.inReplyTo,
    references: compose.references,
    attachment_ids: compose.attachments.map((a) => a.id),
  });

  const sendMessage = async () => {
    if (!activeMailbox) {
      setError("먼저 메일함을 만들어야 합니다.");
      return;
    }
    if (!compose.to.trim()) {
      setError("받는 사람을 입력하세요.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (compose.draftId) {
        await mail.updateDraft(compose.draftId, {
          ...composeInput(),
          send: true,
        });
      } else {
        await mail.send(composeInput());
      }
      setCompose(emptyCompose);
      setComposeOpen(false);
      await Promise.all([refreshMessages(), refreshCounts()]);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const saveDraft = async () => {
    if (!activeMailbox) {
      setError("먼저 메일함을 만들어야 합니다.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const saved = compose.draftId
        ? await mail.updateDraft(compose.draftId, composeInput())
        : await mail.saveDraft(composeInput());
      setCompose((prev) => ({ ...prev, draftId: saved.id }));
      await Promise.all([refreshMessages(), refreshCounts()]);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const discardDraft = async () => {
    if (!compose.draftId) {
      setCompose(emptyCompose);
      setComposeOpen(false);
      return;
    }
    setBusy(true);
    try {
      await mail.deleteDraft(compose.draftId);
      setCompose(emptyCompose);
      setComposeOpen(false);
      await Promise.all([refreshMessages(), refreshCounts()]);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const retryMessage = async (m: MailMessage) => {
    setBusy(true);
    setError(null);
    try {
      const sent = await mail.retryMessage(m.id);
      setMessages((prev) =>
        prev.map((item) => (item.id === m.id ? sent : item)),
      );
      if (selected?.id === m.id) setSelected(sent);
      void refreshCounts();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const createMailbox = async () => {
    setBusy(true);
    setError(null);
    try {
      const mb = await mail.createMailbox({
        address: newAddress,
        display_name: newDisplayName,
        is_primary: mailboxes.length === 0,
      });
      setMailboxes((prev) => [...prev, mb]);
      selectMailbox(mb.id);
      setNewAddress("");
      setNewDisplayName("");
      setMailboxOpen(false);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  if (status && !status.enabled) {
    return (
      <PageWrapper>
        <PageHeader title="메일" />
        <div className="rounded-lg border bg-card p-6 text-sm text-muted-foreground">
          메일 모듈이 비활성화되어 있습니다.
        </div>
      </PageWrapper>
    );
  }

  return (
    <PageWrapper className="h-full">
      <PageHeader
        title="메일"
        description={
          activeMailbox
            ? activeMailbox.address
            : "메일함을 만들면 송수신을 시작할 수 있습니다."
        }
        actions={
          <div className="flex items-center gap-2">
            <form
              className="hidden sm:block"
              onSubmit={(e) => {
                e.preventDefault();
                submitSearch(searchInput);
              }}
            >
              <Input
                placeholder="메일 검색"
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                className="h-9 w-56"
              />
            </form>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void refreshMessages()}
            >
              <RefreshCcw className="mr-2 h-4 w-4" />
              새로고침
            </Button>
            <Button
              size="sm"
              onClick={() => {
                setCompose(emptyCompose);
                setComposeOpen(true);
              }}
            >
              <MailPlus className="mr-2 h-4 w-4" />
              작성
            </Button>
          </div>
        }
      />

      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="grid min-h-[620px] grid-cols-[180px_minmax(260px,340px)_1fr] overflow-hidden rounded-lg border bg-background">
        <aside className="border-r bg-muted/25 p-2">
          <div className="space-y-1">
            {folders.map((f) => {
              const Icon = f.icon;
              const active = folder === f.id;
              const badge = folderBadge(f.id);
              return (
                <button
                  key={f.id}
                  onClick={() => selectFolder(f.id)}
                  className={cn(
                    "flex h-9 w-full items-center gap-2 rounded-md px-2 text-left text-sm",
                    active
                      ? "bg-accent font-medium"
                      : "text-muted-foreground hover:bg-accent/60",
                  )}
                >
                  <Icon className="h-4 w-4" />
                  <span className="min-w-0 flex-1 truncate">{f.label}</span>
                  {badge > 0 &&
                    (f.id === "unread" ||
                      f.id === "draft" ||
                      f.id === "outbox") && (
                      <Badge
                        variant={
                          f.id === "outbox" ? "destructive" : "secondary"
                        }
                      >
                        {badge}
                      </Badge>
                    )}
                </button>
              );
            })}
          </div>
          <div className="mt-5 border-t pt-4">
            <p className="px-2 pb-2 text-xs font-medium text-muted-foreground">
              메일함
            </p>
            {mailboxes.map((mb) => (
              <button
                key={mb.id}
                onClick={() => selectMailbox(mb.id)}
                className={cn(
                  "block w-full truncate rounded px-2 py-1 text-left text-xs",
                  activeMailbox?.id === mb.id
                    ? "bg-accent font-medium"
                    : "text-muted-foreground hover:bg-accent/60",
                )}
              >
                {mb.address}
              </button>
            ))}
            {mailboxes.length === 0 && (
              <p className="px-2 text-xs text-muted-foreground">
                아직 메일함이 없습니다.
              </p>
            )}
            <Button
              className="mt-3 w-full"
              variant="outline"
              size="sm"
              onClick={() => setMailboxOpen(true)}
            >
              <MailPlus className="mr-2 h-4 w-4" />
              메일함 추가
            </Button>
          </div>
        </aside>

        <section className="border-r">
          {searchQuery && (
            <div className="flex items-center justify-between border-b px-3 py-2 text-xs">
              <span className="truncate text-muted-foreground">
                검색: {searchQuery}
              </span>
              <button
                className="text-muted-foreground hover:text-foreground"
                onClick={() => submitSearch("")}
                aria-label="검색 해제"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          )}
          {loading ? (
            <div className="flex h-full items-center justify-center text-muted-foreground">
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              불러오는 중
            </div>
          ) : messages.length === 0 ? (
            <div className="flex h-full items-center justify-center px-6 text-center text-sm text-muted-foreground">
              표시할 메일이 없습니다.
            </div>
          ) : (
            <div className="divide-y">
              {messages.map((m) => (
                <button
                  key={m.id}
                  onClick={() => void openMessage(m)}
                  className={cn(
                    "block w-full px-3 py-3 text-left hover:bg-accent/40",
                    selected?.id === m.id && "bg-accent/60",
                  )}
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={cn(
                        "min-w-0 flex-1 truncate text-sm",
                        !m.is_read &&
                          m.direction === "inbound" &&
                          "font-semibold",
                      )}
                    >
                      {m.direction === "outbound"
                        ? m.to.map(formatAddress).join(", ")
                        : formatAddress(m.from)}
                    </span>
                    {m.is_draft && <Badge variant="outline">임시</Badge>}
                    {m.is_outbox && (
                      <Badge variant="destructive">전송 실패</Badge>
                    )}
                    {m.is_starred && (
                      <Star className="h-3.5 w-3.5 fill-current text-primary" />
                    )}
                  </div>
                  <p className="mt-1 truncate text-sm">
                    {m.subject || "(제목 없음)"}
                  </p>
                  <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                    {m.body_text || stripHTML(m.body_html)}
                  </p>
                  <p className="mt-2 text-[11px] text-muted-foreground">
                    {formatDate(m.received_at || m.sent_at || m.created_at)}
                  </p>
                </button>
              ))}
            </div>
          )}
        </section>

        <main className="min-w-0">
          {selected ? (
            <MessageView
              message={selected}
              onReply={replyTo}
              onForward={forwardTo}
              onEditDraft={editDraft}
              onToggleStar={toggleStar}
              onDelete={removeMessage}
              onRetry={retryMessage}
            />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              메일을 선택하세요.
            </div>
          )}
        </main>
      </div>

      <Modal
        open={composeOpen}
        onClose={() => setComposeOpen(false)}
        title={compose.draftId ? "임시 메일 편집" : "메일 작성"}
        className="max-w-2xl"
      >
        <div className="space-y-3">
          <Input
            placeholder="받는 사람"
            value={compose.to}
            onChange={(e) => setCompose({ ...compose, to: e.target.value })}
          />
          <div className="grid grid-cols-2 gap-2">
            <Input
              placeholder="참조"
              value={compose.cc}
              onChange={(e) => setCompose({ ...compose, cc: e.target.value })}
            />
            <Input
              placeholder="숨은참조"
              value={compose.bcc}
              onChange={(e) => setCompose({ ...compose, bcc: e.target.value })}
            />
          </div>
          <Input
            placeholder="제목"
            value={compose.subject}
            onChange={(e) =>
              setCompose({ ...compose, subject: e.target.value })
            }
          />
          <Textarea
            className="min-h-48"
            placeholder="본문"
            value={compose.text}
            onChange={(e) => setCompose({ ...compose, text: e.target.value })}
          />
          {compose.attachments.length > 0 && (
            <div className="space-y-1 rounded-md border p-2">
              {compose.attachments.map((a) => (
                <div key={a.id} className="flex items-center gap-2 text-xs">
                  <Paperclip className="h-3.5 w-3.5" />
                  <span className="min-w-0 flex-1 truncate">{a.filename}</span>
                  <span className="text-muted-foreground">
                    {formatSize(a.size_bytes)}
                  </span>
                  <button
                    className="rounded p-1 text-muted-foreground hover:bg-accent"
                    onClick={() => void discardAttachment(a.id)}
                    aria-label="Remove attachment"
                  >
                    <X className="h-3.5 w-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-center justify-between gap-2">
            <input
              ref={fileInput}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => void uploadFiles(e.currentTarget.files)}
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => fileInput.current?.click()}
              disabled={busy}
            >
              <Upload className="mr-2 h-4 w-4" />
              첨부
            </Button>
            <div className="flex items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void discardDraft()}
                disabled={busy}
              >
                {compose.draftId ? "취소" : "닫기"}
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void saveDraft()}
                disabled={busy}
              >
                <FileEdit className="mr-2 h-4 w-4" />
                임시 저장
              </Button>
              <Button onClick={() => void sendMessage()} disabled={busy}>
                {busy ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Send className="mr-2 h-4 w-4" />
                )}
                보내기
              </Button>
            </div>
          </div>
        </div>
      </Modal>

      <Modal
        open={mailboxOpen}
        onClose={() => setMailboxOpen(false)}
        title="메일함 추가"
      >
        <div className="space-y-3">
          <Input
            placeholder="name@example.com"
            value={newAddress}
            onChange={(e) => setNewAddress(e.target.value)}
          />
          <Input
            placeholder="표시 이름"
            value={newDisplayName}
            onChange={(e) => setNewDisplayName(e.target.value)}
          />
          <div className="flex justify-end">
            <Button onClick={() => void createMailbox()} disabled={busy}>
              {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              추가
            </Button>
          </div>
        </div>
      </Modal>
    </PageWrapper>
  );
}

function MessageView({
  message,
  onReply,
  onForward,
  onEditDraft,
  onToggleStar,
  onDelete,
  onRetry,
}: {
  message: MailMessage;
  onReply: (m: MailMessage) => void;
  onForward: (m: MailMessage) => void;
  onEditDraft: (m: MailMessage) => void;
  onToggleStar: (m: MailMessage) => void;
  onDelete: (m: MailMessage) => void;
  onRetry: (m: MailMessage) => void;
}) {
  const sanitizedHTML = useMemo(
    () => (message.body_html ? sanitizeMailHtml(message.body_html) : ""),
    [message.body_html],
  );
  const renderedHTML = useMemo(() => {
    if (!message.body_html) return "";
    let html = sanitizeMailHtml(message.body_html);
    // Rewrite cid: references to real attachment download URLs so inline
    // images in transactional mail / newsletters actually render. We run
    // this AFTER sanitization (cid: itself is allowed through) and use only
    // the already-sanitized href=/src= attributes, so no new injection
    // surface opens.
    const atts = message.attachments ?? [];
    if (atts.length) {
      const cidMap = new Map<string, string>();
      for (const a of atts) {
        if (a.content_id) cidMap.set(a.content_id, a.download_url);
      }
      if (cidMap.size) {
        html = html.replace(
          /\bsrc=(["'])cid:([^"']+)\1/gi,
          (match, quote, cid) => {
            const url = cidMap.get(cid);
            return url ? `src=${quote}${url}${quote}` : match;
          },
        );
      }
    }
    return html;
  }, [message.body_html, message.attachments]);

  return (
    <article className="flex h-full min-w-0 flex-col">
      <header className="border-b p-4">
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-lg font-semibold">
              {message.subject || "(제목 없음)"}
            </h2>
            <p className="mt-1 truncate text-sm text-muted-foreground">
              {message.direction === "outbound"
                ? "To " + message.to.map(formatAddress).join(", ")
                : formatAddress(message.from)}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              {formatDate(
                message.received_at || message.sent_at || message.created_at,
              )}
            </p>
          </div>
          <div className="flex shrink-0 gap-1">
            {message.is_outbox ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void onRetry(message)}
                title="다시 전송"
              >
                <RotateCw className="h-4 w-4" />
              </Button>
            ) : message.is_draft ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void onEditDraft(message)}
              >
                <FileEdit className="h-4 w-4" />
              </Button>
            ) : (
              <>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onReply(message)}
                >
                  <Reply className="h-4 w-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onForward(message)}
                >
                  <Forward className="h-4 w-4" />
                </Button>
              </>
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void onToggleStar(message)}
            >
              <Star
                className={cn(
                  "h-4 w-4",
                  message.is_starred && "fill-current text-primary",
                )}
              />
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void onDelete(message)}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        </div>
      </header>
      <div className="flex-1 overflow-auto p-5">
        {renderedHTML ? (
          <iframe
            title="Mail body"
            sandbox="allow-popups allow-popups-to-escape-sandbox"
            referrerPolicy="no-referrer"
            srcDoc={sanitizedHTML}
            className="h-full min-h-[360px] w-full rounded-md border bg-white"
          />
        ) : (
          <pre className="whitespace-pre-wrap font-sans text-sm leading-6">
            {message.body_text}
          </pre>
        )}
        {message.attachments && message.attachments.length > 0 && (
          <div className="mt-6 border-t pt-4">
            <p className="mb-2 text-sm font-medium">첨부 파일</p>
            <div className="flex flex-wrap gap-2">
              {message.attachments.map((a) => (
                <a
                  key={a.id}
                  href={a.download_url}
                  className="inline-flex max-w-64 items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm hover:bg-accent"
                >
                  <Paperclip className="h-4 w-4 shrink-0" />
                  <span className="truncate">{a.filename || "attachment"}</span>
                  <span className="text-xs text-muted-foreground">
                    {formatSize(a.size_bytes)}
                  </span>
                </a>
              ))}
            </div>
          </div>
        )}
      </div>
    </article>
  );
}

function normalizeFolder(box: string | null) {
  if (box === "sent" || box === "starred" || box === "unread" || box === "outbox")
    return box;
  if (box === "drafts" || box === "draft") return "draft";
  return "inbox";
}

function errorText(e: unknown) {
  if (isApiError(e)) return e.error_description ?? e.error;
  if (e instanceof Error) return e.message;
  return "요청을 처리하지 못했습니다.";
}

function formatDate(value?: string) {
  if (!value) return "";
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function formatSize(bytes: number) {
  if (!bytes) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function stripHTML(html: string) {
  return html
    .replace(/<[^>]*>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function quote(text: string) {
  return text
    .split("\n")
    .map((line) => `> ${line}`)
    .join("\n");
}
