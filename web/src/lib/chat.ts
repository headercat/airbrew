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
  markRead: (roomID: string, seq = 0) =>
    api.post<{ ok: true; seq: number }>(`/api/chat/rooms/${roomID}/read`, {
      seq,
    }),
};

export function openChatEvents(onEvent: (event: ChatEvent) => void) {
  const es = new EventSource("/api/chat/events");
  const types = [
    "room.created",
    "room.updated",
    "message.created",
    "room.read",
  ];
  for (const type of types) {
    es.addEventListener(type, (ev) => {
      try {
        onEvent(JSON.parse((ev as MessageEvent).data) as ChatEvent);
      } catch {
        /* ignore malformed stream frames */
      }
    });
  }
  return es;
}
