// AI module API client. Wraps fetch with typed shapes for conversations,
// agents, and the SSE chat stream.

import { api, isApiError, type ApiError } from "@/lib/api";

export type AIAgent = {
  id: string;
  name: string;
  description: string;
  model: string;
  tools: string[];
  is_builtin: boolean;
  is_active: boolean;
};

export type AdminAIAgent = AIAgent & {
  system_prompt: string;
  temperature: number;
  max_tokens: number;
  max_turns: number;
};

export type Conversation = {
  id: string;
  agent_id: string;
  title: string;
  model: string;
  revision: number;
  created_at: string;
  updated_at: string;
};

export type ConversationDetail = Conversation & {
  system: string;
  tools: string[];
  temperature: number;
  max_turns: number;
  messages: Message[];
};

export type ToolCall = {
  id: string;
  name: string;
  args: string;
};

export type Message = {
  id: string;
  role: "system" | "user" | "assistant" | "tool";
  content: string;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  tool_name?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  seq: number;
  created_at: string;
};

export type Driver = {
  name: string;
  needs_api_key: boolean;
  default_model: string;
};

export type AdminAITool = {
  key: string;
  name: string;
  description: string;
  parameters: Record<string, unknown>;
};

export type AdminAIUsageDay = {
  user_id?: string;
  day: string;
  prompt_tokens: number;
  completion_tokens: number;
  request_count: number;
  total_tokens: number;
};

export type AdminAIUsage = {
  days: number;
  usage: AdminAIUsageDay[];
  totals: AdminAIUsageDay;
};

export type AdminAIRun = {
  conversation_id: string;
  run_id: string;
  user_id: string;
  title: string;
  expires_at: string;
  created_at: string;
  expired: boolean;
};

export type AIProvider = {
  id: string;
  direction: "chat" | "embed";
  driver: string;
  name: string;
  base_url: string;
  model_hint: string;
  is_active: boolean;
  has_api_key: boolean;
  created_at: string;
  updated_at: string;
};

// ---- User API ----

export async function listAgents(): Promise<AIAgent[]> {
  const res = await api.get<{ agents: AIAgent[] }>("/api/ai/agents");
  return res.agents;
}

export async function listConversations(): Promise<Conversation[]> {
  const res = await api.get<{ conversations: Conversation[] }>(
    "/api/ai/conversations",
  );
  return res.conversations;
}

export async function getConversation(id: string): Promise<ConversationDetail> {
  return api.get<ConversationDetail>(`/api/ai/conversations/${id}`);
}

export async function createConversation(
  agentId: string,
  title?: string,
): Promise<Conversation> {
  return api.post<Conversation>("/api/ai/conversations", {
    agent_id: agentId,
    title: title ?? "",
  });
}

export async function deleteConversation(id: string): Promise<void> {
  await api.del<{ ok: true }>(`/api/ai/conversations/${id}`);
}

export async function patchConversation(
  id: string,
  patch: { title?: string },
): Promise<Conversation> {
  return api.patch<Conversation>(`/api/ai/conversations/${id}`, patch);
}

// ---- Admin API ----

export async function adminListProviders(): Promise<AIProvider[]> {
  const res = await api.get<{ providers: AIProvider[] }>(
    "/api/admin/ai/providers",
  );
  return res.providers;
}

export async function adminListDrivers(): Promise<Driver[]> {
  const res = await api.get<{ drivers: Driver[] }>("/api/admin/ai/drivers");
  return res.drivers;
}

export async function adminListTools(): Promise<AdminAITool[]> {
  const res = await api.get<{ tools: AdminAITool[] }>("/api/admin/ai/tools");
  return res.tools;
}

export async function adminGetUsage(opts?: {
  days?: number;
  user?: string;
}): Promise<AdminAIUsage> {
  const params = new URLSearchParams();
  if (opts?.days) params.set("days", String(opts.days));
  if (opts?.user) params.set("user", opts.user);
  const qs = params.toString();
  return api.get<AdminAIUsage>(`/api/admin/ai/usage${qs ? `?${qs}` : ""}`);
}

export async function adminListRuns(opts?: {
  includeExpired?: boolean;
}): Promise<AdminAIRun[]> {
  const params = new URLSearchParams();
  if (opts?.includeExpired) params.set("include_expired", "1");
  const qs = params.toString();
  const res = await api.get<{ runs: AdminAIRun[] }>(
    `/api/admin/ai/runs${qs ? `?${qs}` : ""}`,
  );
  return res.runs;
}

export async function adminDeleteRun(
  conversationId: string,
  runId: string,
): Promise<void> {
  const params = new URLSearchParams({ run_id: runId });
  await api.del<{ ok: true }>(
    `/api/admin/ai/runs/${conversationId}?${params.toString()}`,
  );
}

