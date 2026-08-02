import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Plus, Send, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { PageWrapper } from "@/components/page";
import {
  type AIAgent,
  type Conversation,
  type ConversationDetail,
  type Message,
  type StreamEvent,
  createConversation,
  deleteConversation,
  getConversation,
  listAgents,
  listConversations,
  patchConversation,
  streamChat,
} from "@/lib/ai";
import { isApiError } from "@/lib/api";
import { MessageBubble } from "./message-bubble";

type ToolNotice = {
  id: string;
  name: string;
  args: string;
  result: string;
  pending: boolean;
};

type ChatMessage = Message & {
  conversation_id?: string;
  // Streaming-only state: in-flight tool calls / pending assistant text
  streaming?: boolean;
  toolNotices?: ToolNotice[];
};

export type { ChatMessage, ToolNotice };

export default function AIPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { id: activeId } = useParams<{ id: string }>();

  const [agents, setAgents] = useState<AIAgent[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [detail, setDetail] = useState<ConversationDetail | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [newAgentId, setNewAgentId] = useState<string>("");

  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  const refreshConversations = useCallback(async () => {
    try {
      const list = await listConversations();
      setConversations(list);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    listAgents()
      .then((a) => {
        setAgents(a);
        if (a.length > 0) setNewAgentId(a[0].id);
      })
      .catch((err) => setError(fmtErr(err)));
    refreshConversations();
  }, [refreshConversations]);

  // Load active conversation detail when route changes.
  useEffect(() => {
    if (!activeId) {
      setDetail(null);
      setMessages([]);
      return;
    }
    let alive = true;
    setError(null);
    getConversation(activeId)
      .then((d) => {
        if (!alive) return;
        setDetail(d);
        setMessages(normalizeMessages(d.messages));
      })
      .catch((err) => {
        if (alive) setError(fmtErr(err));
      });
    return () => {
      alive = false;
    };
  }, [activeId]);

  // Auto-scroll to bottom on new messages.
  useEffect(() => {
    const node = scrollRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [messages]);

  async function handleNewConversation() {
    if (!newAgentId) return;
    try {
      const conv = await createConversation(newAgentId, "");
      setConversations((prev) => [conv, ...prev]);
      navigate(`/ai/${conv.id}`);
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  async function handleDelete(id: string) {
    if (!confirm(t("ai.confirmDelete"))) return;
    try {
      await deleteConversation(id);
      setConversations((prev) => prev.filter((c) => c.id !== id));
      if (id === activeId) navigate("/ai");
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  async function handleSend() {
    if (!detail || !draft.trim() || busy) return;
    const text = draft.trim();
    setDraft("");
    setError(null);
    setBusy(true);

    // Optimistic user message.
    const userMsg: ChatMessage = {
      id: `tmp-${Math.random().toString(36).slice(2)}`,
      conversation_id: detail.id,
      role: "user",
      content: text,
      seq: (messages.at(-1)?.seq ?? 0) + 1,
      created_at: new Date().toISOString(),
    };
    const assistantId = `tmp-asst-${Math.random().toString(36).slice(2)}`;
    const assistantMsg: ChatMessage = {
      id: assistantId,
      conversation_id: detail.id,
      role: "assistant",
      content: "",
      seq: userMsg.seq + 1,
      created_at: new Date().toISOString(),
      streaming: true,
      toolNotices: [],
    };
    setMessages((prev) => [...prev, userMsg, assistantMsg]);

    const ctrl = new AbortController();
    abortRef.current = ctrl;
    let streamEnded = false;

    try {
      await streamChat({
        conversationId: detail.id,
        message: text,
        signal: ctrl.signal,
        onEvent: (ev: StreamEvent) => {
          setMessages((prev) => {
            const next = [...prev];
            const idx = next.findIndex((m) => m.id === assistantId);
            if (idx === -1) return prev;
            const cur = { ...next[idx] };
            switch (ev.kind) {
              case "delta":
                cur.content += ev.content;
                break;
              case "tool_start":
                cur.toolNotices = [
                  ...(cur.toolNotices ?? []),
                  {
                    id: ev.id,
                    name: ev.name,
                    args: ev.args,
                    result: "",
                    pending: true,
                  },
                ];
                break;
              case "tool":
                cur.toolNotices = upsertToolNotice(cur.toolNotices, {
                  id: ev.id,
                  name: ev.name,
                  args: ev.args,
                  result: ev.result,
                  pending: false,
                });
                break;
              case "done":
                cur.streaming = false;
                streamEnded = true;
                if (ev.message_id) cur.id = ev.message_id;
                if (ev.usage) {
                  cur.prompt_tokens = ev.usage.prompt_tokens;
                  cur.completion_tokens = ev.usage.completion_tokens;
                }
                if (ev.title) {
                  setDetail((prev) =>
                    prev ? { ...prev, title: ev.title! } : prev,
                  );
                  setConversations((prev) =>
                    prev.map((c) =>
                      c.id === detail.id ? { ...c, title: ev.title! } : c,
                    ),
                  );
                }
                break;
              case "error":
                cur.streaming = false;
                cur.content +=
                  (cur.content ? "\n\n" : "") +
                  `_${t("ai.streamError", {
                    msg: `${ev.error.code}: ${ev.error.description}`,
                  })}_`;
                break;
              case "metadata":
                // model metadata, ignored in the bubble
                break;
            }
            next[idx] = cur;
            return next;
          });
        },
      });
    } catch (err) {
      if (!ctrl.signal.aborted) {
        setError(fmtErr(err));
        setMessages((prev) =>
          prev.map((m) =>
            m.id === assistantId
              ? {
                  ...m,
                  streaming: false,
                  content:
                    (m.content ? m.content + "\n\n" : "") +
                    `_${t("ai.streamError", { msg: fmtErr(err) })}_`,
                }
              : m,
          ),
        );
      }
    } finally {
      setBusy(false);
      abortRef.current = null;
      refreshConversations();
      // SSE recovery: if the assistant turn never reached "done" (network
      // drop, server restart), re-fetch the conversation so the user
      // sees the persisted server-side state. Partial assistant text is
      // discarded unless the provider completed the turn.
      if (!streamEnded && !ctrl.signal.aborted) {
        try {
          const fresh = await getConversation(detail.id);
          setDetail(fresh);
          setMessages(normalizeMessages(fresh.messages));
        } catch {
          /* leave the optimistic state in place */
        }
      }
    }
  }

  function handleStop() {
    abortRef.current?.abort();
    setBusy(false);
    setMessages((prev) =>
      prev.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
    );
    if (detail) {
      getConversation(detail.id)
        .then((fresh) => {
          setDetail(fresh);
          setMessages(normalizeMessages(fresh.messages));
        })
        .catch(() => {
          /* keep local cancelled state */
        });
    }
  }

  return (
    <PageWrapper className="max-w-6xl">
      <div className="flex items-center justify-between gap-4">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold tracking-tight">
            {t("dashboard.modules.ai.label")}
          </h1>
          <p className="text-sm text-muted-foreground">
            {t("dashboard.modules.ai.description")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <select
            className="h-9 rounded-md border border-border bg-background px-2 text-sm"
            value={newAgentId}
            onChange={(e) => setNewAgentId(e.target.value)}
            disabled={agents.length === 0}
          >
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
          <Button
            onClick={handleNewConversation}
            size="sm"
            disabled={agents.length === 0}
          >
            <Plus className="mr-1 h-4 w-4" />
            {t("ai.newConversation")}
          </Button>
        </div>
      </div>

      {agents.length === 0 && !error && (
        <p className="text-sm text-muted-foreground">{t("ai.noAgents")}</p>
      )}
      {error && <p className="text-sm text-destructive">{error}</p>}

      <div className="grid gap-4 md:grid-cols-[260px_1fr]">
        {/* Conversation list */}
        <Card className="h-[70vh] overflow-y-auto p-2">
          {conversations.length === 0 ? (
            <p className="px-3 py-6 text-center text-xs text-muted-foreground">
              {t("ai.noConversations")}
            </p>
          ) : (
            <ul className="space-y-1">
              {conversations.map((c) => (
                <li key={c.id}>
                  <button
                    onClick={() => navigate(`/ai/${c.id}`)}
                    className={`group flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors ${
                      c.id === activeId
                        ? "bg-accent text-accent-foreground"
                        : "hover:bg-accent/50"
                    }`}
                  >
                    <span className="flex-1 truncate">
                      {c.title || t("ai.untitled")}
                    </span>
                    <span
                      role="button"
                      tabIndex={0}
                      onClick={(e) => {
                        e.stopPropagation();
                        handleDelete(c.id);
                      }}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") {
                          e.stopPropagation();
                          handleDelete(c.id);
                        }
                      }}
                      className="opacity-0 transition-opacity group-hover:opacity-100"
                    >
                      <Trash2 className="h-3.5 w-3.5 text-muted-foreground hover:text-destructive" />
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        {/* Chat pane */}
        <Card className="flex h-[70vh] flex-col">
          {!detail ? (
            <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
              {t("ai.selectConversation")}
            </div>
          ) : (
            <>
              <div className="flex items-center justify-between gap-2 border-b border-border px-4 py-2">
                <div className="flex min-w-0 items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => navigate("/ai")}
                  >
                    <ArrowLeft className="mr-1 h-4 w-4" />
                    {t("common.back")}
                  </Button>
                  <TitleEditor
                    initial={detail.title}
                    onCommit={(title) =>
                      patchConversation(detail.id, { title })
                        .then((c) =>
                          setDetail((prev) =>
                            prev ? { ...prev, title: c.title } : prev,
                          ),
                        )
                        .catch((e) => setError(fmtErr(e)))
                    }
                  />
                </div>
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Badge variant="outline" className="text-[10px]">
                    {detail.model}
                  </Badge>
                  {detail.tools.length > 0 && (
                    <Badge variant="secondary" className="text-[10px]">
                      {detail.tools.length} {t("ai.tools")}
                    </Badge>
                  )}
                </div>
              </div>

              <div
                ref={scrollRef}
                className="flex-1 space-y-4 overflow-y-auto p-4"
                aria-live="polite"
                aria-relevant="additions"
              >
                {messages.map((m) => (
                  <MessageBubble key={m.id} message={m} />
                ))}
              </div>

              <div className="border-t border-border p-3">
                <div className="flex items-end gap-2">
                  <Textarea
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && !e.shiftKey) {
                        e.preventDefault();
                        handleSend();
                      }
                    }}
                    placeholder={t("ai.composerPlaceholder")}
                    rows={2}
                    className="min-h-[44px] flex-1 resize-none"
                    disabled={busy}
                  />
                  {busy ? (
                    <Button variant="secondary" size="sm" onClick={handleStop}>
                      {t("ai.stop")}
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      onClick={handleSend}
                      disabled={!draft.trim()}
                    >
                      <Send className="mr-1 h-4 w-4" />
                      {t("ai.send")}
                    </Button>
                  )}
                </div>
              </div>
            </>
          )}
        </Card>
      </div>
    </PageWrapper>
  );
}

function TitleEditor({
  initial,
  onCommit,
}: {
  initial: string;
  onCommit: (title: string) => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(initial);

  useEffect(() => {
    if (!editing) setValue(initial);
  }, [initial, editing]);

  if (!editing) {
    return (
      <button
        type="button"
        onClick={() => setEditing(true)}
        className="truncate text-sm text-muted-foreground hover:text-foreground"
        title={t("ai.editTitle")}
      >
        {initial || t("ai.untitled")}
      </button>
    );
  }
  return (
    <input
      autoFocus
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onBlur={() => {
        setEditing(false);
        if (value.trim() && value.trim() !== initial) {
          onCommit(value.trim());
        }
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          (e.target as HTMLInputElement).blur();
        } else if (e.key === "Escape") {
          setValue(initial);
          setEditing(false);
        }
      }}
      className="min-w-0 flex-1 rounded-md border border-border bg-background px-2 py-1 text-sm"
    />
  );
}

function fmtErr(err: unknown): string {
  if (isApiError(err)) return err.error_description ?? err.error;
  if (err instanceof Error) return err.message;
  return String(err);
}

function upsertToolNotice(
  notices: ToolNotice[] | undefined,
  next: ToolNotice,
): ToolNotice[] {
  const rows = notices ? [...notices] : [];
  const idx = rows.findIndex((n) => n.id === next.id);
  if (idx === -1) return [...rows, next];
  rows[idx] = next;
  return rows;
}

function normalizeMessages(messages: Message[]): ChatMessage[] {
  const out: ChatMessage[] = [];
  const toolByID = new Map<string, Message>();
  for (const msg of messages) {
    if (msg.role === "tool" && msg.tool_call_id) {
      toolByID.set(msg.tool_call_id, msg);
    }
  }
  for (const msg of messages) {
    if (msg.role === "tool") continue;
    const toolNotices =
      msg.role === "assistant" && msg.tool_calls && msg.tool_calls.length > 0
        ? msg.tool_calls.map((tc) => {
            const tool = toolByID.get(tc.id);
            return {
              id: tc.id,
              name: tc.name,
              args: tc.args,
              result: tool?.content ?? "",
              pending: !tool,
            };
          })
        : undefined;
    out.push({ ...msg, toolNotices });
  }
  return out;
}
