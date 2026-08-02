// Typed client for the /api/vault endpoints. Mirrors the Go handler request
// and response shapes exactly (see internal/passwords/handler/handler.go).
// All payloads are ciphertext produced by lib/vault/crypto — the client never
// sends plaintext to the server.

import { api, isApiError, type ApiError } from "@/lib/api";

export type VaultItemType = "login" | "secure_note" | "card" | "identity";

export type Envelope = {
  kdf_algorithm: string;
  kdf_salt: string;
  kdf_memory_kib: number;
  kdf_iterations: number;
  kdf_parallelism: number;
  protected_vault_key: string;
  protected_vault_nonce: string;
  version: number;
  updated_at: string;
};

export type VaultFolder = {
  id: string;
  name_cipher: string;
  name_nonce: string;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at: string | null;
};

export type VaultItem = {
  id: string;
  type: VaultItemType;
  folder_id: string;
  name_cipher: string;
  name_nonce: string;
  data_cipher: string;
  data_nonce: string;
  notes_cipher: string;
  notes_nonce: string;
  favorite: boolean;
  reprompt: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at: string | null;
};

export type SyncResponse = {
  cursor: number;
  folders: VaultFolder[];
  items: VaultItem[];
  has_more?: boolean;
};

export type EnvelopeInput = {
  kdf_algorithm: string;
  kdf_salt: string;
  kdf_memory_kib: number;
  kdf_iterations: number;
  kdf_parallelism: number;
  protected_vault_key: string;
  protected_vault_nonce: string;
  if_version?: number;
};

export type ItemInput = {
  type: VaultItemType;
  folder_id: string;
  name_cipher: string;
  name_nonce: string;
  data_cipher: string;
  data_nonce: string;
  notes_cipher: string;
  notes_nonce: string;
  favorite: boolean;
  reprompt: boolean;
  if_revision?: number;
};

// Conflict is returned on a 409 when the client's if_revision/if_version is
// stale. The server attaches its current row so the client can merge.
export type Conflict = {
  error: "conflict";
  error_description: string;
  current: VaultItem | VaultFolder | Envelope;
};

export function isConflict(e: unknown): e is Conflict {
  return (
    isApiError(e) && (e as ApiError & Partial<Conflict>).error === "conflict"
  );
}

// getKeys fetches the envelope for unlock. Rejects with status 404 when the
// vault has not been set up yet (caller treats that as the setup state).
export function getKeys(): Promise<Envelope> {
  return api.get<Envelope>("/api/vault/keys");
}

export function setup(input: EnvelopeInput): Promise<{ ok: true }> {
  return api.post("/api/vault/setup", input);
}

export function rotateKeys(input: EnvelopeInput): Promise<{ ok: true }> {
  return api.post("/api/vault/keys/rotate", input);
}

export function sync(since: number, limit = 500): Promise<SyncResponse> {
  return api.get<SyncResponse>(
    `/api/vault/sync?since=${since}&limit=${limit}`,
  );
}

export function createItem(input: ItemInput): Promise<VaultItem> {
  return api.post<VaultItem>("/api/vault/items", input);
}

export function updateItem(id: string, input: ItemInput): Promise<VaultItem> {
  return api.putRaw<VaultItem>(`/api/vault/items/${id}`, input);
}

export function deleteItem(
  id: string,
  ifRevision: number,
): Promise<{ ok: true }> {
  return api.del(`/api/vault/items/${id}?if_revision=${ifRevision}`);
}

export function createFolder(
  nameCipher: string,
  nameNonce: string,
): Promise<VaultFolder> {
  return api.post<VaultFolder>("/api/vault/folders", {
    name_cipher: nameCipher,
    name_nonce: nameNonce,
  });
}

// ---- attachments ----

export type AttachmentMeta = {
  id: string;
  name_cipher: string;
  name_nonce: string;
  file_key_cipher: string;
  file_key_nonce: string;
  size_bytes: number;
  created_at: string;
};

export function listAttachments(itemId: string): Promise<AttachmentMeta[]> {
  return api
    .get<{ attachments: AttachmentMeta[] }>(
      `/api/vault/items/${itemId}/attachments`,
    )
    .then((r) => r.attachments ?? []);
}

export function deleteAttachment(itemId: string, aid: string): Promise<void> {
  return api
    .del(`/api/vault/items/${itemId}/attachments/${aid}`)
    .then(() => undefined);
}

// fetchAttachmentBlob returns the raw encrypted attachment payload.
export async function fetchAttachmentBlob(
  itemId: string,
  aid: string,
): Promise<Uint8Array> {
  const res = await fetch(`/api/vault/items/${itemId}/attachments/${aid}`, {
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw {
      error: "http_error",
      error_description: res.statusText,
      status: res.status,
    } as ApiError;
  }
  return new Uint8Array(await res.arrayBuffer());
}

// uploadAttachment posts the multipart form (encrypted file + wrapped key +
// encrypted name) and returns the stored metadata.
export async function uploadAttachment(
  itemId: string,
  sealed: Uint8Array,
  meta: {
    fileKeyCipher: string;
    fileKeyNonce: string;
    nameCipher: string;
    nameNonce: string;
    sizeBytes: number;
  },
): Promise<AttachmentMeta> {
  const fd = new FormData();
  fd.append("file", new Blob([sealed as unknown as BlobPart]));
  fd.append("file_key_cipher", meta.fileKeyCipher);
  fd.append("file_key_nonce", meta.fileKeyNonce);
  fd.append("name_cipher", meta.nameCipher);
  fd.append("name_nonce", meta.nameNonce);
  fd.append("size_bytes", String(meta.sizeBytes));
  const res = await fetch(`/api/vault/items/${itemId}/attachments`, {
    method: "POST",
    body: fd,
    credentials: "same-origin",
  });
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw {
      error: body?.error ?? "http_error",
      error_description: body?.error_description ?? res.statusText,
      status: res.status,
    } as ApiError;
  }
  return body as AttachmentMeta;
}

// ---- item history ----

export type ItemRevision = {
  id: string;
  name_cipher: string;
  name_nonce: string;
  data_cipher: string;
  data_nonce: string;
  revision: number;
  created_at: string;
};

export function listRevisions(itemId: string): Promise<ItemRevision[]> {
  return api
    .get<{ revisions: ItemRevision[] }>(
      `/api/vault/items/${itemId}/revisions`,
    )
    .then((r) => r.revisions ?? []);
}

export function restoreRevision(
  itemId: string,
  revId: string,
  ifRevision: number,
): Promise<VaultItem> {
  return api.post<VaultItem>(
    `/api/vault/items/${itemId}/revisions/${revId}/restore?if_revision=${ifRevision}`,
  );
}

// ---- export / import ----

export type ExportBundle = {
  envelope: Envelope;
  folders: VaultFolder[];
  items: VaultItem[];
};

export function exportVault(): Promise<ExportBundle> {
  return api.get<ExportBundle>("/api/vault/export");
}

export type ImportCounts = { folders: number; items: number };

export function importVault(
  folders: { name_cipher: string; name_nonce: string }[],
  items: ItemInput[],
): Promise<ImportCounts> {
  return api.post<ImportCounts>("/api/vault/import", { folders, items });
}

