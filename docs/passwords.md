# Passwords module (vault)

A zero-knowledge password manager. The server stores only ciphertext and key
envelopes — it can never decrypt vault contents. Any authenticated device can
sync the encrypted vault and, with the user's master password, decrypt locally.

## Trust boundary & threat model

| Threat                              | Mitigation                                                  |
| ----------------------------------- | ----------------------------------------------------------- |
| SQLite DB file leaked at rest       | All vault fields are AES-256-GCM ciphertext; server has no key |
| Server memory observed during sync  | Vault key never leaves the client; server only relays ciphertext |
| Master password guessed             | Argon2id KDF (tunable memory/time), client-side              |
| New device login                    | Pulls encrypted envelope + ciphertext, decrypts locally       |
| Concurrent edits from two devices   | Per-row optimistic concurrency via `revision` + sync cursor  |
| Master password lost                | **No recovery.** Encrypted export is the backup path         |

The master password is **distinct from the login password** (Milestone 1 auth).
Reusing it would break zero-knowledge because the existing login flow receives
the plaintext password over the wire to argon2id it server-side. The login
password authenticates the session; the master password unlocks the vault.

## Cryptographic design

Two-layer key hierarchy (Bitwarden-style). The outer layer is derived from the
master password; the inner key encrypts the data and never changes — so a master
password rotation re-wraps only one small envelope, not every item.

```
master password ──Argon2id(salt, m/t/p)──▶ master key (32 B, client only)
                                              │
                              AES-256-GCM wrap│  (one envelope, synced)
                                              ▼
                                       vault key (32 B random, client only)
                                              │
                              AES-256-GCM     │  encrypts every item/folder/attachment
                                              ▼
                                       vault ciphertext (stored on server)
```

- **KDF**: Argon2id, per-user random 16 B salt, params stored per-user so they
  can be raised over time (`memory_kib`, `iterations`, `parallelism`).
- **Wrap**: AES-256-GCM gives confidentiality + authenticity in one primitive,
  so no separate HMAC key is needed (unlike CBC+HMAC). Each ciphertext stores its
  own 12 B random nonce alongside.
- **Vault key**: 32 random bytes, generated once at setup, constant for the
  account lifetime. Rotating it (rare) means re-encrypting all items.
- **Per-attachment keys**: each attachment gets its own 32 B file key, itself
  encrypted with the vault key. Lets large files stream under a dedicated key and
  reserves a path for future per-item sharing.

All crypto runs in the SPA (WebCrypto for AES-GCM/HKDF; Argon2id via WASM). The
Go server never executes the KDF and never holds the vault key. Existing
`golang.org/x/crypto/argon2` is used only for the login-password hash.

## Lifecycle flows

**Setup (once):** client generates salt + vault key, prompts for master
password, derives master key, wraps vault key, `POST /api/vault/setup`.

**Unlock (any device, after login):** `GET /api/vault/keys` → envelope → user
types master password → client derives master key → decrypts vault key. Wrong
password = GCM auth-tag failure (client reports "invalid", no server round-trip).

**Sync:** client holds the highest `revision` cursor it has seen;
`GET /api/vault/sync?since=<cursor>` returns every changed row (inserts, updates,
and soft-deleted tombstones). Client decrypts and merges into local IndexedDB.

**Add/update item:** client encrypts fields with vault key, sends ciphertext;
server stamps the next per-user `revision` on the row and returns it.

**Master password change:** with the vault key already in memory, derive a new
master key from the new password, re-wrap the **same** vault key, `POST
/api/vault/keys/rotate` with the new envelope. Items are untouched.

## Data model

A new migration `0005_vault.sql` (mirrors style of `0001_initial.sql`). All PKs
use `internal/id`; `DATETIME` defaults follow existing convention.

```sql
-- Per-user vault key envelope (1:1)
CREATE TABLE vault_keys (
  user_id                  TEXT PRIMARY KEY NOT NULL,
  kdf_algorithm            TEXT NOT NULL DEFAULT 'argon2id',
  kdf_salt                 TEXT NOT NULL,            -- base64, 16 B random
  kdf_memory_kib           INTEGER NOT NULL DEFAULT 65536,
  kdf_iterations           INTEGER NOT NULL DEFAULT 3,
  kdf_parallelism          INTEGER NOT NULL DEFAULT 2,
  protected_vault_key      TEXT NOT NULL,            -- base64 AES-GCM(vaultKey)
  protected_vault_nonce    TEXT NOT NULL,
  created_at               DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at               DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Per-user monotonic sync cursor
CREATE TABLE vault_sync_state (
  user_id        TEXT PRIMARY KEY NOT NULL,
  current_rev    INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Folders (encrypted name)
CREATE TABLE vault_folders (
  id            TEXT PRIMARY KEY NOT NULL,
  user_id       TEXT NOT NULL,
  name_cipher   TEXT NOT NULL,
  name_nonce    TEXT NOT NULL,
  revision      INTEGER NOT NULL,        -- per-user sync cursor value
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at    DATETIME,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_vault_folders_sync ON vault_folders(user_id, revision);

-- Items: login / secure_note / card / identity
CREATE TABLE vault_items (
  id            TEXT PRIMARY KEY NOT NULL,
  user_id       TEXT NOT NULL,
  type          TEXT NOT NULL CHECK (type IN ('login','secure_note','card','identity')),
  folder_id     TEXT,
  name_cipher   TEXT NOT NULL,
  name_nonce    TEXT NOT NULL,
  data_cipher   TEXT NOT NULL,          -- encrypted JSON of type-specific fields
  data_nonce    TEXT NOT NULL,
  notes_cipher  TEXT,
  notes_nonce   TEXT,
  favorite      INTEGER NOT NULL DEFAULT 0,
  reprompt      INTEGER NOT NULL DEFAULT 0,   -- require master re-entry to view
  revision      INTEGER NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at    DATETIME,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (folder_id) REFERENCES vault_folders(id) ON DELETE SET NULL
);
CREATE INDEX idx_vault_items_sync ON vault_items(user_id, revision);

-- Per-item history (encrypted snapshots)
CREATE TABLE vault_item_revisions (
  id            TEXT PRIMARY KEY NOT NULL,
  item_id       TEXT NOT NULL,
  type          TEXT,
  folder_id     TEXT,
  name_cipher   TEXT NOT NULL,
  name_nonce    TEXT NOT NULL,
  data_cipher   TEXT NOT NULL,
  data_nonce    TEXT NOT NULL,
  notes_cipher  TEXT,
  notes_nonce   TEXT,
  favorite      INTEGER,
  reprompt      INTEGER,
  revision      INTEGER NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (item_id) REFERENCES vault_items(id) ON DELETE CASCADE
);
CREATE INDEX idx_vault_item_revisions_item ON vault_item_revisions(item_id, created_at DESC);

-- Attachments (encrypted via internal/blob)
CREATE TABLE vault_attachments (
  id              TEXT PRIMARY KEY NOT NULL,
  item_id         TEXT NOT NULL,
  blob_path       TEXT NOT NULL,        -- opaque path from blob.Store.Save
  size_bytes      INTEGER NOT NULL,
  file_key_cipher TEXT NOT NULL,        -- AES-GCM(fileKey) under vault key
  file_key_nonce  TEXT NOT NULL,
  name_cipher     TEXT NOT NULL,        -- encrypted original filename
  name_nonce      TEXT NOT NULL,
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (item_id) REFERENCES vault_items(id) ON DELETE CASCADE
);
```

