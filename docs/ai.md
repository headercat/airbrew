# AI Agents Module

Tool-using LLM agents with streaming responses. The server brokers requests
to one of several administrator-configured providers (OpenAI-compatible,
Anthropic, Ollama), persists conversations per user, and exposes a
Server-Sent-Events chat surface that the SPA streams token-by-token.

## Status

| Phase | Scope                                                          |
| ----- | -------------------------------------------------------------- |
| 1     | Provider abstraction, chat, streaming, persistence             |
| 2     | Tool calling, agent definitions, system prompts                |
| 3     | Per-agent tool selection, token accounting, audit hardening    |

## Design constraints

- **Standard library first**. The only added dependency is the existing
  `argon2` / `modernc.org/sqlite` set. LLM calls use `net/http` directly so
  we do not pull in vendor SDKs.
- **No secrets in the SPA**. Provider API keys live in the encrypted
  `ai_provider_configs` row; only admin users can write them.
- **Module-gated**. Like passwords/mail, the module can be turned off in
  `server_settings`; the gating middleware short-circuits every endpoint.
- **Streaming first**. The chat endpoint always streams SSE; the SPA is the
  only client we ship, and partial output is the whole point.
- **Tool calling is server-side**. Tool dispatch lives in the agent runtime;
  the browser only sees tool-call annotations in the SSE stream, never the
  raw tool schema.

## Layout

```
internal/ai/
  ai.go                 Module wiring (status endpoint, mux mounting)
  agent/                Agent runtime: prompt assembly, tool dispatch loop
    agent.go            Agent struct, Run(ctx, ...) loop
    registry.go         Catalog of built-in + admin-defined agents
    tools.go            Tool interface, registry, builtin tools
  conv/                 Conversation + message persistence
    conv.go             Domain types (Conversation, Message, ToolCall)
    repository.go       SQL persistence (delta sync, ownership-scoped)
    service.go          Business logic, validation
  provider/             LLM provider abstraction
    provider.go         LLMClient interface, Message/Role/Tool types
    registry.go         Driver registry, active-provider resolution
    openai.go           OpenAI-compatible (OpenAI, Groq, OpenRouter, vLLM, ...)
    anthropic.go        Anthropic Messages API
    ollama.go           Ollama local runtime
  handler/              HTTP layer
    handler.go          Conversation CRUD + SSE chat
    admin.go            Provider config + agent admin endpoints
    sse.go              SSE writer helpers (event/done/error frames)
```

## Database schema

See `internal/db/migrations/0009_ai.sql`. Highlights:

- `ai_provider_configs` — one row per (direction=`chat|embed`) provider.
  API keys are stored encrypted with the session HMAC.
- `ai_agents` — admin-defined agent: name, model, system prompt, allowed
  tool keys, max_turns.
- `ai_conversations` — per-user conversation, pinned to an agent_id.
- `ai_messages` — ordered messages within a conversation; role, content,
  tool_calls JSON, token usage.

A per-user monotonic `revision` (mirroring the vault pattern) drives
multi-tab sync.

## Provider abstraction

```go
type Message struct {
    Role       string        // system | user | assistant | tool
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string        // set when Role == "tool"
}

type LLMClient interface {
    // ChatStream sends the turn and emits deltas on the returned channel.
    // Cancellation via ctx aborts the upstream HTTP request.
    ChatStream(ctx context.Context, req Request) (<-chan Delta, error)
}

type Request struct {
    Model    string
    Messages []Message
    Tools    []ToolSchema
    Temperature float32
    MaxTokens   int
}
```

Drivers wrap `net/http`. SSE parsing for OpenAI/Ollama and NDJSON for
Anthropic is local to each driver.

## Agent runtime

The runtime loop:

1. Build `[]Message` from the conversation history plus the in-flight user
   turn.
2. Inject the agent's system prompt at index 0.
3. Call `LLMClient.ChatStream`.
4. Forward text deltas to the SSE channel.
5. If the assistant returns `tool_calls`, dispatch each one, append the
   tool message, and loop again (subject to `max_turns`).
6. On the final turn (no tool calls), persist the assistant message and
   close the stream.

Tool dispatch is bounded by `context.WithTimeout` per call. A tool may
return `(string, error)`; an error is recorded as a tool message so the
model can recover rather than failing the whole turn.

## Built-in tools

Initial set:

| Tool        | Purpose                                            |
| ----------- | ------------------------------------------------- |
| `clock`     | Reports the current server time. Safe, no PII.    |
| `echo`      | Test tool returning its input; used in CI.        |
| `vault.search` | Searches the user's vault by name (decrypts    |
|             | client-side via a sandboxed vault key the agent   |
|             | holds in memory for the duration of one turn).    |

`vault.search` is the only tool that touches another module; it is gated
behind a feature flag (`ai.tools.vault`) and disabled by default. Tools
that ship disabled are still listed so admins can opt in.

## HTTP surface

### User (session required)

| Method | Path                                          | Purpose                       |
| ------ | -------------------------------------------- | ----------------------------- |
| GET    | `/api/ai/status`                             | Public module status           |
| GET    | `/api/ai/conversations`                      | List conversations             |
| POST   | `/api/ai/conversations`                      | Create conversation            |
| GET    | `/api/ai/conversations/{id}`                 | Get one + messages             |
| DELETE | `/api/ai/conversations/{id}`                 | Soft-delete                    |
| POST   | `/api/ai/conversations/{id}/messages`        | Non-streaming fallback send    |
| POST   | `/api/ai/conversations/{id}/stream`          | SSE streaming send             |
| GET    | `/api/ai/agents`                             | Available agents (no secrets)  |

### Admin (session + RequireAdmin)

| Method | Path                                                | Purpose                   |
| ------ | --------------------------------------------------- | ------------------------- |
| GET    | `/api/admin/ai/providers`                           | List providers             |
| PUT    | `/api/admin/ai/providers/{direction}`               | Upsert + activate         |
| DELETE | `/api/admin/ai/providers/{id}`                      | Remove provider            |
| GET    | `/api/admin/ai/agents`                              | List agent defs            |
| POST   | `/api/admin/ai/agents`                              | Create agent def           |
| PUT    | `/api/admin/ai/agents/{id}`                         | Update agent def           |
| DELETE | `/api/admin/ai/agents/{id}`                         | Remove agent def           |

## SSE protocol

```
event: delta
data: {"content":"Hello"}

event: tool
data: {"id":"...","name":"clock","args":{},"result":"..."}

event: done
data: {"message_id":"...","usage":{"prompt":120,"completion":8}}
```

Errors mid-stream use:

```
event: error
data: {"error":"provider_timeout","description":"..."}
```

## Token accounting

Each provider response carries a usage block. We persist `prompt_tokens`
and `completion_tokens` on the assistant row and accumulate a per-user
`ai_usage_daily` rollup (one row per user per UTC day) so the admin
panel can show burn-down charts without scanning the messages table.

## Security

- API keys are encrypted at rest with AES-256-GCM keyed by the session
  HMAC secret. The cipher key is derived once at startup.
- Provider egress is restricted by a per-request timeout (default 120s)
  enforced in the runtime, on top of the user's request ctx.
- All inputs are length-capped (system prompt 8 KiB, message 32 KiB,
  conversation history 50 messages) to bound DB row size and prompt cost.
- Audit events: `ai.conversation_created`, `ai.message_sent`,
  `ai.agent_created`, `ai.provider_upserted`, `ai.provider_deleted`.
- The module is `RequireEnabled`-gated like passwords/mail.
- A per-IP rate limiter caps chat requests (default 30/min).
