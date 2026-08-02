import { api, type ModuleStatus } from "@/lib/api";

export type ContactValue = {
  value: string;
  type?: string;
};

export type ContactAddress = {
  type?: string;
  street?: string;
  locality?: string;
  region?: string;
  postal_code?: string;
  country?: string;
};

export type ContactRecord = {
  id: string;
  name_prefix: string;
  given_name: string;
  middle_name: string;
  family_name: string;
  name_suffix: string;
  display_name: string;
  nickname: string;
  company: string;
  title: string;
  department: string;
  emails: ContactValue[];
  phones: ContactValue[];
  addresses: ContactAddress[];
  ims: ContactValue[];
  urls: ContactValue[];
  birthday?: string;
  notes: string;
  avatar_url?: string;
  is_favorite: boolean;
  group_ids: string[];
  created_at: string;
  updated_at: string;
};

export type ContactGroup = {
  id: string;
  name: string;
  color?: string;
  count: number;
  created_at: string;
  updated_at: string;
};

export type ContactPayload = {
  name_prefix?: string;
  given_name?: string;
  middle_name?: string;
  family_name?: string;
  name_suffix?: string;
  display_name?: string;
  nickname?: string;
  company?: string;
  title?: string;
  department?: string;
  emails?: ContactValue[];
  phones?: ContactValue[];
  addresses?: ContactAddress[];
  ims?: ContactValue[];
  urls?: ContactValue[];
  birthday?: string;
  notes?: string;
  is_favorite?: boolean;
  group_ids?: string[];
};

export type ContactListArgs = {
  q?: string;
  group?: string;
  favorite?: boolean;
  sort?: string;
  order?: "asc" | "desc";
  limit?: number;
  offset?: number;
};

function query(args: ContactListArgs = {}) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(args)) {
    if (value === undefined || value === "" || value === false) continue;
    params.set(key, String(value));
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin", ...init });
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw {
      error: body?.error ?? "http_error",
      error_description: body?.error_description ?? res.statusText,
      status: res.status,
    };
  }
  return body as T;
}

export const contacts = {
  status: () => api.get<ModuleStatus>("/api/contacts/status"),
  list: (args?: ContactListArgs) =>
    api.get<{ contacts: ContactRecord[] }>(`/api/contacts${query(args)}`),
  create: (body: ContactPayload) =>
    api.post<ContactRecord>("/api/contacts", body),
  update: (id: string, body: ContactPayload) =>
    api.putRaw<ContactRecord>(`/api/contacts/${id}`, body),
  patch: (id: string, body: ContactPayload) =>
    api.patch<ContactRecord>(`/api/contacts/${id}`, body),
  remove: (id: string) => api.del<{ ok: boolean }>(`/api/contacts/${id}`),
  uploadAvatar: (id: string, file: File) =>
    api.upload<ContactRecord>(`/api/contacts/${id}/avatar`, file),
  clearAvatar: (id: string) =>
    api.del<ContactRecord>(`/api/contacts/${id}/avatar`),
  groups: () => api.get<{ groups: ContactGroup[] }>("/api/contacts/groups"),
  createGroup: (body: { name: string; color?: string }) =>
    api.post<ContactGroup>("/api/contacts/groups", body),
  updateGroup: (id: string, body: { name: string; color?: string }) =>
    api.patch<ContactGroup>(`/api/contacts/groups/${id}`, body),
  removeGroup: (id: string) =>
    api.del<{ ok: boolean }>(`/api/contacts/groups/${id}`),
  importVCF: (file: File) => {
    return fetchJSON<{ imported: number }>("/api/contacts/import", {
      method: "POST",
      body: file,
      headers: { "Content-Type": file.type || "text/vcard" },
    });
  },
  exportVCF: async () => {
    const res = await fetch("/api/contacts/export", {
      credentials: "same-origin",
    });
    if (!res.ok) {
      throw {
        error: "http_error",
        error_description: res.statusText,
        status: res.status,
      };
    }
    return URL.createObjectURL(await res.blob());
  },
};
