// Thin fetch wrapper that talks to the Go backend and surfaces API errors
// in a typed, predictable shape.

export type ApiError = {
  error: string;
  error_description?: string;
  status: number;
};

export type User = {
  id: string;
  email: string;
  display_name: string;
  description: string;
  birthday: string; // YYYY-MM-DD or ""
  phone_number: string;
  avatar_url: string;
  status: string;
  role: "user" | "admin" | string;
  custom_fields: Record<string, string>;
};

export type ModuleStatus = {
  module: string;
  status: string;
  enabled?: string;
};

// ---- Admin ----

export type AdminModule = {
  key: string;
  name: string;
  description: string;
  admin_only: boolean;
  system: boolean;
  enabled: boolean;
};

export type AdminUser = {
  id: string;
  email: string;
  display_name: string;
  status: string;
  role: string;
  created_at: string;
};

export type AdminDashboard = {
  users: number;
  admins: number;
  active_sessions: number;
  modules_total: number;
  modules_enabled: number;
  recent_events: AuditEntry[];
};

export type AuditEntry = {
  id: string;
  actor_user_id: string;
  actor_email: string;
  event_type: string;
  target_type: string;
  target_id: string;
  ip_address: string;
  metadata: string;
  created_at: string;
};

export type AdminSession = {
  id: string;
  user_id: string;
  user_email: string;
  ip_address: string;
  user_agent: string;
  created_at: string;
  expires_at: string;
};

export type Branding = {
  workspace_name: string;
  logo_url: string;
  primary_color: string;
};

export type SystemInfo = {
  version: string;
  go_version: string;
  num_cpu: number;
  user_count: number;
  modules_enabled: number;
  modules_total: number;
  active_sessions: number;
  db_path: string;
  db_size_mb: string;
};

export type ProfileUpdate = {
  display_name: string;
  description: string;
  birthday: string;
  phone_number: string;
  custom_fields: Record<string, string>;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    credentials: "same-origin",
    ...init,
  });

  const text = await res.text();
  const body = text ? JSON.parse(text) : null;

  if (!res.ok) {
    const err: ApiError = {
      error: body?.error ?? "http_error",
      error_description: body?.error_description ?? res.statusText,
      status: res.status,
    };
    throw err;
  }

  return body as T;
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: "POST",
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  patch: <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: "PATCH",
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  putRaw: <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: "PUT",
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  del: <T>(path: string) =>
    request<T>(path, { method: "DELETE" }),
  upload: async <T>(path: string, file: File, fieldName = "file"): Promise<T> => {
    const fd = new FormData();
    fd.append(fieldName, file);
    const res = await fetch(path, { method: "POST", body: fd, credentials: "same-origin" });
    const text = await res.text();
    const body = text ? JSON.parse(text) : null;
    if (!res.ok) {
      const err: ApiError = {
        error: body?.error ?? "http_error",
        error_description: body?.error_description ?? res.statusText,
        status: res.status,
      };
      throw err;
    }
    return body as T;
  },
};

export function isApiError(e: unknown): e is ApiError {
  return typeof e === "object" && e !== null && "error" in e && "status" in e;
}
