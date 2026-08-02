// Typed client for the drive module. DTO field names mirror the Go JSON tags.
import { api } from "@/lib/api";

export type DriveNode = {
  id: string;
  parent_id: string;
  kind: "file" | "folder";
  name: string;
  content_type: string;
  size_bytes: number;
  sha256?: string;
  is_starred: boolean;
  deleted_at?: string;
  created_at: string;
  updated_at: string;
};

export type DriveShare = {
  id: string;
  node_id: string;
  node_name: string;
  node_trashed: boolean;
  token: string;
  url: string;
  has_password: boolean;
  expires_at?: string;
  downloads: number;
  is_active: boolean;
  created_at: string;
};

export type DriveStatus = {
  module: string;
  status: string;
  enabled: boolean;
  max_upload_bytes: number;
  quota_bytes: number;
};

export type DriveUsage = { used: number; quota: number };

export type DriveShareMeta = {
  name: string;
  content_type: string;
  size_bytes: number;
  expires_at?: string;
};

export type ListParams = {
  parent?: string;
  folder?: string;
  view?: string;
  q?: string;
  kind?: string;
  sort?: string;
  order?: string;
  limit?: number;
  offset?: number;
};

export const drive = {
  status: () => api.get<DriveStatus>("/api/drive/status"),
  list: (p: ListParams = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(p)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    const qs = q.toString();
    return api.get<{ nodes: DriveNode[] }>(
      "/api/drive/files" + (qs ? "?" + qs : ""),
    );
  },
  get: (id: string) => api.get<DriveNode>(`/api/drive/files/${id}`),
  createFolder: (name: string, parent = "") =>
    api.post<DriveNode>("/api/drive/folders", { name, parent_id: parent }),
  upload: (file: File, parent = "", name?: string) => {
    const fd = new FormData();
    fd.append("file", file);
    const q = new URLSearchParams();
    if (parent) q.set("parent", parent);
    if (name) q.set("name", name);
    const qs = q.toString();
    return fetch("/api/drive/files" + (qs ? "?" + qs : ""), {
      method: "POST",
      body: fd,
      credentials: "same-origin",
    }).then(async (res) => {
      const text = await res.text();
      const body = text ? JSON.parse(text) : null;
      if (!res.ok) throw body ?? { error: "http_error", status: res.status };
      return body as DriveNode;
    });
  },
  patch: (
    id: string,
    body: { name?: string; parent_id?: string; starred?: boolean },
  ) => api.patch<DriveNode>(`/api/drive/files/${id}`, body),
  remove: (id: string, permanent = false) =>
    api.del<{ ok: boolean }>(
      `/api/drive/files/${id}${permanent ? "?permanent=true" : ""}`,
    ),
  restore: (id: string) =>
    api.post<{ ok: boolean }>(`/api/drive/files/${id}/restore`),
  copy: (id: string, parent: string, name?: string) =>
    api.post<DriveNode>(`/api/drive/files/${id}/copy`, {
      parent_id: parent,
      name: name ?? "",
    }),
  downloadURL: (id: string, inline = false) =>
    `/api/drive/files/${id}/download${inline ? "?inline=true" : ""}`,
  emptyTrash: () => api.post<{ ok: boolean }>("/api/drive/trash/empty"),
  usage: () => api.get<DriveUsage>("/api/drive/usage"),

  listShares: () => api.get<{ shares: DriveShare[] }>("/api/drive/shares"),
  listNodeShares: (id: string) =>
    api.get<{ shares: DriveShare[] }>(`/api/drive/files/${id}/shares`),
  createShare: (
    id: string,
    body: { password?: string; expires_in_seconds?: number },
  ) => api.post<DriveShare>(`/api/drive/files/${id}/shares`, body),
  deleteShare: (id: string) =>
    api.del<{ ok: boolean }>(`/api/drive/shares/${id}`),

  // Public share access (no auth). The password is sent via the
  // X-Share-Password header, never the URL query string, so it does not leak
  // into browser history, referrers, or server logs.
  shareMeta: (token: string, password?: string) =>
    fetch(`/api/drive/s/${token}`, {
      headers: password ? { "X-Share-Password": password } : {},
      credentials: "same-origin",
    }).then(async (res) => {
      const text = await res.text();
      const body = text ? JSON.parse(text) : null;
      if (!res.ok) throw body ?? { error: "http_error", status: res.status };
      return body as DriveShareMeta;
    }),
  // Fetches the shared file with the password header and resolves to an
  // object URL the caller can hand to an <a download> (revoked after use).
  shareDownload: async (token: string, password?: string) => {
    const res = await fetch(`/api/drive/s/${token}/download`, {
      headers: password ? { "X-Share-Password": password } : {},
      credentials: "same-origin",
    });
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      const body = text ? JSON.parse(text) : null;
      throw body ?? { error: "http_error", status: res.status };
    }
    const blob = await res.blob();
    return URL.createObjectURL(blob);
  },
};
