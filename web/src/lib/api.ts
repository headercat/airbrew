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
  health: string;
  status_message: string;
  settings_path: string;
  dependencies: string[];
  disable_impact: string;
};

export type AdminUser = {
  id: string;
  email: string;
  display_name: string;
  status: string;
  role: string;
  created_at: string;
  deleted_at?: string;
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
  actor_client_id: string;
  actor_email: string;
  event_type: string;
  target_type: string;
  target_id: string;
  ip_address: string;
  user_agent: string;
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

export type PasswordPolicy = {
  min_length: number;
  require_uppercase: boolean;
  require_lowercase: boolean;
  require_digit: boolean;
  require_symbol: boolean;
  max_age_days: number;
  history_count: number;
};

export type IPAllowlist = {
  enabled: boolean;
  cidrs: string[];
  trusted_proxies: string[];
};

export type LoginAttempt = {
  id: string;
  user_id: string;
  email: string;
  success: boolean;
  ip_address: string;
  user_agent: string;
  failure?: string;
  created_at: string;
};

export type OAuthClient = {
  id: string;
  client_id: string;
  name: string;
  client_type: "public" | "confidential";
  token_endpoint_auth_method:
    "none" | "client_secret_basic" | "client_secret_post";
  allowed_scopes: string[];
  redirect_uris: string[];
  post_logout_redirect_uris: string[];
  is_first_party: boolean;
  require_consent: boolean;
  is_active: boolean;
  created_at: string;
  updated_at: string;
};

export type Branding = {
  workspace_name: string;
  logo_url: string;
  primary_color: string;
};

export type SystemInfo = {
  version: string;
  go_version: string;
  started_at: string;
  uptime_seconds: number;
  num_cpu: number;
  user_count: number;
  modules_enabled: number;
  modules_total: number;
  active_sessions: number;
  db_path: string;
  data_dir: string;
  db_size_mb: string;
  disk_free_bytes: number;
  disk_total_bytes: number;
  migration_version: string;
};

export type BackupVerification = {
  ok: boolean;
  integrity_check: string;
  quick_check: string;
  schema_check: string;
  size_bytes: number;
  sha256: string;
  generated_at: string;
  migration_version: string;
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
  del: <T>(path: string) => request<T>(path, { method: "DELETE" }),
  upload: async <T>(
    path: string,
    file: File,
    fieldName = "file",
  ): Promise<T> => {
    const fd = new FormData();
    fd.append(fieldName, file);
    const res = await fetch(path, {
      method: "POST",
      body: fd,
      credentials: "same-origin",
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
  },
};

export function isApiError(e: unknown): e is ApiError {
  return typeof e === "object" && e !== null && "error" in e && "status" in e;
}
