import { api } from "@/lib/api";

export type MailAddress = {
  name?: string;
  address: string;
};

export type Mailbox = {
  id: string;
  address: string;
  local_part: string;
  domain: string;
  display_name: string;
  is_primary: boolean;
  created_at: string;
};

export type MailAttachment = {
  id: string;
  message_id?: string;
  filename: string;
  content_type: string;
  content_id?: string;
  inline: boolean;
  size_bytes: number;
  created_at: string;
  download_url: string;
};

export type MailMessage = {
  id: string;
  mailbox_id: string;
  message_id: string;
  thread_id: string;
  in_reply_to: string;
  references: string[];
  subject: string;
  from: MailAddress;
  to: MailAddress[];
  cc: MailAddress[];
  bcc: MailAddress[];
  reply_to: MailAddress[];
  direction: "inbound" | "outbound" | string;
  body_text: string;
  body_html: string;
  is_read: boolean;
  is_starred: boolean;
  is_draft: boolean;
  size_bytes: number;
  received_at?: string;
  sent_at?: string;
  created_at: string;
  attachments?: MailAttachment[];
};

export type MailThread = {
  thread_id: string;
  subject: string;
  from: MailAddress;
  last_at: string;
  count: number;
  unread_count: number;
};

export type SendMailInput = {
  mailbox_id: string;
  to: MailAddress[];
  cc?: MailAddress[];
  bcc?: MailAddress[];
  reply_to?: MailAddress[];
  subject: string;
  text: string;
  html?: string;
  in_reply_to?: string;
  references?: string[];
  attachment_ids?: string[];
};

export const mail = {
  status: () =>
    api.get<{
      module: string;
      status: string;
      enabled: boolean;
      inbound_drivers: string[];
      outbound_drivers: string[];
    }>("/api/mail/status"),

  mailboxes: () => api.get<{ mailboxes: Mailbox[] }>("/api/mail/mailboxes"),

  createMailbox: (body: {
    address: string;
    display_name?: string;
    is_primary?: boolean;
  }) => api.post<Mailbox>("/api/mail/mailboxes", body),

  threads: (args: { mailbox?: string; limit?: number; offset?: number }) => {
    const qs = new URLSearchParams();
    if (args.mailbox) qs.set("mailbox", args.mailbox);
    if (args.limit) qs.set("limit", String(args.limit));
    if (args.offset) qs.set("offset", String(args.offset));
    return api.get<{ threads: MailThread[] }>(
      `/api/mail/threads${qs.size ? `?${qs}` : ""}`,
    );
  },

  messages: (args: {
    mailbox?: string;
    folder?: string;
    thread?: string;
    limit?: number;
    offset?: number;
  }) => {
    const qs = new URLSearchParams();
    if (args.mailbox) qs.set("mailbox", args.mailbox);
    if (args.folder) qs.set("folder", args.folder);
    if (args.thread) qs.set("thread", args.thread);
    if (args.limit) qs.set("limit", String(args.limit));
    if (args.offset) qs.set("offset", String(args.offset));
    return api.get<{ messages: MailMessage[] }>(
      `/api/mail/messages${qs.size ? `?${qs}` : ""}`,
    );
  },

  message: (id: string) => api.get<MailMessage>(`/api/mail/messages/${id}`),

  patchMessage: (
    id: string,
    body: { is_read?: boolean; is_starred?: boolean },
  ) => api.patch<{ ok: boolean }>(`/api/mail/messages/${id}`, body),

  deleteMessage: (id: string) =>
    api.del<{ ok: boolean }>(`/api/mail/messages/${id}`),

  uploadAttachment: (file: File) =>
    api.upload<MailAttachment>("/api/mail/attachments", file),

  deleteAttachment: (id: string) =>
    api.del<{ ok: boolean }>(`/api/mail/attachments/${id}`),

  send: (body: SendMailInput) => api.post<MailMessage>("/api/mail/send", body),
};

export function formatAddress(a?: MailAddress) {
  if (!a) return "";
  return a.name ? `${a.name} <${a.address}>` : a.address;
}

export function parseAddressList(input: string): MailAddress[] {
  return input
    .split(/[,\n;]/)
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => {
      const match = part.match(/^(.*?)<([^>]+)>$/);
      if (match) {
        return {
          name: match[1].trim().replace(/^"|"$/g, ""),
          address: match[2].trim(),
        };
      }
      return { address: part };
    });
}
