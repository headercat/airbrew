import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Loader2, Plus, Send, Trash2, Wrench } from "lucide-react";
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
  streamChat,
} from "@/lib/ai";
import { isApiError } from "@/lib/api";

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
        setMessages(d.messages.map((m) => ({ ...m })));
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
      id: `tmp-${Date.now()}`,
      conversation_id: detail.id,
      role: "user",
      content: text,
      seq: (messages.at(-1)?.seq ?? 0) + 1,
      created_at: new Date().toISOString(),
    };
    const assistantId = `tmp-asst-${Date.now()}`;
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
              case "tool":
                cur.toolNotices = [
                  ...(cur.toolNotices ?? []),
                  {
                    id: ev.id,
                    name: ev.name,
                    args: ev.args,
                    result: ev.result,
                    pending: false,
                  },
                ];
                break;
              case "done":
                cur.streaming = false;
                if (ev.message_id) cur.id = ev.message_id;
                if (ev.usage) {
                  cur.prompt_tokens = ev.usage.prompt_tokens;
                  cur.completion_tokens = ev.usage.completion_tokens;
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
    }
  }

  function handleStop() {
    abortRef.current?.abort();
    setBusy(false);
    setMessages((prev) =>
      prev.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
    );
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
          >
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
          <Button onClick={handleNewConversation} size="sm">
            <Plus className="mr-1 h-4 w-4" />
            {t("ai.newConversation")}
          </Button>
        </div>
      </div>

      {error && (
        <p className="text-sm text-destructive">{error}</p>
      )}

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
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => navigate("/ai")}
                >
                  <ArrowLeft className="mr-1 h-4 w-4" />
                  {t("common.back")}
                </Button>
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

function MessageBubble({ message }: { message: ChatMessage }) {
  const { t } = useTranslation();
  const isUser = message.role === "user";
  const isTool = message.role === "tool";

  if (isTool) {
    // Tool result messages are rolled up into the assistant bubble via
    // toolNotices; the raw tool row is hidden to avoid duplication.
    return null;
  }

  return (
    <div className={`flex ${isUser ? "justify-end" : "justify-start"}`}>
      <div
        className={`max-w-[85%] space-y-2 rounded-2xl px-4 py-2 text-sm ${
          isUser
            ? "bg-primary text-primary-foreground"
            : "bg-muted text-foreground"
        }`}
      >
        <div className="whitespace-pre-wrap break-words">
          {message.content}
          {message.streaming && (
            <Loader2 className="ml-1 inline h-3 w-3 animate-spin align-middle" />
          )}
        </div>

        {message.toolNotices && message.toolNotices.length > 0 && (
          <div className="space-y-1 border-t border-border/40 pt-2 text-xs">
            {message.toolNotices.map((tc, i) => (
              <div
                key={`${tc.id}-${i}`}
                className="flex items-start gap-2 text-muted-foreground"
              >
                <Wrench className="mt-0.5 h-3 w-3 shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="font-mono">{tc.name}</div>
                  <pre className="mt-0.5 overflow-x-auto whitespace-pre-wrap break-words text-[10px]">
                    {tc.result}
                  </pre>
                </div>
              </div>
            ))}
          </div>
        )}

        {!isUser && (message.prompt_tokens || message.completion_tokens) ? (
          <div className="text-right text-[10px] opacity-60">
            {t("ai.tokens", {
              prompt: message.prompt_tokens ?? 0,
              completion: message.completion_tokens ?? 0,
            })}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function fmtErr(err: unknown): string {
  if (isApiError(err)) return err.error_description ?? err.error;
  if (err instanceof Error) return err.message;
  return String(err);
}
