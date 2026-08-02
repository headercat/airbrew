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

The agent framework and hardening work for these phases is implemented.
Domain-specific production tools such as vault search or mail drafting
remain future integrations.

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
    ChatStream(ctx context.Context, req Request) <-chan Delta
    Model() string
}

type Request struct {
    Model    string
    Messages []Message
    Tools    []ToolSchema
    Temperature float32
    MaxTokens   int
}
```

Drivers wrap `net/http`. SSE parsing for OpenAI-compatible and Anthropic
providers, plus NDJSON parsing for Ollama, is local to each driver.

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

The runtime enforces the conversation's tool allow-list at dispatch time,
even if a provider emits a tool call for a schema that was not sent in the
request. Tool call IDs are normalised before persistence so replayed
transcripts remain provider-compatible.

## Built-in tools

Initial set:

| Tool        | Purpose                                            |
| ----------- | ------------------------------------------------- |
| `clock`     | Reports the current server time. Safe, no PII.    |
| `echo`      | Test tool returning its input; used in CI.        |

Future module-integration tools (`vault.search`, `mail.draft`) are
planned but not yet wired; the registry is ready to host them.

## HTTP surface

### User (session required)

| Method | Path                                          | Purpose                       |
| ------ | -------------------------------------------- | ----------------------------- |
| GET    | `/api/ai/status`                             | Public module status           |
| GET    | `/api/ai/conversations`                      | List conversations             |
| POST   | `/api/ai/conversations`                      | Create conversation            |
| GET    | `/api/ai/conversations/{id}`                 | Get one + messages             |
| PATCH  | `/api/ai/conversations/{id}`                 | Rename (title)                 |
| DELETE | `/api/ai/conversations/{id}`                 | Soft-delete                    |
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
| GET    | `/api/admin/ai/tools`                               | Registered tool catalog     |
| GET    | `/api/admin/ai/drivers`                             | Driver introspection        |
| GET    | `/api/admin/ai/usage?days=30&user=<id>`             | Daily token rollup          |

## SSE protocol

```
event: delta
data: {"content":"Hello"}

event: tool_start
data: {"id":"...","name":"clock","args":"{}"}

event: tool
data: {"id":"...","name":"clock","args":"{}","result":"..."}

event: done
data: {"message_id":"...","usage":{"prompt_tokens":120,"completion_tokens":8}}
```

Errors mid-stream use:

```
event: error
data: {"code":"provider_upstream","description":"provider returned HTTP 429"}
```

## Token accounting

Each provider response carries a usage block. We persist `prompt_tokens`
and `completion_tokens` on the assistant row and accumulate a per-user
`ai_usage_daily` rollup (one row per user per UTC day) so the admin
panel can show burn-down charts without scanning the messages table.
`GET /api/admin/ai/usage` returns the rollup plus totals.

## History replay

The runtime replays at most the last `MaxHistoryMessages` (50) messages
per turn, then repairs tool-call boundaries before sending history to a
provider. Orphan `tool` rows and incomplete `assistant(tool_calls)` /
`tool` exchanges are dropped from replay so provider transcripts remain
valid. `revision` on `ai_conversations` is bumped on every append so a
multi-tab SPA can poll for changes and resync.

## Run concurrency

Each assistant run takes an in-memory lock and a durable
`ai_run_locks` lease scoped to the conversation. The in-memory lock
prevents overlap inside one process; the database lease prevents overlap
across processes and survives until release or expiry. Stale client
snapshots are rejected with a revision conflict before appending the user
message.

## Title auto-generation

On the first turn of an untitled conversation the stream handler fires
a background `AutoTitle` turn — a low-token, single-shot completion
that summarises the user's first message into a 3-6 word title. The
result is persisted only while the title is still empty and emitted
alongside the `done` event so the SPA updates both the chat header and
the sidebar atomically. Manual rename via
`PATCH /api/ai/conversations/{id}` wins over a late auto-title response.

## SSE recovery

The user message is persisted before the upstream call begins, so a
network drop mid-stream never loses the prompt. Final assistant messages
are persisted only after the provider emits a terminal marker (`[DONE]`,
usage chunk, `message_stop`, or provider-specific equivalent). If a
provider stream ends without that marker, the runtime emits an error and
does not persist the partial assistant text. If the browser connection
breaks before `done`, the SPA re-fetches the conversation and reconciles
to the persisted server state.

## Security

- API keys are encrypted at rest with AES-256-GCM keyed by a key
  derived (SHA-256) from `AIRBREW_SESSION_SECRET`. If the seal cannot
  be initialised at startup the module logs an error and refuses to
  store API keys; there is no plaintext fallback.
- Provider egress is restricted by a per-driver HTTP timeout
  (default 120s chat / 300s Ollama) enforced via a dedicated
  `*http.Client` per build — no shared `http.DefaultClient`.
- Provider stream parsers treat EOF before the provider's terminal marker
  as an error, preventing truncated assistant output from being saved as a
  completed turn.
- All inputs are length-capped (system prompt 8 KiB, message 32 KiB,
  conversation history 50 messages, tools 32) to bound DB row size and
  prompt cost.
- Agent definitions can only reference tool keys currently registered in
  the server tool catalog.
- Errors are sanitised at the HTTP boundary: 500 responses return a
  generic message; the underlying error stays in server logs. Upstream
  provider response bodies are logged via slog and never forwarded to
  the SPA (some providers echo back masked key fragments in error
  bodies).
- Audit events: `ai.conversation_created`, `ai.message_sent`,
  `ai.conversation_deleted`, `ai.agent_created`, `ai.agent_updated`,
  `ai.agent_deleted`, `ai.provider_upserted`, `ai.provider_deleted`.
- The module is `RequireEnabled`-gated like passwords/mail.
- A per-IP rate limiter caps chat requests (default 30/min).
