-- Real-time chat module: rooms, participants, messages, and read state.

PRAGMA foreign_keys = ON;

CREATE TABLE chat_rooms (
  id          TEXT PRIMARY KEY NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('direct', 'group')),
  title       TEXT,
  created_by  TEXT NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  deleted_at  DATETIME,
  FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_chat_rooms_updated_at ON chat_rooms(updated_at DESC);

CREATE TABLE chat_room_participants (
  room_id      TEXT NOT NULL,
  user_id      TEXT NOT NULL,
  role         TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
  joined_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  last_read_seq INTEGER NOT NULL DEFAULT 0,
  muted_until  DATETIME,
  PRIMARY KEY (room_id, user_id),
  FOREIGN KEY (room_id) REFERENCES chat_rooms(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_chat_participants_user ON chat_room_participants(user_id, room_id);

CREATE TABLE chat_messages (
  id         TEXT PRIMARY KEY NOT NULL,
  room_id    TEXT NOT NULL,
  sender_id  TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  body       TEXT NOT NULL,
  edited_at  DATETIME,
  deleted_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  UNIQUE (room_id, seq),
  FOREIGN KEY (room_id) REFERENCES chat_rooms(id) ON DELETE CASCADE,
  FOREIGN KEY (sender_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_chat_messages_room_seq ON chat_messages(room_id, seq DESC);

CREATE TABLE chat_direct_room_keys (
  user_low_id  TEXT NOT NULL,
  user_high_id TEXT NOT NULL,
  room_id      TEXT NOT NULL UNIQUE,
  PRIMARY KEY (user_low_id, user_high_id),
  FOREIGN KEY (user_low_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (user_high_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY (room_id) REFERENCES chat_rooms(id) ON DELETE CASCADE
);