export async function adminPutProvider(
  direction: "chat" | "embed",
  body: {
    driver: string;
    name?: string;
    base_url?: string;
    model_hint?: string;
    api_key?: string;
  },
): Promise<AIProvider> {
  return api.putRaw<AIProvider>(`/api/admin/ai/providers/${direction}`, body);
}

export async function adminDeleteProvider(id: string): Promise<void> {
  await api.del<{ ok: true }>(`/api/admin/ai/providers/${id}`);
}

export async function adminListAgents(): Promise<AdminAIAgent[]> {
  const res = await api.get<{ agents: AdminAIAgent[] }>("/api/admin/ai/agents");
  return res.agents;
}

export async function adminCreateAgent(
  body: Omit<AdminAIAgent, "id" | "is_builtin">,
): Promise<AdminAIAgent> {
  return api.post<AdminAIAgent>("/api/admin/ai/agents", body);
}

export async function adminUpdateAgent(
  id: string,
  body: Omit<AdminAIAgent, "id" | "is_builtin">,
): Promise<AdminAIAgent> {
  return api.putRaw<AdminAIAgent>(`/api/admin/ai/agents/${id}`, body);
}

export async function adminDeleteAgent(id: string): Promise<void> {
  await api.del<{ ok: true }>(`/api/admin/ai/agents/${id}`);
}

// ---- SSE streaming chat ----

export type StreamEvent =
  | { kind: "metadata"; content: string }
  | { kind: "delta"; content: string }
  | {
      kind: "tool_start";
      id: string;
      name: string;
      args: string;
    }
  | {
      kind: "tool";
      id: string;
      name: string;
      args: string;
      result: string;
    }
  | {
      kind: "done";
      message_id?: string;
      title?: string;
      usage?: Usage;
    }
  | { kind: "error"; error: { code: string; description: string } };

export type Usage = {
  prompt_tokens: number;
  completion_tokens: number;
  unavailable?: boolean;
};

/**
 * Streams one assistant turn from the SSE endpoint. The callback receives
 * each parsed event; the returned promise resolves when the stream ends
 * (cleanly or with an error event).
 *
 * The signal aborts the upstream request so the user can stop generation.
 */
export async function streamChat(opts: {
  conversationId: string;
  message: string;
  signal?: AbortSignal;
  onEvent: (e: StreamEvent) => void;
}): Promise<void> {
  const { conversationId, message, signal, onEvent } = opts;
  const res = await fetch(`/api/ai/conversations/${conversationId}/stream`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    body: JSON.stringify({ message }),
    credentials: "same-origin",
    signal,
  });

  if (!res.ok) {
    const text = await res.text();
    let body: { error?: string; error_description?: string } = {};
    try {
      body = text ? JSON.parse(text) : {};
    } catch {
      /* ignore */
    }
    const err: ApiError = {
      error: body.error ?? "http_error",
      error_description: body.error_description ?? res.statusText,
      status: res.status,
    };
    throw err;
  }

  if (!res.body) return;
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let sawDone = false;

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    buf = buf.replace(/\r\n/g, "\n");

    // SSE frames are separated by a blank line. Process all complete
    // frames, leaving the trailing partial in buf.
    let sep: number;
    while ((sep = buf.indexOf("\n\n")) !== -1) {
      const frame = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      const ev = parseFrame(frame);
      if (ev) {
        onEvent(ev);
        if (ev.kind === "done") {
          sawDone = true;
        }
        if (ev.kind === "error") {
          const err: ApiError = {
            error: ev.error.code,
            error_description: ev.error.description,
            status: 0,
          };
          throw err;
        }
      }
    }
  }
  if (!sawDone) {
    const err: ApiError = {
      error: "stream_closed",
      error_description: "stream closed before done",
      status: 0,
    };
    throw err;
  }
}

function parseFrame(frame: string): StreamEvent | null {
  let event = "message";
  const dataLines: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith("event:")) {
      event = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trim());
    } else if (line.startsWith(":")) {
      // comment / keep-alive
      continue;
    }
  }
  if (dataLines.length === 0) return null;
  const data = dataLines.join("\n");
  let payload: any = {};
  try {
    payload = JSON.parse(data);
  } catch {
    return null;
  }
  switch (event) {
    case "metadata":
      return { kind: "metadata", content: payload.content ?? "" };
    case "delta":
      return { kind: "delta", content: payload.content ?? "" };
    case "tool_start":
      return {
        kind: "tool_start",
        id: payload.id ?? "",
        name: payload.name ?? "",
        args: payload.args ?? "",
      };
    case "tool":
      return {
        kind: "tool",
        id: payload.id ?? "",
        name: payload.name ?? "",
        args: payload.args ?? "",
        result: payload.result ?? "",
      };
    case "done":
      return {
        kind: "done",
        message_id: payload.message_id,
        title: payload.title,
        usage: payload.usage,
      };
    case "error":
      return {
        kind: "error",
        error: {
          code: payload.code ?? "error",
          description: payload.description ?? "",
        },
      };
    default:
      return null;
  }
}

export { isApiError };
export type { ApiError };
