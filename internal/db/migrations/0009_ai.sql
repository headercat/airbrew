-- 0009_ai.sql
-- AI agent module: provider configs, agent definitions, conversations, and
-- messages. See docs/ai.md.
--
-- Design notes:
--  * API keys live in ai_provider_configs.api_key_enc, AES-256-GCM encrypted
--    by a key derived from the session HMAC secret. The "direction" column
--    distinguishes chat completion vs embedding providers; only chat is used
--    by the v1 runtime.
--  * ai_agents holds admin-defined agents: name, model, system prompt, and
--    the JSON list of allowed tool keys. The runtime clones these into the
--    conversation's snapshot columns so that edits to an agent do not change
--    the behaviour of an in-flight conversation retroactively.
--  * ai_conversations owns the monotonic revision counter (one row per user)
--    mirroring the vault's delta-sync pattern; clients can poll for changes.
--  * ai_messages stores ordered turns; tool_calls and tool_results keep their
--    raw JSON so the runtime can replay tool dispatch on history rebuild.

CREATE TABLE ai_provider_configs (
  id            TEXT PRIMARY KEY NOT NULL,
  direction     TEXT NOT NULL CHECK (direction IN ('chat','embed')),
  driver        TEXT NOT NULL,                 -- openai | anthropic | ollama
  name          TEXT NOT NULL,                 -- display label
  base_url      TEXT NOT NULL DEFAULT '',      -- override endpoint (e.g. OpenRouter, vLLM)
  model_hint    TEXT NOT NULL DEFAULT '',      -- default model id
  api_key_enc   TEXT NOT NULL DEFAULT '',      -- AES-256-GCM ciphertext (base64)
  api_key_nonce TEXT NOT NULL DEFAULT '',      -- GCM nonce (base64)
  is_active     INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  CHECK (direction <> 'chat' OR driver IN ('openai','anthropic','ollama'))
);
CREATE INDEX idx_ai_provider_direction_active ON ai_provider_configs(direction, is_active);

CREATE TABLE ai_agents (
  id            TEXT PRIMARY KEY NOT NULL,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL,                 -- provider-specific model id
  system_prompt TEXT NOT NULL DEFAULT '',
  tools         TEXT NOT NULL DEFAULT '[]',    -- JSON array of tool keys
  temperature   REAL    NOT NULL DEFAULT 0.7,
  max_tokens    INTEGER NOT NULL DEFAULT 0,    -- 0 = provider default
  max_turns     INTEGER NOT NULL DEFAULT 6,    -- tool dispatch loop cap
  is_builtin    INTEGER NOT NULL DEFAULT 0,
  is_active     INTEGER NOT NULL DEFAULT 1,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX idx_ai_agents_active ON ai_agents(is_active);

CREATE TABLE ai_conversations (
  id              TEXT PRIMARY KEY NOT NULL,
  user_id         TEXT NOT NULL,
  agent_id        TEXT NOT NULL,
  title           TEXT NOT NULL DEFAULT '',
  -- Snapshot of agent fields at creation time, so later agent edits do not
  -- retroactively change in-flight conversations.
  snap_model      TEXT NOT NULL,
  snap_system     TEXT NOT NULL DEFAULT '',
  snap_tools      TEXT NOT NULL DEFAULT '[]',
  snap_temperature REAL    NOT NULL DEFAULT 0.7,
  snap_max_tokens INTEGER NOT NULL DEFAULT 0,
  snap_max_turns  INTEGER NOT NULL DEFAULT 6,
  revision        INTEGER NOT NULL DEFAULT 1,  -- monotonic per-user sync cursor
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at      DATETIME,
  FOREIGN KEY (user_id)  REFERENCES users(id)     ON DELETE CASCADE,
  FOREIGN KEY (agent_id) REFERENCES ai_agents(id) ON DELETE RESTRICT
);
CREATE INDEX idx_ai_conversations_sync ON ai_conversations(user_id, revision);
CREATE INDEX idx_ai_conversations_user_updated ON ai_conversations(user_id, deleted_at, updated_at DESC);

CREATE TABLE ai_messages (
  id              TEXT PRIMARY KEY NOT NULL,
  conversation_id TEXT NOT NULL,
  role            TEXT NOT NULL CHECK (role IN ('system','user','assistant','tool')),
  content         TEXT NOT NULL DEFAULT '',
  tool_calls      TEXT NOT NULL DEFAULT '[]',  -- JSON: assistant-issued calls
  tool_call_id    TEXT NOT NULL DEFAULT '',    -- set when role='tool'
  tool_name       TEXT NOT NULL DEFAULT '',    -- set when role='tool'
  -- Token usage for assistant turns; 0 for user/tool rows.
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  seq              INTEGER NOT NULL,           -- monotonic within a conversation
  created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE CASCADE
);
CREATE INDEX idx_ai_messages_conv_seq ON ai_messages(conversation_id, seq);

-- Daily token usage rollup (one row per user per UTC day). The admin panel
-- reads this directly instead of scanning ai_messages.
CREATE TABLE ai_usage_daily (
  user_id            TEXT NOT NULL,
  day                TEXT NOT NULL,            -- YYYY-MM-DD (UTC)
  prompt_tokens      INTEGER NOT NULL DEFAULT 0,
  completion_tokens  INTEGER NOT NULL DEFAULT 0,
  request_count      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
