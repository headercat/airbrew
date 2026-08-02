-- 0013_contacts.sql
-- Contacts module: per-user address book with groups. See docs/contacts.md.
--
-- contacts holds one address-book entry per row. The structured multi-value
-- fields (emails, phones, addresses, ims, urls) are stored as JSON arrays of
-- typed objects so vCard 4.0 round-trips losslessly while still allowing
-- per-field queries via JSON helpers when needed. The flat name/company
-- columns are extracted so listings and ordering stay cheap. An avatar photo,
-- when uploaded, lives in the blob store (namespace "contacts") and is
-- referenced by avatar_path.
--
-- contact_groups are user-defined labels (Family, Work, ...). Membership is a
-- many-to-many join in contact_group_members; both sides cascade so deleting a
-- contact or a group cleans up the rows automatically.

-- Address-book entries
CREATE TABLE contacts (
  id           TEXT PRIMARY KEY NOT NULL,
  user_id      TEXT NOT NULL,
  name_prefix  TEXT NOT NULL DEFAULT '',    -- honorific prefix (Dr., Mr., ...)
  given_name   TEXT NOT NULL DEFAULT '',    -- first name
  middle_name  TEXT NOT NULL DEFAULT '',
  family_name  TEXT NOT NULL DEFAULT '',    -- last name
  name_suffix  TEXT NOT NULL DEFAULT '',    -- generational/credentials (Jr., PhD)
  display_name TEXT NOT NULL DEFAULT '',    -- formatted full name / nickname, used for sorting
  nickname     TEXT NOT NULL DEFAULT '',
  company      TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',    -- job title
  department   TEXT NOT NULL DEFAULT '',
  emails       TEXT NOT NULL DEFAULT '[]',  -- JSON [{value,type}]
  phones       TEXT NOT NULL DEFAULT '[]',  -- JSON [{value,type}]
  addresses    TEXT NOT NULL DEFAULT '[]',  -- JSON [{type,street,locality,region,postal_code,country}]
  ims          TEXT NOT NULL DEFAULT '[]',  -- JSON [{value,type}] instant messaging
  urls         TEXT NOT NULL DEFAULT '[]',  -- JSON [{value,type}]
  birthday     DATETIME,                    -- NULL = unknown
  notes        TEXT NOT NULL DEFAULT '',
  avatar_path  TEXT,                        -- namespace/name in blob store; NULL = none
  is_favorite  INTEGER NOT NULL DEFAULT 0,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_contacts_user        ON contacts(user_id, display_name COLLATE NOCASE);
CREATE INDEX idx_contacts_user_fav    ON contacts(user_id, is_favorite, display_name COLLATE NOCASE);
CREATE INDEX idx_contacts_user_company ON contacts(user_id, company COLLATE NOCASE);
CREATE INDEX idx_contacts_avatar      ON contacts(avatar_path);

-- User-defined groups / labels
CREATE TABLE contact_groups (
  id         TEXT PRIMARY KEY NOT NULL,
  user_id    TEXT NOT NULL,
  name       TEXT NOT NULL,
  color      TEXT,                          -- optional hex color hint for UI
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (user_id, name COLLATE NOCASE),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_contact_groups_user ON contact_groups(user_id);

-- Many-to-many membership between contacts and groups
CREATE TABLE contact_group_members (
  contact_id TEXT NOT NULL,
  group_id   TEXT NOT NULL,
  PRIMARY KEY (contact_id, group_id),
  FOREIGN KEY (contact_id) REFERENCES contacts(id)        ON DELETE CASCADE,
  FOREIGN KEY (group_id)   REFERENCES contact_groups(id)  ON DELETE CASCADE
);
CREATE INDEX idx_cgm_group   ON contact_group_members(group_id);
CREATE INDEX idx_cgm_contact ON contact_group_members(contact_id);
