# Contacts module

A per-user address book: vCard-style contacts with structured names,
multi-value emails/phones/addresses/IMs/URLs, notes, an optional avatar,
user-defined groups (labels) with many-to-many membership, favorites, search,
and vCard 4.0 import/export.

Each user owns one address book (the set of `contacts` rows whose `user_id`
matches). Avatars live in the blob store under the `contacts` namespace and are
referenced from `contacts.avatar_path`; the metadata row is otherwise
self-contained. Multi-value fields are stored as JSON arrays of typed objects
so a vCard round-trips losslessly.

## Data model

Migration `0013_contacts.sql` follows the style of `0008_drive.sql`. All PKs use
`internal/id`; `DATETIME` defaults and `INTEGER` booleans follow existing
convention. Foreign keys cascade on user deletion.

- **`contacts`** — one address-book entry per row. Flat name/company columns are
  extracted for cheap listing/ordering; the structured multi-value fields
  (`emails`, `phones`, `addresses`, `ims`, `urls`) are JSON arrays. `birthday`
  is nullable; `avatar_path` references the blob store (NULL = none);
  `is_favorite` is a boolean flag. Indexes cover the listing query
  (`user_id, display_name`), the favorites view, the company grouping, and
  avatar lookup.
- **`contact_groups`** — user-defined labels (Family, Work, ...). `name` is
  unique per user (case-insensitive); `color` is an optional UI hint. Deleting
  a user cascades.
- **`contact_group_members`** — many-to-many join. Both sides cascade, so
  deleting a contact or a group cleans up membership rows automatically.

Name uniqueness is intentionally **not** enforced on contacts (duplicate names
are allowed, matching common address books); the display-name ordering keeps
the listing stable.

## Search

Listing filters by group membership, favorites, and a free-text `q` that matches
across `display_name`, `given_name`, `family_name`, `nickname`, `company`,
`emails`, `phones`, and `notes` (LIKE-based, wildcard-escaped). Pagination
defaults to 100 and caps at 200, matching the rest of the codebase.

## vCard import / export

- `POST /api/contacts/import` parses a vCard stream (2.1/3.0/4.0, single- or
  multi-card) and creates a contact per record. The parser folds continuation
  lines and ignores unknown properties; it caps at 5000 records to bound memory.
- `GET /api/contacts/export` serialises every contact (up to 2000) as vCard 4.0
  (`Content-Type: text/vcard`).

## Avatars

`POST /api/contacts/{id}/avatar` accepts a raw body or `multipart/form-data`
`file` field (capped at 8 MiB). The bytes are stored in the blob store and the
previous avatar (if any) is purged best-effort. `DELETE` clears the reference
and purges the blob. Public URLs are served at `/api/files/<avatar_path>` by the
shared blob handler.

## Audit events

The module logs `contacts.*` events to the shared audit log: `contact_created`,
`contact_updated`, `contact_deleted`, `avatar_set`, `avatar_cleared`,
`groups_set`, `group_created`, `group_updated`, `group_deleted`, `import`, and
`export`.

## Module gating

`contacts` is registered in `internal/modules.Catalog` and the user-facing
endpoints are wrapped in `modules.RequireEnabled(state, "contacts")`, so an
admin can take the address book offline without a restart. The status endpoint
remains public so the SPA can show a disabled state.
