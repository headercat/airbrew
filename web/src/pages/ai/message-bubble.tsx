// AI message bubble. Renders user vs assistant bubbles differently and
// supports markdown rendering for assistant content. Tool-call notices
// and per-message copy/regenerate actions are included.

import { useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy, Loader2, RotateCcw, Wrench } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ChatMessage } from "./index";

export function MessageBubble({
  message,
  onRegenerate,
}: {
  message: ChatMessage;
  onRegenerate?: () => void;
}) {
  const { t } = useTranslation();
  const isUser = message.role === "user";
  const isTool = message.role === "tool";
  if (isTool) return null; // tool results are rolled up into toolNotices

  return (
    <div className={`group flex ${isUser ? "justify-end" : "justify-start"}`}>
      <div
        className={`relative max-w-[85%] space-y-2 rounded-2xl px-4 py-2 text-sm ${
          isUser
            ? "bg-primary text-primary-foreground"
            : "bg-muted text-foreground"
        }`}
      >
        {isUser ? (
          <div className="whitespace-pre-wrap break-words">
            {message.content}
          </div>
        ) : (
          <div className="prose prose-sm dark:prose-invert max-w-none break-words">
            {message.content ? (
              <ReactMarkdown
                remarkPlugins={[remarkGfm]}
                components={{
                  a: ({ href, children }) => {
                    if (!isSafeLink(href)) {
                      return <span>{children}</span>;
                    }
                    return (
                      <a href={href} target="_blank" rel="noreferrer noopener">
                        {children}
                      </a>
                    );
                  },
                }}
              >
                {message.content}
              </ReactMarkdown>
            ) : message.streaming ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : null}
          </div>
        )}

        {message.toolNotices && message.toolNotices.length > 0 && (
          <div className="space-y-1 border-t border-border/40 pt-2 text-xs">
            {message.toolNotices.map((tc, i) => (
              <ToolNoticeView key={`${tc.id}-${i}`} notice={tc} />
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

        {!message.streaming && (
          <BubbleActions message={message} onRegenerate={onRegenerate} />
        )}
      </div>
    </div>
  );
}

function isSafeLink(href: string | undefined): href is string {
  if (!href) return false;
  try {
    const u = new URL(href, window.location.origin);
    return (
      u.protocol === "http:" ||
      u.protocol === "https:" ||
      u.protocol === "mailto:"
    );
  } catch {
    return false;
  }
}

function ToolNoticeView({
  notice,
}: {
  notice: NonNullable<ChatMessage["toolNotices"]>[0];
}) {
  return (
    <div className="flex items-start gap-2 text-muted-foreground">
      {notice.pending ? (
        <Loader2 className="mt-0.5 h-3 w-3 shrink-0 animate-spin" />
      ) : (
        <Wrench className="mt-0.5 h-3 w-3 shrink-0" />
      )}
      <div className="min-w-0 flex-1">
        <div className="font-mono">{notice.name}</div>
        {notice.result && (
          <pre className="mt-0.5 overflow-x-auto whitespace-pre-wrap break-words text-[10px]">
            {notice.result}
          </pre>
        )}
      </div>
    </div>
  );
}

function BubbleActions({
  message,
  onRegenerate,
}: {
  message: ChatMessage;
  onRegenerate?: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(message.content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard not available */
    }
  }

  return (
    <div className="absolute -bottom-7 right-2 flex gap-1 opacity-0 transition-opacity group-hover:opacity-100">
      <button
        type="button"
        onClick={copy}
        aria-label={t("ai.copy")}
        className="rounded p-1 text-muted-foreground hover:bg-background hover:text-foreground"
      >
        {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
      </button>
      {onRegenerate && message.role === "assistant" && (
        <button
          type="button"
          onClick={onRegenerate}
          aria-label={t("ai.regenerate")}
          className="rounded p-1 text-muted-foreground hover:bg-background hover:text-foreground"
        >
          <RotateCcw className="h-3 w-3" />
        </button>
      )}
    </div>
  );
}
