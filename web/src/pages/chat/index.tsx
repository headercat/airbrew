import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  CheckCheck,
  Edit3,
  Loader2,
  LogOut,
  MessageSquarePlus,
  Search,
  Send,
  Users,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Modal } from "@/components/ui/modal";
import { Textarea } from "@/components/ui/textarea";
import { PageWrapper } from "@/components/page";
import { isApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import {
  chat,
  openChatEvents,
  type ChatEvent,
  type ChatMessage,
  type ChatRoom,
  type ChatUser,
} from "@/lib/chat";
import { cn } from "@/lib/utils";

type DraftMessage = ChatMessage & { pending?: boolean; failed?: boolean };

export default function ChatPage() {
  const { t } = useTranslation();
  const { user } = useAuth();
  const [rooms, setRooms] = useState<ChatRoom[]>([]);
  const [activeID, setActiveID] = useState("");
  const [messages, setMessages] = useState<DraftMessage[]>([]);
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [messagesLoading, setMessagesLoading] = useState(false);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [composerOpen, setComposerOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  const activeRoom = rooms.find((r) => r.id === activeID) ?? null;
  const visibleRooms = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return rooms;
    return rooms.filter((r) => {
      const haystack = [
        r.title,
        r.last_message?.body ?? "",
        ...(r.participants ?? []).map((p) => p.user.email),
        ...(r.participants ?? []).map((p) => p.user.display_name),
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(q);
    });
  }, [rooms, query]);

  const loadRooms = useCallback(async () => {
    try {
      const next = await chat.listRooms();
      setRooms(next);
      setActiveID((cur) => cur || next[0]?.id || "");
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadRooms();
  }, [loadRooms]);

  useEffect(() => {
    const es = openChatEvents((ev) => {
      applyEvent(ev);
    });
    es.onerror = () => setError(t("chat.connectionLost"));
    return () => es.close();
  }, [t]);

  useEffect(() => {
    if (!activeID) {
      setMessages([]);
      return;
    }
    let alive = true;
    setMessagesLoading(true);
    setError(null);
    Promise.all([chat.getRoom(activeID), chat.listMessages(activeID)])
      .then(([room, msgs]) => {
        if (!alive) return;
        setRooms((prev) => upsertRoom(prev, room));
        setMessages(msgs);
        if (msgs.length > 0) {
          void chat.markRead(activeID, msgs[msgs.length - 1].seq);
        }
      })
      .catch((e) => {
        if (alive) setError(errMsg(e));
      })
      .finally(() => {
        if (alive) setMessagesLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [activeID]);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, activeID]);

  function applyEvent(ev: ChatEvent) {
    switch (ev.type) {
      case "room.created":
      case "room.updated":
        if (ev.room) setRooms((prev) => upsertRoom(prev, ev.room!));
        else void loadRooms();
        break;
      case "message.created":
        setRooms((prev) =>
          moveRoomToTop(
            prev.map((room) =>
              room.id === ev.room_id
                ? {
                    ...room,
                    last_message: ev.message,
                    unread_count:
                      ev.room_id === activeID ||
                      ev.message.sender_id === user?.id
                        ? 0
                        : room.unread_count + 1,
                    updated_at: ev.message.created_at,
                  }
                : room,
            ),
            ev.room_id,
          ),
        );
        if (ev.room_id === activeID) {
          setMessages((prev) => mergeMessage(prev, ev.message));
          void chat.markRead(ev.room_id, ev.message.seq);
        }
        break;
      case "room.read":
        if (ev.room_id === activeID) {
          setMessages((prev) =>
            prev.map((m) =>
              m.seq <= ev.seq
                ? { ...m, read_by_count: Math.max(m.read_by_count ?? 0, 2) }
                : m,
            ),
          );
        }
        setRooms((prev) =>
          prev.map((r) =>
            r.id === ev.room_id && ev.user_id === user?.id
              ? { ...r, last_read_seq: ev.seq, unread_count: 0 }
              : r,
          ),
        );
        break;
    }
  }

  async function sendMessage() {
    if (!activeRoom || !draft.trim() || sending) return;
    const body = draft.trim();
    setDraft("");
    setSending(true);
    setError(null);
    const tmp: DraftMessage = {
      id: `tmp-${Date.now()}`,
      room_id: activeRoom.id,
      sender_id: user?.id ?? "",
      sender: {
        id: user?.id ?? "",
        email: user?.email ?? "",
        display_name: user?.display_name ?? "",
        avatar_url: user?.avatar_url ?? "",
      },
      seq: (messages.at(-1)?.seq ?? activeRoom.last_read_seq ?? 0) + 1,
      body,
      created_at: new Date().toISOString(),
      pending: true,
    };
    setMessages((prev) => [...prev, tmp]);
    try {
      const saved = await chat.sendMessage(activeRoom.id, body);
      setMessages((prev) =>
        mergeMessage(
          prev.filter((m) => m.id !== tmp.id),
          saved,
        ),
      );
      setRooms((prev) =>
        moveRoomToTop(
          prev.map((r) =>
            r.id === activeRoom.id
              ? {
                  ...r,
                  last_message: saved,
                  last_read_seq: saved.seq,
                  unread_count: 0,
                }
              : r,
          ),
          activeRoom.id,
        ),
      );
    } catch (e) {
      setError(errMsg(e));
      setMessages((prev) =>
        prev.map((m) =>
          m.id === tmp.id ? { ...m, pending: false, failed: true } : m,
        ),
      );
    } finally {
      setSending(false);
    }
  }

  async function loadEarlier() {
    if (!activeID || messages.length === 0) return;
    setMessagesLoading(true);
    try {
      const earlier = await chat.listMessages(activeID, messages[0].seq);
      setMessages((prev) => [...earlier, ...prev]);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setMessagesLoading(false);
    }
  }

  return (
    <PageWrapper>
      <div className="flex min-h-[calc(100vh-8rem)] flex-col overflow-hidden rounded-lg border bg-background lg:flex-row">
        <aside className="flex w-full flex-col border-b lg:w-80 lg:border-b-0 lg:border-r">
          <div className="flex items-center justify-between gap-2 border-b p-3">
            <div>
              <h1 className="text-base font-semibold">{t("chat.title")}</h1>
              <p className="text-xs text-muted-foreground">
                {t("chat.subtitle")}
              </p>
            </div>
            <Button
              size="icon"
              onClick={() => setComposerOpen(true)}
              title={t("chat.newRoom")}
            >
              <MessageSquarePlus className="h-4 w-4" />
            </Button>
          </div>
          <div className="border-b p-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t("chat.searchRooms")}
                className="pl-9"
              />
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">
            {loading ? (
              <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                {t("common.loading")}
              </div>
            ) : visibleRooms.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">
                {t("chat.noRooms")}
              </div>
            ) : (
              visibleRooms.map((room) => (
                <RoomRow
                  key={room.id}
                  room={room}
                  active={room.id === activeID}
                  currentUserID={user?.id ?? ""}
                  onClick={() => setActiveID(room.id)}
                />
              ))
            )}
          </div>
        </aside>

        <main className="flex min-w-0 flex-1 flex-col">
          {activeRoom ? (
            <>
              <div className="flex items-center justify-between gap-3 border-b p-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="truncate text-base font-semibold">
                      {activeRoom.title}
                    </h2>
                    <Badge variant="secondary">
                      {roomKindLabel(activeRoom.kind, t)}
                    </Badge>
                  </div>
                  <p className="truncate text-xs text-muted-foreground">
                    {(activeRoom.participants ?? [])
                      .map((p) => displayName(p.user))
                      .join(", ")}
                  </p>
                </div>
                <div className="flex items-center gap-1">
                  {activeRoom.kind === "group" &&
                    activeRoom.created_by === user?.id && (
                      <Button
                        variant="ghost"
                        size="icon"
                        onClick={() => setRenameOpen(true)}
                        title={t("chat.rename")}
                      >
                        <Edit3 className="h-4 w-4" />
                      </Button>
                    )}
                  {activeRoom.kind === "group" && (
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={async () => {
                        if (!confirm(t("chat.confirmLeave"))) return;
                        await chat.leaveRoom(activeRoom.id);
                        setRooms((prev) =>
                          prev.filter((r) => r.id !== activeRoom.id),
                        );
                        setActiveID("");
                      }}
                      title={t("chat.leave")}
                    >
                      <LogOut className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              </div>

              {error && (
                <div className="border-b bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {error}
                </div>
              )}

              <div
                ref={scrollRef}
                className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4"
              >
                {messages.length > 0 && (
                  <div className="flex justify-center">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={loadEarlier}
                      disabled={messagesLoading}
                    >
                      {messagesLoading && (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      )}
                      {t("chat.loadEarlier")}
                    </Button>
                  </div>
                )}
                {messages.length === 0 && !messagesLoading ? (
                  <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
                    {t("chat.emptyRoom")}
                  </div>
                ) : (
                  messages.map((msg) => (
                    <MessageBubble
                      key={msg.id}
                      message={msg}
                      mine={msg.sender_id === user?.id}
                    />
                  ))
                )}
              </div>

              <div className="border-t p-3">
                <div className="flex items-end gap-2">
                  <Textarea
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && !e.shiftKey) {
                        e.preventDefault();
                        void sendMessage();
                      }
                    }}
                    placeholder={t("chat.composerPlaceholder")}
                    className="max-h-40 min-h-[44px] resize-none"
                  />
                  <Button
                    size="icon"
                    onClick={() => void sendMessage()}
                    disabled={!draft.trim() || sending}
                    title={t("chat.send")}
                  >
                    {sending ? (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    ) : (
                      <Send className="h-4 w-4" />
                    )}
                  </Button>
                </div>
              </div>
            </>
          ) : (
            <div className="flex flex-1 items-center justify-center p-8 text-center text-sm text-muted-foreground">
              {t("chat.selectRoom")}
            </div>
          )}
        </main>

        <aside className="hidden w-72 border-l lg:block">
          <div className="border-b p-3">
            <h2 className="flex items-center gap-2 text-sm font-semibold">
              <Users className="h-4 w-4" />
              {t("chat.members")}
            </h2>
          </div>
          <div className="space-y-2 p-3">
            {(activeRoom?.participants ?? []).map((p) => (
              <div key={p.user.id} className="flex items-center gap-2">
                <UserAvatar user={p.user} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">
                    {displayName(p.user)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {p.user.email}
                  </div>
                </div>
                {p.role === "owner" && (
                  <Badge variant="outline">{t("chat.owner")}</Badge>
                )}
              </div>
            ))}
          </div>
        </aside>
      </div>

      <NewRoomModal
        open={composerOpen}
        onClose={() => setComposerOpen(false)}
        onCreated={(room) => {
          setRooms((prev) => upsertRoom(prev, room));
          setActiveID(room.id);
          setComposerOpen(false);
        }}
      />
      {activeRoom && (
        <RenameModal
          room={activeRoom}
          open={renameOpen}
          onClose={() => setRenameOpen(false)}
          onRenamed={(room) => {
            setRooms((prev) => upsertRoom(prev, room));
            setRenameOpen(false);
          }}
        />
      )}
    </PageWrapper>
  );
}

function RoomRow({
  room,
  active,
  currentUserID,
  onClick,
}: {
  room: ChatRoom;
  active: boolean;
  currentUserID: string;
  onClick: () => void;
}) {
  const people = room.participants ?? [];
  const other =
    people.find((p) => p.user.id !== currentUserID)?.user ?? people[0]?.user;
  return (
    <button
      className={cn(
        "flex w-full items-center gap-3 border-b p-3 text-left hover:bg-accent",
        active && "bg-accent",
      )}
      onClick={onClick}
    >
      <UserAvatar
        user={room.kind === "direct" && other ? other : undefined}
        group={room.kind === "group"}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm font-medium">{room.title}</span>
          {room.unread_count > 0 && (
            <span className="rounded-full bg-primary px-2 py-0.5 text-[11px] font-medium text-primary-foreground">
              {room.unread_count}
            </span>
          )}
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {room.last_message?.body || "No messages yet"}
        </p>
      </div>
    </button>
  );
}

function MessageBubble({
  message,
  mine,
}: {
  message: DraftMessage;
  mine: boolean;
}) {
  return (
    <div className={cn("flex gap-2", mine && "justify-end")}>
      {!mine && <UserAvatar user={message.sender} />}
      <div className={cn("max-w-[78%] space-y-1", mine && "items-end")}>
        {!mine && (
          <div className="text-xs text-muted-foreground">
            {displayName(message.sender)}
          </div>
        )}
        <div
          className={cn(
            "rounded-lg border px-3 py-2 text-sm leading-relaxed",
            mine ? "bg-primary text-primary-foreground" : "bg-muted",
            message.failed && "border-destructive",
          )}
        >
          <div className="whitespace-pre-wrap break-words">{message.body}</div>
        </div>
        <div
          className={cn(
            "flex items-center gap-1 text-[11px] text-muted-foreground",
            mine && "justify-end",
          )}
        >
          <span>{formatTime(message.created_at)}</span>
          {message.pending && <Loader2 className="h-3 w-3 animate-spin" />}
          {message.failed && <span className="text-destructive">Failed</span>}
          {mine && (message.read_by_count ?? 0) > 1 && (
            <CheckCheck className="h-3 w-3" />
          )}
        </div>
      </div>
    </div>
  );
}

function NewRoomModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (room: ChatRoom) => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"direct" | "group">("direct");
  const [query, setQuery] = useState("");
  const [title, setTitle] = useState("");
  const [results, setResults] = useState<ChatUser[]>([]);
  const [selected, setSelected] = useState<ChatUser[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    const handle = setTimeout(() => {
      chat
        .searchUsers(query)
        .then(setResults)
        .catch((e) => setError(errMsg(e)));
    }, 180);
    return () => clearTimeout(handle);
  }, [open, query]);

  async function create() {
    if (selected.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const room =
        mode === "direct"
          ? await chat.createDirect(selected[0].id)
          : await chat.createGroup(
              title.trim() || selected.map((u) => displayName(u)).join(", "),
              selected.map((u) => u.id),
            );
      onCreated(room);
      setQuery("");
      setTitle("");
      setSelected([]);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  function toggleUser(u: ChatUser) {
    if (mode === "direct") {
      setSelected([u]);
      return;
    }
    setSelected((prev) =>
      prev.some((x) => x.id === u.id)
        ? prev.filter((x) => x.id !== u.id)
        : [...prev, u],
    );
  }

  return (
    <Modal open={open} onClose={onClose} title={t("chat.newRoom")}>
      <div className="space-y-4">
        <div className="grid grid-cols-2 gap-2">
          <Button
            variant={mode === "direct" ? "default" : "outline"}
            onClick={() => setMode("direct")}
          >
            {t("chat.direct")}
          </Button>
          <Button
            variant={mode === "group" ? "default" : "outline"}
            onClick={() => setMode("group")}
          >
            {t("chat.group")}
          </Button>
        </div>
        {mode === "group" && (
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("chat.groupTitle")}
          />
        )}
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("chat.searchUsers")}
        />
        {selected.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {selected.map((u) => (
              <button
                key={u.id}
                className="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs"
                onClick={() =>
                  setSelected((prev) => prev.filter((x) => x.id !== u.id))
                }
              >
                {displayName(u)}
                <X className="h-3 w-3" />
              </button>
            ))}
          </div>
        )}
        <div className="max-h-64 overflow-y-auto rounded-md border">
          {results.map((u) => (
            <button
              key={u.id}
              className="flex w-full items-center gap-2 border-b p-2 text-left last:border-b-0 hover:bg-accent"
              onClick={() => toggleUser(u)}
            >
              <UserAvatar user={u} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">
                  {displayName(u)}
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {u.email}
                </div>
              </div>
              {selected.some((x) => x.id === u.id) && (
                <CheckCheck className="h-4 w-4 text-primary" />
              )}
            </button>
          ))}
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button onClick={create} disabled={busy || selected.length === 0}>
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {t("chat.create")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function RenameModal({
  room,
  open,
  onClose,
  onRenamed,
}: {
  room: ChatRoom;
  open: boolean;
  onClose: () => void;
  onRenamed: (room: ChatRoom) => void;
}) {
  const { t } = useTranslation();
  const [title, setTitle] = useState(room.title);
  const [busy, setBusy] = useState(false);

  useEffect(() => setTitle(room.title), [room.title]);

  async function save() {
    if (!title.trim()) return;
    setBusy(true);
    try {
      onRenamed(await chat.renameRoom(room.id, title));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t("chat.rename")}>
      <div className="space-y-4">
        <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button onClick={save} disabled={busy || !title.trim()}>
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {t("common.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function UserAvatar({ user, group }: { user?: ChatUser; group?: boolean }) {
  const label = group ? "G" : initials(user);
  return (
    <Avatar className="h-9 w-9">
      {user?.avatar_url && (
        <AvatarImage src={user.avatar_url} alt={displayName(user)} />
      )}
      <AvatarFallback>{label}</AvatarFallback>
    </Avatar>
  );
}

function upsertRoom(rooms: ChatRoom[], room: ChatRoom) {
  return moveRoomToTop(
    rooms.some((r) => r.id === room.id)
      ? rooms.map((r) => (r.id === room.id ? { ...r, ...room } : r))
      : [room, ...rooms],
    room.id,
  );
}

function moveRoomToTop(rooms: ChatRoom[], roomID: string) {
  const idx = rooms.findIndex((r) => r.id === roomID);
  if (idx <= 0) return rooms;
  const next = [...rooms];
  const [room] = next.splice(idx, 1);
  next.unshift(room);
  return next;
}

function mergeMessage(
  messages: DraftMessage[],
  msg: ChatMessage,
): DraftMessage[] {
  if (messages.some((m) => m.id === msg.id)) return messages;
  return [...messages, msg].sort((a, b) => a.seq - b.seq);
}

function displayName(user?: Pick<ChatUser, "display_name" | "email">) {
  return user?.display_name || user?.email || "Unknown";
}

function initials(user?: ChatUser) {
  const name = displayName(user);
  return name
    .split(/\s+/)
    .map((part) => part[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

function roomKindLabel(kind: ChatRoom["kind"], t: (key: string) => string) {
  return kind === "direct" ? t("chat.direct") : t("chat.group");
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function errMsg(e: unknown) {
  if (isApiError(e)) return e.error_description ?? e.error;
  return e instanceof Error ? e.message : String(e);
}