## Sync mechanism

- A single per-user counter `vault_sync_state.current_rev` is bumped inside the
  same transaction as every write (insert/update/soft-delete). The new value is
  stamped onto the row's `revision`.
- `GET /api/vault/sync?since=N` returns all `vault_folders` and `vault_items`
  rows for the user with `revision > N`, plus the new cursor. Tombstones are
  soft-deleted rows still carrying a `revision`; the client deletes them locally.
- Tombstones older than 30 days are purged by a janitor (no cursor bump).
- **Optimistic concurrency:** on `PUT`/`DELETE` the client sends the `revision`
  it last saw; the server applies the change only if the stored `revision`
  matches, otherwise returns `409` with the current row so the client can merge.
  MVP policy is last-write-wins after re-fetch.
- A cold sync (`since=0`) returns the entire encrypted vault; this is how a new
  device boots.

## Endpoint inventory (target)

```
POST   /api/vault/setup                      setup envelope (first time)
GET    /api/vault/keys                       fetch envelope for unlock
POST   /api/vault/keys/rotate                master-password change (re-wrap)

GET    /api/vault/sync?since=                delta or full sync
POST   /api/vault/items                      create
GET    /api/vault/items/:id                  read one
PUT    /api/vault/items/:id                  update (revision-guarded)
DELETE /api/vault/items/:id                  soft delete

POST   /api/vault/folders
PUT    /api/vault/folders/:id
DELETE /api/vault/folders/:id

GET    /api/vault/items/:id/revisions        history
POST   /api/vault/items/:id/attachments      upload encrypted blob
GET    /api/vault/items/:id/attachments/:aid download
DELETE /api/vault/items/:id/attachments/:aid

POST   /api/vault/export                     encrypted export bundle
POST   /api/vault/import                     restore
```

## Search

Server cannot `WHERE` on ciphertext. The vault is small (personal use), so the
SPA decrypts the synced vault into IndexedDB and searches locally. A blind
HMAC index for exact-match lookup is reserved as future work if local search
proves insufficient.

## Module layout

`internal/passwords/` mirroring `internal/auth/`:

```
internal/passwords/
  vault/        domain types (Item, Folder), repository, service
  handler/      JSON HTTP handlers
  Module        registered in internal/modules + internal/server
```

Catalog entry to add to `internal/modules/modules.go`:

```go
{Key: "passwords", Name: "Passwords",
 Description: "Zero-knowledge password vault with multi-device sync."},
```

## Milestone roadmap

| #  | Milestone                                                |
| -- | -------------------------------------------------------- |
| 1  | Schema `0005_vault.sql` + key envelope endpoints (setup/keys/rotate) |
| 2  | Item + folder CRUD with revision sync cursor             |
| 3  | `/api/vault/sync` delta/full + soft-delete tombstones    |
| 4  | Optimistic-concurrency conflict handling (409 path)      |
| 5  | SPA crypto layer (Argon2id WASM, AES-GCM), unlock UI     |
| 6  | Attachment upload/download via `internal/blob`           |
| 7  | Item history (revisions)                                 |
| 8  | Encrypted export/import                                  |
| 9  | Auto-lock timers + clipboard clear; audit events         |

## Decisions still open

- **Argon2id in the browser.** Match the login KDF via a WASM build, or fall
  back to PBKDF2-SHA256 (native WebCrypto) to avoid a WASM bundle. Will decide
  in milestone 5.
- **Vault key rotation.** Rare; would re-encrypt every item. Decide whether to
  expose it or keep it as an admin/manual operation.
- **Recovery code.** Out of MVP (no recovery). A printed offline code that is
  itself a second master password is a possible later addition.
- **Folder nesting.** Schema is flat for MVP; nested folders can be added with a
  nullable `parent_id` without breaking sync.
