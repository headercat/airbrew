import { api } from "@/lib/api";

export type ChatUser = {
  id: string;
  email: string;
  display_name: string;
  avatar_url: string;
};

export type ChatParticipant = {
  user: ChatUser;
  role: "owner" | "member" | string;
  joined_at: string;
  last_read_seq: number;
};

export type ChatMessage = {
  id: string;
  room_id: string;
  sender: ChatUser;
  sender_id: string;
  seq: number;
  body: string;
  created_at: string;
  edited_at?: string;
  deleted_at?: string;
  read_by_count?: number;
};

export type ChatRoom = {
  id: string;
  kind: "direct" | "group";
  title: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  participants?: ChatParticipant[];
  last_message?: ChatMessage;
  last_read_seq: number;
  unread_count: number;
};

export type ChatEvent =
  | {
      type: "room.created" | "room.updated";
      room_id: string;
      room?: ChatRoom;
      user_id?: string;
    }
  | { type: "message.created"; room_id: string; message: ChatMessage }
  | { type: "message.updated"; room_id: string; message: ChatMessage }
  | { type: "message.deleted"; room_id: string; message: { id: string } }
  | { type: "room.read"; room_id: string; user_id: string; seq: number };

export const chat = {
  searchUsers: async (q: string) => {
    const res = await api.get<{ users: ChatUser[] }>(
      `/api/chat/users?q=${encodeURIComponent(q)}`,
    );
    return res.users ?? [];
  },
  listRooms: async () => {
    const res = await api.get<{ rooms: ChatRoom[] }>("/api/chat/rooms");
    return res.rooms ?? [];
  },
  getRoom: (id: string) => api.get<ChatRoom>(`/api/chat/rooms/${id}`),
  createDirect: (userID: string) =>
    api.post<ChatRoom>("/api/chat/rooms", {
      kind: "direct",
      member_ids: [userID],
    }),
  createGroup: (title: string, memberIDs: string[]) =>
    api.post<ChatRoom>("/api/chat/rooms", {
      kind: "group",
      title,
      member_ids: memberIDs,
    }),
  renameRoom: (id: string, title: string) =>
    api.patch<ChatRoom>(`/api/chat/rooms/${id}`, { title }),
  leaveRoom: (id: string) => api.del<{ ok: true }>(`/api/chat/rooms/${id}`),
  listMessages: async (roomID: string, beforeSeq?: number) => {
    const qs = beforeSeq ? `?before_seq=${beforeSeq}` : "";
    const res = await api.get<{ messages: ChatMessage[] }>(
      `/api/chat/rooms/${roomID}/messages${qs}`,
    );
    return res.messages ?? [];
  },
  sendMessage: (roomID: string, body: string) =>
    api.post<ChatMessage>(`/api/chat/rooms/${roomID}/messages`, { body }),
  editMessage: (roomID: string, messageID: string, body: string) =>
    api.patch<ChatMessage>(
      `/api/chat/rooms/${roomID}/messages/${messageID}`,
      { body },
    ),
  deleteMessage: (roomID: string, messageID: string) =>
    api.del<{ ok: boolean }>(
      `/api/chat/rooms/${roomID}/messages/${messageID}`,
    ),
  markRead: (roomID: string, seq = 0) =>
    api.post<{ ok: true; seq: number }>(`/api/chat/rooms/${roomID}/read`, {
      seq,
    }),
};

export function openChatEvents(
  onEvent: (event: ChatEvent) => void,
  opts?: {
    getSinceCursor?: () => string;
    onReady?: (cursor: string, hasMore: boolean) => void;
  },
) {
  // The browser EventSource reuses the same URL on its built-in reconnect, so
  // we cannot update the replay cursor dynamically that way. Close on error
  // and reopen with the latest cursor so the server replays any message.created
  // frames missed while disconnected (or while the hub dropped a frame because
  // the subscriber fell behind). The cursor is created_at-based (seq is only
  // unique per room, so a seq cursor would lose low-activity rooms).
  const factory = (cursor: string) => {
    const qs = cursor ? `?since_cursor=${encodeURIComponent(cursor)}` : "";
    return new EventSource(`/api/chat/events${qs}`);
  };
  const types = [
    "room.created",
    "room.updated",
    "message.created",
    "message.updated",
    "message.deleted",
    "room.read",
  ];
  const bind = (stream: EventSource) => {
    stream.addEventListener("ready", (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent).data) as {
          ok: boolean;
          cursor?: string;
          has_more?: boolean;
        };
        if (data.cursor) opts?.onReady?.(data.cursor, data.has_more ?? false);
      } catch {
        /* ignore malformed ready frame */
      }
    });
    for (const type of types) {
      stream.addEventListener(type, (ev) => {
        try {
          onEvent(JSON.parse((ev as MessageEvent).data) as ChatEvent);
        } catch {
          /* ignore malformed stream frames */
        }
      });
    }
  };
  let es = factory(opts?.getSinceCursor?.() ?? "");
  const reconnect = () => {
    es.close();
    window.setTimeout(() => {
      es = factory(opts?.getSinceCursor?.() ?? "");
      bind(es);
      es.onerror = () => reconnect();
    }, 1500);
  };
  // Wrap onReady so a backlog that exceeded the replay cap (has_more) keeps
  // paging by reopening the stream with the now-advanced cursor, instead of
  // dropping the unreplayed tail.
  const upstreamReady = opts?.onReady;
  if (upstreamReady) {
    opts.onReady = (cursor, hasMore) => {
      upstreamReady(cursor, hasMore);
      if (hasMore) {
        es.close();
        es = factory(opts.getSinceCursor?.() ?? "");
        bind(es);
        es.onerror = () => reconnect();
      }
    };
  }
  bind(es);
  es.onerror = () => reconnect();
  return { close: () => es.close() };
}
