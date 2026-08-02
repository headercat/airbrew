# Drive module

A per-user file store: a self-referential tree of files and folders backed by
the blob store, with uploads, downloads, move/copy, star, a soft-delete trash,
public share links, and per-workspace storage limits.

Each user owns one root (the set of nodes whose `parent_id` is NULL). File bytes
live in the blob store under the `drive` namespace and are referenced from
`drive_nodes.blob_path`; the metadata row also records a `sha256` content hash
for integrity checks and future deduplication. Folders are rows with no blob.

## Data model

Migration `0008_drive.sql` follows the style of `0001_initial.sql`. All PKs use
`internal/id`; `DATETIME` defaults and `INTEGER` booleans follow existing
convention.

- **`drive_nodes`** — unified files + folders. Self-referential `parent_id`
  (NULL = root) gives a tree; `kind` ∈ `file`/`folder`; `deleted_at` non-NULL
  means the node is in the trash. Foreign keys cascade on user deletion and on
  parent deletion (so removing a folder drops its subtree).
- **`drive_shares`** — public share links. One row per link, addressed by a
  random `token`; `password_hash` (argon2id) and `expires_at` are optional;
  `downloads` counts accesses; `is_active` is flipped off by the janitor when a
  share expires.

Name uniqueness is intentionally **not** enforced (duplicate names in the same
folder are allowed, matching common drives); the kind/name ordering keeps the
listing stable.

## Trash semantics

Soft-deleting a folder trashes the whole subtree via a recursive CTE. The trash
view lists only top-level trashed items (a trashed folder's children are hidden
so they aren't double-counted). Restoring a node restores its subtree; emptying
the trash (or deleting from it permanently) hard-deletes and purges the
dropped blobs.

## Storage limits

Per-workspace limits live in `server_settings` under `module.drive.config` as
JSON (`{"max_upload_bytes","quota_bytes"}`), with defaults of 50 MiB per upload
and 1 GiB per user. The service holds the active config in an `atomic.Pointer`
so an admin `PUT /api/admin/drive/config` applies live with no restart. Uploads
stream through a sha256 hasher and a size counter; the per-upload cap aborts an
oversized stream, and the quota is checked against the freshly-counted size.

## Background janitor

Started by the Module with the process-lifetime context (nil ctx in tests skips
it). Every six hours it (1) deactivates shares whose `expires_at` has passed and
(2) sweeps orphan blobs in the `drive` namespace — listing the blob store and
deleting any path not referenced by a `drive_nodes` row. Both passes are
best-effort: errors log at Warn and never fail the process.

## Module layout

`internal/drive/` mirroring `internal/mail/` and `internal/passwords/`:

```
internal/drive/
  drive.go            Module wiring (New, RegisterRoutes, RegisterPublicRoutes, RegisterAdminRoutes)
  files/              Node + Share domain, repository, service, janitor
  handler/            JSON HTTP handlers (user, admin, public share)
```

## Endpoint inventory

```
# Public (no session)
GET  /api/drive/status
GET  /api/drive/s/{token}                  (share metadata; ?pw= for password)
GET  /api/drive/s/{token}/download         (file bytes; ?pw= / X-Share-Password)

# Authenticated user endpoints (session required)
GET    /api/drive/files?parent=&view=&folder=&q=&sort=&order=&kind=&limit=&offset=
                                        → {nodes, total} (total drives the pager)
POST   /api/drive/files                    (multipart "file" or raw body; ?parent=&name=)
POST   /api/drive/folders                  {name, parent_id}
GET    /api/drive/files/{id}
GET    /api/drive/files/{id}/path          (ancestor chain root→node for breadcrumbs)
PATCH  /api/drive/files/{id}               {name? & parent_id? & starred? — all-at-once}
DELETE /api/drive/files/{id}?permanent=
POST   /api/drive/files/{id}/restore
POST   /api/drive/files/{id}/copy          {parent_id, name}
GET    /api/drive/files/{id}/download?inline=
POST   /api/drive/trash/empty
GET    /api/drive/usage

GET    /api/drive/shares
GET    /api/drive/files/{id}/shares
POST   /api/drive/files/{id}/shares        {password?, expires_in_seconds?}
DELETE /api/drive/shares/{id}

# Admin endpoints (admin session required)
GET  /api/admin/drive/config
PUT  /api/admin/drive/config               {max_upload_bytes?, quota_bytes?}
```

## Frontend

`web/src/pages/drive/`:

- `index.tsx` — folder navigation with breadcrumbs, drag-and-drop + button
  upload, search, sort, star, rename, move, share, download, soft-delete trash
  with restore/empty, a per-user quota bar, and a shared-links view. Reachable
  at `/drive` (mounted above the `:module` stub catch-all).
- `share.tsx` — public share landing page at `/s/:token`, with a password gate
  and a download button; reachable without a session.

## Decisions

- **Unified nodes table.** Files and folders share `drive_nodes` so a
  self-referential tree with move/rename is simple, and recursive-CTE
  trash/restore/cascade-delete fall out for free.
- **Streaming upload.** Content is hashed and counted while streaming into the
  blob store via `io.TeeReader`; the per-upload limit aborts an oversized
  stream without buffering the whole file.
- **Share passwords.** Reuse `internal/auth/password` (argon2id) so share links
  share the codebase's existing password primitive. Password is sent on the
  download URL (`?pw=`) / `X-Share-Password` header; a landing page gates it.
- **No thumbnail generation.** Image previews use the original blob URL (the
  browser scales them), avoiding a new image-processing dependency.

## Milestone roadmap

| #  | Milestone                                                            |
| -- | -------------------------------------------------------------------- |
| 1  | Schema `0008_drive.sql`, Node/Share domain, repository               |
| 2  | Service: upload/download, folders, move/copy, star, trash            |
| 3  | User HTTP endpoints + module wiring + status                         |
| 4  | Public share links (token/password/expiry) + share landing page      |
| 5  | Per-user quota + admin config + janitor (orphan blobs, expired shares) |
| 6  | SPA drive UI (folders, uploads, search, trash, shares)               |
| 7  | Folder download as zip, dedup-by-hash storage, resumable uploads     |
