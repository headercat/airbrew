// Vault store: the client-side state machine that ties crypto + API together.
//
// Lifecycle: bootstrap -> (not_setup | locked) -> unlocked. The vault key is
// held only in a ref (never in React state, never persisted) so it clears on
// lock or tab close. All item fields are decrypted in memory for search and
// re-encrypted before any network write.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import {
  b64ToBytes,
  bytesToB64,
  decryptBytes,
  decryptString,
  deriveMasterKey,
  encryptBytes,
  encryptString,
  generateSalt,
  generateVaultKey,
  open,
  randomBytes,
  seal,
  DEFAULT_KDF_PARAMS,
  type KdfParams,
} from "@/lib/vault/crypto";
import {
  getVaultCache,
  putVaultCache,
  clearVaultCache,
  userIdForEnvelope,
} from "@/lib/vault/cache";
import { readVaultSecuritySettings } from "@/lib/vault/security";
import type { CSVParsedItem } from "@/lib/vault/csv";
import * as VApi from "@/lib/vault/api";
import type {
  AttachmentMeta,
  Envelope,
  ItemInput,
  VaultItem,
  VaultItemType,
} from "@/lib/vault/api";

// ---- decrypted client models ----

// FieldKind controls how a field is rendered (input type) and interpreted
// (e.g. "totp" derives a rotating code). The item "type" only picks a preset
// of these fields; the user can add/rename/remove any field freely.
export type FieldKind = "text" | "password" | "totp" | "url" | "multiline";

export type Field = {
  id: string;
  name: string;
  value: string;
  kind: FieldKind;
};

// ItemData is the JSON encrypted into data_cipher. It only carries fields so
// any combination of custom fields round-trips.
export type ItemData = { fields: Field[] };

export type DecryptedItem = {
  id: string;
  type: VaultItemType;
  folderId: string;
  name: string;
  notes: string;
  fields: Field[];
  favorite: boolean;
  reprompt: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
};

export type DecryptedFolder = {
  id: string;
  name: string;
  revision: number;
};

// TrashEntry is a unified view of a soft-deleted folder or item. "kind"
// distinguishes them so the trash UI can render and route restore/purge calls
// without re-importing the API types.
export type TrashEntry = {
  id: string;
  kind: "folder" | "item";
  name: string;
  type?: VaultItemType;
  revision: number;
  deletedAt: string;
};

// Attachment is the decrypted client view: the name is decrypted with the
// vault key, while fileKeyCipher/Nonce stay wrapped until download (when the
// file key is unwrapped and used to decrypt the blob).
export type Attachment = {
  id: string;
  name: string;
  size: number;
  fileKeyCipher: string;
  fileKeyNonce: string;
  cryptoVersion?: number;
  createdAt: string;
};

export type VaultStatus = "unknown" | "not_setup" | "locked" | "unlocked";

function fieldId(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();

  const bytes = randomBytes(16);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join(
    "",
  );
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(
    12,
    16,
  )}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// newField creates a blank field with a stable client-side id.
export function newField(
  kind: FieldKind = "text",
  name = "",
  value = "",
): Field {
  return { id: fieldId(), name, value, kind };
}

// PRESETS seed the field list when an item type is chosen. They are starting
// points only — the user can edit them however they like afterwards.
export const PRESETS: Record<VaultItemType, Field[]> = {
  login: [
    newField("text", "Username"),
    newField("password", "Password"),
    newField("url", "Website"),
    newField("totp", "TOTP"),
  ],
  card: [
    newField("text", "Cardholder"),
    newField("password", "Card number"),
    newField("text", "Expiry"),
    newField("password", "CVV"),
  ],
  identity: [
    newField("text", "Full name"),
    newField("text", "Email"),
    newField("text", "Phone"),
  ],
  secure_note: [],
};

// migrateLegacyData converts the original rigid LoginData shape into the
// flexible fields[] model so items created before this change keep rendering.
function migrateLegacyData(raw: unknown): Field[] {
  if (!raw || typeof raw !== "object") return [];
  const r = raw as Record<string, unknown>;
  const fields: Field[] = [];
  const push = (kind: FieldKind, name: string, value: unknown) => {
    if (value !== undefined && value !== null && value !== "") {
      const v = Array.isArray(value) ? value[0] : value;
      if (typeof v === "string") fields.push(newField(kind, name, v));
    }
  };
  push("text", "Username", r.username);
  push("password", "Password", r.password);
  push("url", "Website", r.uris);
  push("totp", "TOTP", r.totp);
  return fields;
}

// WrongMasterPassword is thrown by unlock/unwrap when the AES-GCM auth tag
// fails to verify — the only signal that the master password was wrong.
export class WrongMasterPassword extends Error {
  constructor() {
    super("incorrect master password");
    this.name = "WrongMasterPassword";
  }
}

// ---- crypto <-> api glue ----

const AAD = {
  envelope: "airbrew:vault:envelope-key:v1",
  folderName: "airbrew:vault:folder-name:v1",
  itemName: "airbrew:vault:item-name:v1",
  itemData: "airbrew:vault:item-data:v1",
  itemNotes: "airbrew:vault:item-notes:v1",
  attachmentName: "airbrew:vault:attachment-name:v1",
  attachmentFileKey: "airbrew:vault:attachment-file-key:v1",
  attachmentPayload: "airbrew:vault:attachment-payload:v1",
} as const;

const RECORD_AAD_CRYPTO_VERSION = 3;
const CURRENT_CRYPTO_VERSION = RECORD_AAD_CRYPTO_VERSION;

function isLegacyCrypto(version?: number): boolean {
  return (version ?? 1) < RECORD_AAD_CRYPTO_VERSION;
}

function recordAAD(kind: string, id: string, field: string): string {
  return `airbrew:vault:${kind}:${id}:${field}:v3`;
}

function folderAAD(id: string, field: "name", version?: number): string {
  if ((version ?? 1) >= RECORD_AAD_CRYPTO_VERSION) {
    return recordAAD("folder", id, field);
  }
  return AAD.folderName;
}

function itemAAD(
  id: string,
  field: "name" | "data" | "notes",
  version?: number,
): string {
  if ((version ?? 1) >= RECORD_AAD_CRYPTO_VERSION) {
    return recordAAD("item", id, field);
  }
  if (field === "name") return AAD.itemName;
  if (field === "data") return AAD.itemData;
  return AAD.itemNotes;
}

function attachmentAAD(
  id: string,
  field: "name" | "file-key" | "payload",
  version?: number,
): string {
  if ((version ?? 1) >= RECORD_AAD_CRYPTO_VERSION) {
    return recordAAD("attachment", id, field);
  }
  if (field === "name") return AAD.attachmentName;
  if (field === "file-key") return AAD.attachmentFileKey;
  return AAD.attachmentPayload;
}

const ID_ALPHABET =
  "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

function vaultId(): string {
  const bytes = randomBytes(21);
  let out = "";
  for (const b of bytes) out += ID_ALPHABET[b & 63];
  return out;
}

async function decryptStringCompat(
  key: Uint8Array,
  cipher: string,
  nonce: string,
  aad: string,
  cryptoVersion?: number,
): Promise<string> {
  try {
    return await decryptString(key, cipher, nonce, aad);
  } catch {
    if (!isLegacyCrypto(cryptoVersion))
      throw new Error("AAD verification failed");
    return decryptString(key, cipher, nonce);
  }
}

async function decryptBytesCompat(
  key: Uint8Array,
  cipher: string,
  nonce: string,
  aad: string,
  cryptoVersion?: number,
): Promise<Uint8Array> {
  try {
    return await decryptBytes(key, cipher, nonce, aad);
  } catch {
    if (!isLegacyCrypto(cryptoVersion))
      throw new Error("AAD verification failed");
    return decryptBytes(key, cipher, nonce);
  }
}

async function openCompat(
  key: Uint8Array,
  sealed: Uint8Array,
  aad: string,
  cryptoVersion?: number,
): Promise<Uint8Array> {
  try {
    return await open(key, sealed, aad);
  } catch {
    if (!isLegacyCrypto(cryptoVersion))
      throw new Error("AAD verification failed");
    return open(key, sealed);
  }
}

async function decryptItem(
  key: Uint8Array,
  it: VaultItem,
  options: { includeSensitive?: boolean } = {},
): Promise<DecryptedItem> {
  const name = await decryptStringCompat(
    key,
    it.name_cipher,
    it.name_nonce,
    itemAAD(it.id, "name", it.crypto_version),
    it.crypto_version,
  );
  if (it.reprompt && !options.includeSensitive) {
    return {
      id: it.id,
      type: it.type,
      folderId: it.folder_id,
      name,
      notes: "",
      fields: [],
      favorite: it.favorite,
      reprompt: it.reprompt,
      revision: it.revision,
      createdAt: it.created_at,
      updatedAt: it.updated_at,
    };
  }
  const dataJson = await decryptStringCompat(
    key,
    it.data_cipher,
    it.data_nonce,
    itemAAD(it.id, "data", it.crypto_version),
    it.crypto_version,
  );
  let fields: Field[] = [];
  try {
    const parsed = JSON.parse(dataJson) as Partial<ItemData> &
      Record<string, unknown>;
    if (Array.isArray(parsed.fields)) {
      fields = (parsed.fields as Field[]).map((f) => ({
        id: f.id ?? fieldId(),
        name: f.name ?? "",
        value: f.value ?? "",
        kind: (f.kind as FieldKind) ?? "text",
      }));
    } else {
      fields = migrateLegacyData(parsed);
    }
  } catch {
    fields = [];
  }
  let notes = "";
  if (it.notes_cipher && it.notes_nonce) {
    try {
      notes = await decryptStringCompat(
        key,
        it.notes_cipher,
        it.notes_nonce,
        itemAAD(it.id, "notes", it.crypto_version),
        it.crypto_version,
      );
    } catch {
      notes = "";
    }
  }
  return {
    id: it.id,
    type: it.type,
    folderId: it.folder_id,
    name,
    notes,
    fields,
    favorite: it.favorite,
    reprompt: it.reprompt,
    revision: it.revision,
    createdAt: it.created_at,
    updatedAt: it.updated_at,
  };
}

// undecryptableItem is the placeholder shown when a single ciphertext row
// cannot be decrypted (corrupted data, migration mismatch, tampering). It
// keeps the metadata visible so the user sees the row exists but is not
// readable, without aborting the rest of the sync.
const DECRYPT_FAILED_NAME = "•••";

function undecryptableItem(raw: VaultItem): DecryptedItem {
  return {
    id: raw.id,
    type: raw.type,
    folderId: raw.folder_id,
    name: DECRYPT_FAILED_NAME,
    notes: "",
    fields: [],
    favorite: false,
    reprompt: false,
    revision: raw.revision,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
  };
}

// DraftItem is what the editor produces; encryptItemInput turns it into the
// ciphertext payload the server stores.
export type DraftItem = {
  type: VaultItemType;
  folderId: string;
  name: string;
  notes: string;
  fields: Field[];
  favorite: boolean;
  reprompt: boolean;
};

export async function encryptItemInput(
  key: Uint8Array,
  draft: DraftItem,
  itemId: string,
): Promise<ItemInput> {
  const name = await encryptString(
    key,
    draft.name,
    itemAAD(itemId, "name", CURRENT_CRYPTO_VERSION),
  );
  const data: ItemData = { fields: draft.fields };
  const dataEnc = await encryptString(
    key,
    JSON.stringify(data),
    itemAAD(itemId, "data", CURRENT_CRYPTO_VERSION),
  );
  let notesCipher = "";
  let notesNonce = "";
  if (draft.notes) {
    const notes = await encryptString(
      key,
      draft.notes,
      itemAAD(itemId, "notes", CURRENT_CRYPTO_VERSION),
    );
    notesCipher = notes.cipher;
    notesNonce = notes.nonce;
  }
  return {
    type: draft.type,
    folder_id: draft.folderId,
    name_cipher: name.cipher,
    name_nonce: name.nonce,
    data_cipher: dataEnc.cipher,
    data_nonce: dataEnc.nonce,
    notes_cipher: notesCipher,
    notes_nonce: notesNonce,
    id: itemId,
    crypto_version: CURRENT_CRYPTO_VERSION,
    favorite: draft.favorite,
    reprompt: draft.reprompt,
  };
}

// ---- context ----

type VaultContextValue = {
  status: VaultStatus;
  error: string | null;
  busy: boolean;
  items: DecryptedItem[];
  folders: DecryptedFolder[];
  cursor: number;
  bootstrap: () => Promise<void>;
  setupAndUnlock: (masterPassword: string) => Promise<void>;
  unlock: (masterPassword: string) => Promise<void>;
  lock: () => void;
  refresh: () => Promise<void>;
  createItem: (draft: DraftItem) => Promise<DecryptedItem>;
  updateItem: (
    id: string,
    draft: DraftItem,
    ifRevision: number,
  ) => Promise<DecryptedItem>;
  deleteItem: (id: string, ifRevision: number) => Promise<void>;
  listAttachments: (itemId: string) => Promise<Attachment[]>;
  uploadAttachment: (itemId: string, file: File) => Promise<Attachment>;
  deleteAttachment: (itemId: string, aid: string) => Promise<void>;
  downloadAttachment: (
    itemId: string,
    att: Attachment,
  ) => Promise<{ blob: Blob; name: string }>;
  // verifyMasterPassword re-derives the master key from the stored envelope and
  // a supplied password, decrypts the wrapped vault key, and reports whether it
  // matches the key currently in memory. Used by the "reprompt" feature so a
  // sensitive item is only revealed after re-entering the master password.
  verifyMasterPassword: (password: string) => Promise<boolean>;
  unlockItemDetails: (
    itemId: string,
    password: string,
  ) => Promise<DecryptedItem | null>;
  // exportBundle / importBundle move encrypted backups between vaults by
  // unlocking the source envelope and re-encrypting under the current vault key.
  exportBundle: () => Promise<VApi.ExportBundle>;
  importBundle: (
    bundle: VApi.ExportBundle,
    sourcePassword?: string,
  ) => Promise<VApi.ImportCounts>;
  // Folder CRUD (encrypts the folder name with the vault key).
  createFolder: (name: string) => Promise<DecryptedFolder>;
  renameFolder: (id: string, name: string) => Promise<void>;
  deleteFolder: (id: string) => Promise<void>;
  // changeMasterPassword re-wraps the existing vault key under a new master
  // password and rotates the envelope server-side (version-guarded).
  changeMasterPassword: (
    currentPassword: string,
    newPassword: string,
  ) => Promise<void>;
  // Item history: list decrypted revision snapshots and restore one.
  listItemRevisions: (itemId: string) => Promise<
    {
      id: string;
      name: string;
      type: VaultItemType;
      folderId: string;
      hasNotes: boolean;
      favorite: boolean;
      reprompt: boolean;
      revision: number;
      createdAt: string;
    }[]
  >;
  restoreItemRevision: (itemId: string, revId: string) => Promise<void>;
  // Trash (recycle bin): list soft-deleted rows, restore, or permanently
  // delete. Restoring bumps the sync cursor so the row reappears on every
  // device; purging is invisible (the tombstone was already synced).
  listTrash: () => Promise<TrashEntry[]>;
  restoreTrashItem: (id: string) => Promise<void>;
  restoreTrashFolder: (id: string) => Promise<void>;
  purgeTrashItem: (id: string) => Promise<void>;
  purgeTrashFolder: (id: string) => Promise<void>;
  emptyTrash: () => Promise<void>;
  // importCSVRows turns parsed CSV items (from another password manager) into
  // encrypted vault items one at a time, calling onProgress after each so the
  // UI can show a counter. Returns the number of items created and the number
  // that failed (e.g. network error) so the UI can report skipped rows.
  importCSVRows: (
    items: CSVParsedItem[],
    onProgress?: (done: number, total: number) => void,
  ) => Promise<{ created: number; failed: number }>;
};

const VaultContext = createContext<VaultContextValue | null>(null);

export function VaultProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<VaultStatus>("unknown");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [items, setItems] = useState<DecryptedItem[]>([]);
  const [folders, setFolders] = useState<DecryptedFolder[]>([]);
  const [cursor, setCursor] = useState(0);

  // The vault key lives in a ref — never in React state, never serialized.
  const keyRef = useRef<Uint8Array | null>(null);
  const envelopeRef = useRef<Envelope | null>(null);

  // cursorRef / itemsRef mirror the state so syncAndDecrypt can read the
  // current values synchronously without closing over stale React state (the
  // previous version captured `cursor`/`items` in its useCallback deps and
  // used a stale copy when called right after a setState in the same tick,
  // e.g. on unlock). They are always updated together with their state.
  const cursorRef = useRef(0);
  const itemsRef = useRef<DecryptedItem[]>([]);

  // zeroize wipes a key buffer in place. JS GC is not immediate, but filling
  // the backing ArrayBuffer with zeros is the best-effort mitigation available
  // and removes the key from reachable memory as soon as the vault locks.
  function zeroize(buf: Uint8Array | null) {
    if (buf && buf.buffer instanceof ArrayBuffer) {
      try {
        new Uint8Array(buf.buffer).fill(0);
      } catch {
        /* detached/shared buffer — nothing to do */
      }
    }
  }

  const commitItems = useCallback((next: DecryptedItem[]) => {
    itemsRef.current = next;
    setItems(next);
  }, []);
  const commitCursor = useCallback((next: number) => {
    cursorRef.current = next;
    setCursor(next);
  }, []);

  // Raw ciphertext rows, kept in lockstep with the decrypted cache so the
  // IndexedDB ciphertext cache (see lib/vault/cache.ts) can be written without
  // a second network round-trip. Cleared on lock.
  const rawItemsRef = useRef<Map<string, VApi.VaultItem>>(new Map());
  const rawFoldersRef = useRef<Map<string, VApi.VaultFolder>>(new Map());
  const legacyMigrationRunningRef = useRef(false);

  // cacheUserIdRef holds the IndexedDB cache key derived from the envelope salt.
  const cacheUserIdRef = useRef<string>("");

  const persistCiphertextCache = useCallback(
    (nextCursor = cursorRef.current) => {
      if (!cacheUserIdRef.current) return;
      void putVaultCache({
        userId: cacheUserIdRef.current,
        cursor: nextCursor,
        folders: Array.from(rawFoldersRef.current.values()),
        items: Array.from(rawItemsRef.current.values()),
      });
    },
    [],
  );

  const migrateLegacyCiphertextRows = useCallback(async () => {
    const key = keyRef.current;
    if (!key || legacyMigrationRunningRef.current) return;
    const legacyFolders = Array.from(rawFoldersRef.current.values()).filter(
      (f) => !f.deleted_at && isLegacyCrypto(f.crypto_version),
    );
    const legacyItems = Array.from(rawItemsRef.current.values()).filter(
      (it) =>
        !it.deleted_at && !it.reprompt && isLegacyCrypto(it.crypto_version),
    );
    if (!legacyFolders.length && !legacyItems.length) return;
    legacyMigrationRunningRef.current = true;
    try {
      for (const f of legacyFolders) {
        try {
          const name = await decryptStringCompat(
            key,
            f.name_cipher,
            f.name_nonce,
            folderAAD(f.id, "name", f.crypto_version),
            f.crypto_version,
          );
          const enc = await encryptString(
            key,
            name,
            folderAAD(f.id, "name", CURRENT_CRYPTO_VERSION),
          );
          const raw = await VApi.updateFolder(
            f.id,
            enc.cipher,
            enc.nonce,
            CURRENT_CRYPTO_VERSION,
            f.revision,
          );
          rawFoldersRef.current.set(raw.id, raw);
        } catch {
          // Best-effort migration; conflicts or stale rows will be retried later.
        }
      }
      for (const it of legacyItems) {
        try {
          const dec = await decryptItem(key, it, { includeSensitive: true });
          const input = await encryptItemInput(
            key,
            {
              type: dec.type,
              folderId: dec.folderId,
              name: dec.name,
              notes: dec.notes,
              fields: dec.fields,
              favorite: dec.favorite,
              reprompt: dec.reprompt,
            },
            it.id,
          );
          input.if_revision = it.revision;
          const raw = await VApi.updateItem(it.id, input);
          rawItemsRef.current.set(raw.id, raw);
        } catch {
          // Best-effort migration; conflicts or stale rows will be retried later.
        }
      }
      persistCiphertextCache();
    } finally {
      legacyMigrationRunningRef.current = false;
    }
  }, [persistCiphertextCache]);

  // Full/delta sync from the server, decrypt, and merge into local state.
  // Loops on has_more so a large vault streams in bounded pages. Reads inputs
  // from refs so it is safe to call immediately after a state reset.
  const syncAndDecrypt = useCallback(async () => {
    const key = keyRef.current;
    if (!key) return;
    let since = cursorRef.current;
    const folderMap = new Map(foldersRef.current.map((f) => [f.id, f]));
    const byId = new Map(itemsRef.current.map((it) => [it.id, it]));
    let changed = false;
    // Page through all pending changes in this sync run.
    for (;;) {
      const res = await VApi.sync(since);
      for (const raw of res.items) {
        if (raw.deleted_at) {
          byId.delete(raw.id);
          rawItemsRef.current.delete(raw.id);
        } else {
          // A corrupted/corrupted ciphertext (migration gone wrong, server
          // bug, tampering) must NOT abort the whole sync: one bad row would
          // otherwise lock the user out of the entire vault. Drop in a
          // placeholder so the row is visible and the rest still decrypts.
          try {
            byId.set(raw.id, await decryptItem(key, raw));
          } catch {
            byId.set(raw.id, undecryptableItem(raw));
          }
          rawItemsRef.current.set(raw.id, raw);
        }
      }
      for (const f of res.folders) {
        if (f.deleted_at) {
          folderMap.delete(f.id);
          rawFoldersRef.current.delete(f.id);
          continue;
        }
        try {
          const name = await decryptStringCompat(
            key,
            f.name_cipher,
            f.name_nonce,
            folderAAD(f.id, "name", f.crypto_version),
            f.crypto_version,
          );
          folderMap.set(f.id, { id: f.id, name, revision: f.revision });
        } catch {
          folderMap.set(f.id, { id: f.id, name: "•••", revision: f.revision });
        }
        rawFoldersRef.current.set(f.id, f);
      }
      since = res.cursor;
      if (res.items.length || res.folders.length) changed = true;
      if (!res.has_more) break;
    }
    if (changed) {
      const nextItems = Array.from(byId.values()).sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      const nextFolders = Array.from(folderMap.values()).sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      commitItems(nextItems);
      foldersRef.current = nextFolders;
      setFolders(nextFolders);
    }
    commitCursor(since);
    // Persist the ciphertext bundle so the next unlock can render instantly
    // (and survive being offline). Ciphertext only — the key is memory-only.
    persistCiphertextCache(since);
    void migrateLegacyCiphertextRows();
  }, [
    commitItems,
    commitCursor,
    migrateLegacyCiphertextRows,
    persistCiphertextCache,
  ]);

  // hydrateFromCache decrypts the IndexedDB ciphertext cache (if any) into the
  // decrypted cache so the UI can render before the network sync completes.
  // Sets the cursor to the cached value so the subsequent sync is a true delta.
  const hydrateFromCache = useCallback(async () => {
    const key = keyRef.current;
    const uid = cacheUserIdRef.current;
    if (!key || !uid) return;
    const cached = await getVaultCache(uid);
    if (!cached) return;
    const folderMap = new Map(foldersRef.current.map((f) => [f.id, f]));
    const byId = new Map(itemsRef.current.map((it) => [it.id, it]));
    for (const raw of cached.items as VApi.VaultItem[]) {
      try {
        byId.set(raw.id, await decryptItem(key, raw));
      } catch {
        byId.set(raw.id, undecryptableItem(raw));
      }
      rawItemsRef.current.set(raw.id, raw);
    }
    for (const f of cached.folders as VApi.VaultFolder[]) {
      try {
        const name = await decryptStringCompat(
          key,
          f.name_cipher,
          f.name_nonce,
          folderAAD(f.id, "name", f.crypto_version),
          f.crypto_version,
        );
        folderMap.set(f.id, { id: f.id, name, revision: f.revision });
      } catch {
        folderMap.set(f.id, { id: f.id, name: "•••", revision: f.revision });
      }
      rawFoldersRef.current.set(f.id, f);
    }
    const nextItems = Array.from(byId.values()).sort((a, b) =>
      a.name.localeCompare(b.name),
    );
    const nextFolders = Array.from(folderMap.values()).sort((a, b) =>
      a.name.localeCompare(b.name),
    );
    commitItems(nextItems);
    foldersRef.current = nextFolders;
    setFolders(nextFolders);
    commitCursor(cached.cursor);
  }, [commitItems, commitCursor]);

  // foldersRef mirrors folders state (see cursorRef/itemsRef rationale).
  const foldersRef = useRef<DecryptedFolder[]>([]);

  const bootstrap = useCallback(async () => {
    setError(null);
    try {
      const env = await VApi.getKeys();
      envelopeRef.current = env;
      setStatus("locked");
    } catch (e) {
      if (
        e &&
        typeof e === "object" &&
        "status" in e &&
        (e as { status: number }).status === 404
      ) {
        setStatus("not_setup");
      } else {
        setStatus("unknown");
        setError(e instanceof Error ? e.message : "failed to reach vault");
      }
    }
  }, []);

  const setupAndUnlock = useCallback(
    async (masterPassword: string) => {
      setBusy(true);
      setError(null);
      try {
        const params: KdfParams = DEFAULT_KDF_PARAMS;
        const salt = generateSalt();
        const masterKey = await deriveMasterKey(masterPassword, salt, params);
        const vaultKey = generateVaultKey();
        const wrapped = await encryptBytes(masterKey, vaultKey, AAD.envelope);
        const env: VApi.EnvelopeInput = {
          kdf_algorithm: "argon2id",
          kdf_salt: salt,
          kdf_memory_kib: params.memoryKiB,
          kdf_iterations: params.iterations,
          kdf_parallelism: params.parallelism,
          protected_vault_key: wrapped.cipher,
          protected_vault_nonce: wrapped.nonce,
          crypto_version: CURRENT_CRYPTO_VERSION,
        };
        await VApi.setup(env);
        keyRef.current = vaultKey;
        envelopeRef.current = {
          ...env,
          version: 1,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        cacheUserIdRef.current = userIdForEnvelope(salt);
        cursorRef.current = 0;
        itemsRef.current = [];
        foldersRef.current = [];
        rawItemsRef.current = new Map();
        rawFoldersRef.current = new Map();
        setCursor(0);
        setItems([]);
        setFolders([]);
        setStatus("unlocked");
        await syncAndDecrypt();
      } finally {
        setBusy(false);
      }
    },
    [syncAndDecrypt],
  );

  const unlock = useCallback(
    async (masterPassword: string) => {
      const env = envelopeRef.current;
      if (!env) {
        // Bootstrap not run yet; fetch envelope on demand.
        try {
          envelopeRef.current = await VApi.getKeys();
        } catch {
          setStatus("not_setup");
          throw new WrongMasterPassword();
        }
      }
      const envNow = envelopeRef.current!;
      setBusy(true);
      setError(null);
      try {
        const params: KdfParams = {
          memoryKiB: envNow.kdf_memory_kib,
          iterations: envNow.kdf_iterations,
          parallelism: envNow.kdf_parallelism,
        };
        const masterKey = await deriveMasterKey(
          masterPassword,
          envNow.kdf_salt,
          params,
        );
        let vaultKey: Uint8Array;
        try {
          // AES-GCM auth-tag verification is the sole integrity check: a wrong
          // master password yields a tag mismatch here, surfaced as
          // WrongMasterPassword. (The derived key is never sent to the server.)
          vaultKey = await decryptBytesCompat(
            masterKey,
            envNow.protected_vault_key,
            envNow.protected_vault_nonce,
            AAD.envelope,
            envNow.crypto_version,
          );
        } catch {
          zeroize(masterKey);
          throw new WrongMasterPassword();
        }
        zeroize(masterKey);
        keyRef.current = vaultKey;
        cacheUserIdRef.current = userIdForEnvelope(envNow.kdf_salt);
        cursorRef.current = 0;
        itemsRef.current = [];
        foldersRef.current = [];
        rawItemsRef.current = new Map();
        rawFoldersRef.current = new Map();
        setCursor(0);
        setItems([]);
        setFolders([]);
        setStatus("unlocked");
        // Render from the ciphertext cache first (instant), then delta-sync.
        await hydrateFromCache();
        await syncAndDecrypt();
      } finally {
        setBusy(false);
      }
    },
    [syncAndDecrypt, hydrateFromCache],
  );

  const lock = useCallback(() => {
    zeroize(keyRef.current);
    keyRef.current = null;
    itemsRef.current = [];
    foldersRef.current = [];
    rawItemsRef.current = new Map();
    rawFoldersRef.current = new Map();
    cursorRef.current = 0;
    setItems([]);
    setFolders([]);
    setCursor(0);
    setStatus("locked");
  }, []);

  const refresh = useCallback(async () => {
    setBusy(true);
    try {
      await syncAndDecrypt();
    } finally {
      setBusy(false);
    }
  }, [syncAndDecrypt]);

  const createItem = useCallback(
    async (draft: DraftItem): Promise<DecryptedItem> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const itemId = vaultId();
      const input = await encryptItemInput(key, draft, itemId);
      const raw = await VApi.createItem(input);
      const dec = await decryptItem(key, raw);
      rawItemsRef.current.set(raw.id, raw);
      const next = [...itemsRef.current, dec].sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      commitItems(next);
      commitCursor(raw.revision);
      persistCiphertextCache(raw.revision);
      return dec;
    },
    [commitItems, commitCursor, persistCiphertextCache],
  );

  const updateItem = useCallback(
    async (
      id: string,
      draft: DraftItem,
      ifRevision: number,
    ): Promise<DecryptedItem> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const input = await encryptItemInput(key, draft, id);
      input.if_revision = ifRevision;
      const raw = await VApi.updateItem(id, input);
      const dec = await decryptItem(key, raw);
      rawItemsRef.current.set(raw.id, raw);
      const next = itemsRef.current
        .map((it) => (it.id === id ? dec : it))
        .sort((a, b) => a.name.localeCompare(b.name));
      commitItems(next);
      commitCursor(raw.revision);
      persistCiphertextCache(raw.revision);
      return dec;
    },
    [commitItems, commitCursor, persistCiphertextCache],
  );

  const deleteItem = useCallback(
    async (id: string, ifRevision: number): Promise<void> => {
      await VApi.deleteItem(id, ifRevision);
      rawItemsRef.current.delete(id);
      commitItems(itemsRef.current.filter((it) => it.id !== id));
      persistCiphertextCache();
    },
    [commitItems, persistCiphertextCache],
  );

  // --- attachments ---

  const listAttachments = useCallback(
    async (itemId: string): Promise<Attachment[]> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const metas = await VApi.listAttachments(itemId);
      const out: Attachment[] = [];
      for (const m of metas) {
        let name = "attachment";
        try {
          name = await decryptStringCompat(
            key,
            m.name_cipher,
            m.name_nonce,
            attachmentAAD(m.id, "name", m.crypto_version),
            m.crypto_version,
          );
        } catch {
          name = "attachment";
        }
        out.push({
          id: m.id,
          name,
          size: m.size_bytes,
          fileKeyCipher: m.file_key_cipher,
          fileKeyNonce: m.file_key_nonce,
          cryptoVersion: m.crypto_version,
          createdAt: m.created_at,
        });
      }
      return out;
    },
    [],
  );

  const uploadAttachment = useCallback(
    async (itemId: string, file: File): Promise<Attachment> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const plain = new Uint8Array(await file.arrayBuffer());
      // Fresh per-file key, sealed content (nonce||ciphertext), wrapped key and
      // encrypted filename — all under the vault key.
      const attachmentId = vaultId();
      const fileKey = randomBytes(32);
      const sealed = await seal(
        fileKey,
        plain,
        attachmentAAD(attachmentId, "payload", CURRENT_CRYPTO_VERSION),
      );
      const wrapped = await encryptBytes(
        key,
        fileKey,
        attachmentAAD(attachmentId, "file-key", CURRENT_CRYPTO_VERSION),
      );
      const nameEnc = await encryptString(
        key,
        file.name || "attachment",
        attachmentAAD(attachmentId, "name", CURRENT_CRYPTO_VERSION),
      );
      const meta: AttachmentMeta = await VApi.uploadAttachment(itemId, sealed, {
        id: attachmentId,
        fileKeyCipher: wrapped.cipher,
        fileKeyNonce: wrapped.nonce,
        nameCipher: nameEnc.cipher,
        nameNonce: nameEnc.nonce,
        sizeBytes: plain.length,
      });
      return {
        id: meta.id,
        name: file.name || "attachment",
        size: meta.size_bytes,
        fileKeyCipher: meta.file_key_cipher,
        fileKeyNonce: meta.file_key_nonce,
        cryptoVersion: meta.crypto_version,
        createdAt: meta.created_at,
      };
    },
    [],
  );

  const deleteAttachment = useCallback(
    async (itemId: string, aid: string): Promise<void> => {
      await VApi.deleteAttachment(itemId, aid);
    },
    [],
  );

  const downloadAttachment = useCallback(
    async (
      itemId: string,
      att: Attachment,
    ): Promise<{ blob: Blob; name: string }> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const sealed = await VApi.fetchAttachmentBlob(itemId, att.id);
      const fileKey = await decryptBytesCompat(
        key,
        att.fileKeyCipher,
        att.fileKeyNonce,
        attachmentAAD(att.id, "file-key", att.cryptoVersion),
        att.cryptoVersion,
      );
      const plain = await openCompat(
        fileKey,
        sealed,
        attachmentAAD(att.id, "payload", att.cryptoVersion),
        att.cryptoVersion,
      );
      return { blob: new Blob([plain as unknown as BlobPart]), name: att.name };
    },
    [],
  );

  // verifyMasterPassword re-derives the master key from the live envelope and
  // the supplied password, decrypts the wrapped vault key, and checks it byte-
  // for-byte against the key currently in memory. It is the backing check for
  // the "reprompt" toggle on sensitive items.
  const verifyMasterPassword = useCallback(
    async (password: string): Promise<boolean> => {
      const env = envelopeRef.current;
      const live = keyRef.current;
      if (!env || !live) return false;
      const params: KdfParams = {
        memoryKiB: env.kdf_memory_kib,
        iterations: env.kdf_iterations,
        parallelism: env.kdf_parallelism,
      };
      const masterKey = await deriveMasterKey(password, env.kdf_salt, params);
      try {
        const candidate = await decryptBytesCompat(
          masterKey,
          env.protected_vault_key,
          env.protected_vault_nonce,
          AAD.envelope,
          env.crypto_version,
        );
        zeroize(masterKey);
        if (candidate.length !== live.length) return false;
        let match = 0;
        for (let i = 0; i < candidate.length; i++) {
          match |= candidate[i] ^ live[i];
        }
        zeroize(candidate);
        return match === 0;
      } catch {
        zeroize(masterKey);
        return false;
      }
    },
    [],
  );

  const unlockItemDetails = useCallback(
    async (itemId: string, password: string): Promise<DecryptedItem | null> => {
      const key = keyRef.current;
      if (!key) return null;
      const ok = await verifyMasterPassword(password);
      if (!ok) return null;
      const raw = rawItemsRef.current.get(itemId);
      if (!raw) return null;
      const dec = await decryptItem(key, raw, { includeSensitive: true });
      if (isLegacyCrypto(raw.crypto_version)) {
        try {
          const input = await encryptItemInput(
            key,
            {
              type: dec.type,
              folderId: dec.folderId,
              name: dec.name,
              notes: dec.notes,
              fields: dec.fields,
              favorite: dec.favorite,
              reprompt: dec.reprompt,
            },
            raw.id,
          );
          input.if_revision = raw.revision;
          const migrated = await VApi.updateItem(raw.id, input);
          rawItemsRef.current.set(migrated.id, migrated);
          persistCiphertextCache(migrated.revision);
        } catch {
          // Best-effort: keep the reveal working and retry migration later.
        }
      }
      return dec;
    },
    [persistCiphertextCache, verifyMasterPassword],
  );

  const exportBundle = useCallback(async () => {
    return VApi.exportVault();
  }, []);

  const importBundle = useCallback(
    async (bundle: VApi.ExportBundle, sourcePassword?: string) => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      let sourceKey = key;
      if (sourcePassword && bundle.envelope) {
        const params: KdfParams = {
          memoryKiB: bundle.envelope.kdf_memory_kib,
          iterations: bundle.envelope.kdf_iterations,
          parallelism: bundle.envelope.kdf_parallelism,
        };
        const sourceMaster = await deriveMasterKey(
          sourcePassword,
          bundle.envelope.kdf_salt,
          params,
        );
        try {
          sourceKey = await decryptBytesCompat(
            sourceMaster,
            bundle.envelope.protected_vault_key,
            bundle.envelope.protected_vault_nonce,
            AAD.envelope,
            bundle.envelope.crypto_version,
          );
        } catch {
          throw new WrongMasterPassword();
        } finally {
          zeroize(sourceMaster);
        }
      }
      const folders = [];
      try {
        const folderIdMap = new Map<string, string>();
        const itemIdMap = new Map<string, string>();
        for (const f of bundle.folders ?? []) {
          const destFolderId = vaultId();
          folderIdMap.set(f.id, destFolderId);
          const name = await decryptStringCompat(
            sourceKey,
            f.name_cipher,
            f.name_nonce,
            folderAAD(f.id, "name", f.crypto_version),
            f.crypto_version,
          );
          const enc = await encryptString(
            key,
            name,
            folderAAD(destFolderId, "name", CURRENT_CRYPTO_VERSION),
          );
          folders.push({
            id: destFolderId,
            name_cipher: enc.cipher,
            name_nonce: enc.nonce,
            crypto_version: CURRENT_CRYPTO_VERSION,
            deleted_at: f.deleted_at,
          });
        }
        const items: (VApi.ItemInput & {
          id?: string;
          deleted_at?: string | null;
        })[] = [];
        for (const it of bundle.items ?? []) {
          const destItemId = vaultId();
          itemIdMap.set(it.id, destItemId);
          const name = await decryptStringCompat(
            sourceKey,
            it.name_cipher,
            it.name_nonce,
            itemAAD(it.id, "name", it.crypto_version),
            it.crypto_version,
          );
          const data = await decryptStringCompat(
            sourceKey,
            it.data_cipher,
            it.data_nonce,
            itemAAD(it.id, "data", it.crypto_version),
            it.crypto_version,
          );
          const nameEnc = await encryptString(
            key,
            name,
            itemAAD(destItemId, "name", CURRENT_CRYPTO_VERSION),
          );
          const dataEnc = await encryptString(
            key,
            data,
            itemAAD(destItemId, "data", CURRENT_CRYPTO_VERSION),
          );
          let notes_cipher = "";
          let notes_nonce = "";
          if (it.notes_cipher && it.notes_nonce) {
            const notes = await decryptStringCompat(
              sourceKey,
              it.notes_cipher,
              it.notes_nonce,
              itemAAD(it.id, "notes", it.crypto_version),
              it.crypto_version,
            );
            const notesEnc = await encryptString(
              key,
              notes,
              itemAAD(destItemId, "notes", CURRENT_CRYPTO_VERSION),
            );
            notes_cipher = notesEnc.cipher;
            notes_nonce = notesEnc.nonce;
          }
          items.push({
            id: destItemId,
            type: it.type,
            folder_id: it.folder_id
              ? (folderIdMap.get(it.folder_id) ?? "")
              : "",
            name_cipher: nameEnc.cipher,
            name_nonce: nameEnc.nonce,
            data_cipher: dataEnc.cipher,
            data_nonce: dataEnc.nonce,
            notes_cipher,
            notes_nonce,
            crypto_version: CURRENT_CRYPTO_VERSION,
            favorite: it.favorite,
            reprompt: it.reprompt,
            deleted_at: it.deleted_at,
          });
        }
        const attachments: VApi.ExportAttachment[] = [];
        for (const att of bundle.attachments ?? []) {
          const destAttachmentId = vaultId();
          const fileKey = await decryptBytesCompat(
            sourceKey,
            att.file_key_cipher,
            att.file_key_nonce,
            attachmentAAD(att.id, "file-key", att.crypto_version),
            att.crypto_version,
          );
          const verifiedPayload = await openCompat(
            fileKey,
            b64ToBytes(att.payload),
            attachmentAAD(att.id, "payload", att.crypto_version),
            att.crypto_version,
          );
          const newFileKey = randomBytes(32);
          const sealedPayload = await seal(
            newFileKey,
            verifiedPayload,
            attachmentAAD(destAttachmentId, "payload", CURRENT_CRYPTO_VERSION),
          );
          zeroize(verifiedPayload);
          const wrapped = await encryptBytes(
            key,
            newFileKey,
            attachmentAAD(destAttachmentId, "file-key", CURRENT_CRYPTO_VERSION),
          );
          const payload = bytesToB64(sealedPayload);
          zeroize(sealedPayload);
          zeroize(fileKey);
          zeroize(newFileKey);
          const name = await decryptStringCompat(
            sourceKey,
            att.name_cipher,
            att.name_nonce,
            attachmentAAD(att.id, "name", att.crypto_version),
            att.crypto_version,
          );
          const nameEnc = await encryptString(
            key,
            name,
            attachmentAAD(destAttachmentId, "name", CURRENT_CRYPTO_VERSION),
          );
          attachments.push({
            id: destAttachmentId,
            item_id: itemIdMap.get(att.item_id) ?? "",
            name_cipher: nameEnc.cipher,
            name_nonce: nameEnc.nonce,
            file_key_cipher: wrapped.cipher,
            file_key_nonce: wrapped.nonce,
            crypto_version: CURRENT_CRYPTO_VERSION,
            size_bytes: att.size_bytes,
            payload,
          });
        }
        const counts = await VApi.importVault({ folders, items, attachments });
        // Pull the freshly-imported rows into the decrypted cache.
        await syncAndDecrypt();
        return counts;
      } finally {
        if (sourceKey !== key) zeroize(sourceKey);
      }
    },
    [syncAndDecrypt],
  );

  // --- folder CRUD ----------------------------------------------------------

  const createFolder = useCallback(
    async (name: string): Promise<DecryptedFolder> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const folderId = vaultId();
      const enc = await encryptString(
        key,
        name,
        folderAAD(folderId, "name", CURRENT_CRYPTO_VERSION),
      );
      const raw = await VApi.createFolder(
        folderId,
        enc.cipher,
        enc.nonce,
        CURRENT_CRYPTO_VERSION,
      );
      rawFoldersRef.current.set(raw.id, raw);
      const folder: DecryptedFolder = {
        id: raw.id,
        name,
        revision: raw.revision,
      };
      const next = [...foldersRef.current, folder].sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      foldersRef.current = next;
      setFolders(next);
      commitCursor(raw.revision);
      persistCiphertextCache(raw.revision);
      return folder;
    },
    [commitCursor, persistCiphertextCache],
  );

  const renameFolder = useCallback(
    async (id: string, name: string): Promise<void> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const existing = foldersRef.current.find((f) => f.id === id);
      if (!existing) throw new Error("folder not found");
      const enc = await encryptString(
        key,
        name,
        folderAAD(id, "name", CURRENT_CRYPTO_VERSION),
      );
      const raw = await VApi.updateFolder(
        id,
        enc.cipher,
        enc.nonce,
        CURRENT_CRYPTO_VERSION,
        existing.revision,
      );
      rawFoldersRef.current.set(raw.id, raw);
      const next = foldersRef.current
        .map((f) => (f.id === id ? { id, name, revision: raw.revision } : f))
        .sort((a, b) => a.name.localeCompare(b.name));
      foldersRef.current = next;
      setFolders(next);
      commitCursor(raw.revision);
      persistCiphertextCache(raw.revision);
    },
    [commitCursor, persistCiphertextCache],
  );

  const deleteFolder = useCallback(
    async (id: string): Promise<void> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const existing = foldersRef.current.find((f) => f.id === id);
      if (!existing) throw new Error("folder not found");
      await VApi.deleteFolder(id, existing.revision);
      rawFoldersRef.current.delete(id);
      const next = foldersRef.current.filter((f) => f.id !== id);
      foldersRef.current = next;
      setFolders(next);
      persistCiphertextCache();
    },
    [persistCiphertextCache],
  );

  // --- master password change ----------------------------------------------

  const changeMasterPassword = useCallback(
    async (currentPassword: string, newPassword: string) => {
      const env = envelopeRef.current;
      const liveKey = keyRef.current;
      if (!env || !liveKey) throw new Error("vault locked");
      if (newPassword.length < 15) {
        throw new Error("master password must be at least 15 characters");
      }
      // Verify the current password first (constant-time compare to the live
      // vault key) so we never rotate on a wrong current password.
      const ok = await verifyMasterPassword(currentPassword);
      if (!ok) throw new WrongMasterPassword();
      // Re-derive a fresh master key from the NEW password using a new salt and
      // the existing KDF params, re-wrap the SAME vault key, and rotate the envelope. The
      // vault key (and therefore every item) is untouched.
      const params: KdfParams = {
        memoryKiB: env.kdf_memory_kib,
        iterations: env.kdf_iterations,
        parallelism: env.kdf_parallelism,
      };
      const newSalt = generateSalt();
      const newMaster = await deriveMasterKey(newPassword, newSalt, params);
      const wrapped = await encryptBytes(newMaster, liveKey, AAD.envelope);
      zeroize(newMaster);
      await VApi.rotateKeys({
        kdf_algorithm: env.kdf_algorithm || "argon2id",
        kdf_salt: newSalt,
        kdf_memory_kib: params.memoryKiB,
        kdf_iterations: params.iterations,
        kdf_parallelism: params.parallelism,
        protected_vault_key: wrapped.cipher,
        protected_vault_nonce: wrapped.nonce,
        crypto_version: CURRENT_CRYPTO_VERSION,
        if_version: env.version,
      });
      // Refresh the cached envelope so the version advances locally.
      envelopeRef.current = await VApi.getKeys();
      // The cache key is derived from the envelope salt, which changes on
      // rotation. Clear the stale blob so IndexedDB does not grow unbounded
      // across successive master-password changes.
      const oldCacheUserId = cacheUserIdRef.current;
      cacheUserIdRef.current = userIdForEnvelope(envelopeRef.current.kdf_salt);
      persistCiphertextCache();
      if (
        oldCacheUserId &&
        oldCacheUserId !== cacheUserIdRef.current
      ) {
        void clearVaultCache(oldCacheUserId);
      }
    },
    [persistCiphertextCache, verifyMasterPassword],
  );

  // --- item history ---------------------------------------------------------

  const listItemRevisions = useCallback(async (itemId: string) => {
    const key = keyRef.current;
    if (!key) throw new Error("vault locked");
    const revs = await VApi.listRevisions(itemId);
    const out: {
      id: string;
      name: string;
      type: VaultItemType;
      folderId: string;
      hasNotes: boolean;
      favorite: boolean;
      reprompt: boolean;
      revision: number;
      createdAt: string;
    }[] = [];
    for (const r of revs) {
      let name = "—";
      let hasNotes = Boolean(r.notes_cipher && r.notes_nonce);
      try {
        name = await decryptStringCompat(
          key,
          r.name_cipher,
          r.name_nonce,
          itemAAD(itemId, "name", r.crypto_version),
          r.crypto_version,
        );
      } catch {
        name = "—";
      }
      if (r.notes_cipher && r.notes_nonce) {
        try {
          hasNotes = Boolean(
            await decryptStringCompat(
              key,
              r.notes_cipher,
              r.notes_nonce,
              itemAAD(itemId, "notes", r.crypto_version),
              r.crypto_version,
            ),
          );
        } catch {
          hasNotes = true;
        }
      }
      out.push({
        id: r.id,
        name,
        type: r.type,
        folderId: r.folder_id,
        hasNotes,
        favorite: r.favorite,
        reprompt: r.reprompt,
        revision: r.revision,
        createdAt: r.created_at,
      });
    }
    return out;
  }, []);

  const restoreItemRevision = useCallback(
    async (itemId: string, revId: string) => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const existing = itemsRef.current.find((it) => it.id === itemId);
      if (!existing) throw new Error("item not found");
      const raw = await VApi.restoreRevision(itemId, revId, existing.revision);
      const dec = await decryptItem(key, raw);
      rawItemsRef.current.set(raw.id, raw);
      const next = itemsRef.current
        .map((it) => (it.id === itemId ? dec : it))
        .sort((a, b) => a.name.localeCompare(b.name));
      commitItems(next);
      commitCursor(raw.revision);
      persistCiphertextCache(raw.revision);
    },
    [commitItems, commitCursor, persistCiphertextCache],
  );

  // --- trash (recycle bin) -------------------------------------------------

  const listTrash = useCallback(async (): Promise<TrashEntry[]> => {
    const key = keyRef.current;
    if (!key) throw new Error("vault locked");
    const res = await VApi.listTrash();
    const out: TrashEntry[] = [];
    for (const f of res.folders) {
      let name = "•••";
      try {
        name = await decryptStringCompat(
          key,
          f.name_cipher,
          f.name_nonce,
          folderAAD(f.id, "name", f.crypto_version),
          f.crypto_version,
        );
      } catch {
        // keep placeholder
      }
      out.push({
        id: f.id,
        kind: "folder",
        name,
        revision: f.revision,
        deletedAt: f.deleted_at ?? f.updated_at,
      });
    }
    for (const it of res.items) {
      let name = "•••";
      try {
        name = await decryptStringCompat(
          key,
          it.name_cipher,
          it.name_nonce,
          itemAAD(it.id, "name", it.crypto_version),
          it.crypto_version,
        );
      } catch {
        // keep placeholder
      }
      out.push({
        id: it.id,
        kind: "item",
        name,
        type: it.type,
        revision: it.revision,
        deletedAt: it.deleted_at ?? it.updated_at,
      });
    }
    // Newest deletions first.
    out.sort((a, b) => b.deletedAt.localeCompare(a.deletedAt));
    return out;
  }, []);

  const restoreTrashItem = useCallback(
    async (id: string): Promise<void> => {
      await VApi.restoreItem(id);
      await syncAndDecrypt();
    },
    [syncAndDecrypt],
  );

  const restoreTrashFolder = useCallback(
    async (id: string): Promise<void> => {
      await VApi.restoreFolder(id);
      await syncAndDecrypt();
    },
    [syncAndDecrypt],
  );

  const purgeTrashItem = useCallback(async (id: string): Promise<void> => {
    await VApi.purgeItem(id);
  }, []);

  const purgeTrashFolder = useCallback(async (id: string): Promise<void> => {
    await VApi.purgeFolder(id);
  }, []);

  const emptyTrash = useCallback(async (): Promise<void> => {
    await VApi.emptyTrash();
  }, []);

  // --- CSV import ----------------------------------------------------------
  //
  // Each parsed row is turned into a DraftItem (login type by default), then
  // pushed through the normal createItem path so encryption, cursor tracking
  // and the ciphertext cache stay consistent. Items are created sequentially
  // to keep the server-side transactions ordered and to let onProgress report
  // a meaningful counter.
  const importCSVRows = useCallback(
    async (
      items: CSVParsedItem[],
      onProgress?: (done: number, total: number) => void,
    ): Promise<{ created: number; failed: number }> => {
      let created = 0;
      let failed = 0;
      const total = items.length;
      // Cache folder-name → folder-id so repeated CSV rows in the same folder
      // do not each trigger a create round-trip.
      const folderCache = new Map<string, string>();
      for (const csv of items) {
        const fields = [];
        if (csv.username)
          fields.push(newField("text", "Username", csv.username));
        if (csv.password)
          fields.push(newField("password", "Password", csv.password));
        if (csv.url) fields.push(newField("url", "Website", csv.url));
        if (csv.totp) fields.push(newField("totp", "TOTP", csv.totp));

        let folderId = "";
        if (csv.folder) {
          const cached = folderCache.get(csv.folder);
          if (cached) {
            folderId = cached;
          } else {
            try {
              const f = await createFolder(csv.folder);
              folderCache.set(csv.folder, f.id);
              folderId = f.id;
            } catch {
              folderCache.set(csv.folder, "");
            }
          }
        }

        const draft: DraftItem = {
          type: csv.type ?? "login",
          folderId,
          name: csv.name || "Untitled",
          notes: csv.notes ?? "",
          fields,
          favorite: false,
          reprompt: false,
        };
        try {
          await createItem(draft);
          created++;
        } catch {
          failed++;
        }
        onProgress?.(created + failed, total);
      }
      return { created, failed };
    },
    [createItem, createFolder],
  );

  // Auto-bootstrap on first mount so the page knows which gate to show.
  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  // Auto-lock: when the vault is unlocked, lock it after a period of inactivity
  // or when the tab stays hidden for a while. This limits the window in which a
  // decrypted vault sits open on an unattended device. Activity resets the
  // idle timer; lock() wipes the key and decrypted cache.
  const lockRef = useRef(lock);
  lockRef.current = lock;
  useEffect(() => {
    if (status !== "unlocked") return;
    let settings = readVaultSecuritySettings();
    let idleTimer: number | undefined;
    let hiddenSince = 0;

    const resetIdle = () => {
      window.clearTimeout(idleTimer);
      idleTimer = window.setTimeout(
        () => lockRef.current(),
        settings.autoLockMinutes * 60_000,
      );
    };
    const onVisibility = () => {
      if (document.hidden) {
        hiddenSince = Date.now();
      } else if (
        hiddenSince &&
        Date.now() - hiddenSince >= settings.autoLockMinutes * 60_000
      ) {
        lockRef.current();
        hiddenSince = 0;
      }
    };
    const onSettingsChanged = () => {
      settings = readVaultSecuritySettings();
      resetIdle();
    };
    const events = ["mousemove", "keydown", "click", "scroll", "touchstart"];
    events.forEach((e) =>
      window.addEventListener(e, resetIdle, { passive: true }),
    );
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener(
      "vault-security-settings-changed",
      onSettingsChanged,
    );
    resetIdle();
    return () => {
      window.clearTimeout(idleTimer);
      events.forEach((e) => window.removeEventListener(e, resetIdle));
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener(
        "vault-security-settings-changed",
        onSettingsChanged,
      );
    };
  }, [status]);

  const value = useMemo<VaultContextValue>(
    () => ({
      status,
      error,
      busy,
      items,
      folders,
      cursor,
      bootstrap,
      setupAndUnlock,
      unlock,
      lock,
      refresh,
      createItem,
      updateItem,
      deleteItem,
      listAttachments,
      uploadAttachment,
      deleteAttachment,
      downloadAttachment,
      verifyMasterPassword,
      unlockItemDetails,
      exportBundle,
      importBundle,
      createFolder,
      renameFolder,
      deleteFolder,
      changeMasterPassword,
      listItemRevisions,
      restoreItemRevision,
      listTrash,
      restoreTrashItem,
      restoreTrashFolder,
      purgeTrashItem,
      purgeTrashFolder,
      emptyTrash,
      importCSVRows,
    }),
    [
      status,
      error,
      busy,
      items,
      folders,
      cursor,
      bootstrap,
      setupAndUnlock,
      unlock,
      lock,
      refresh,
      createItem,
      updateItem,
      deleteItem,
      listAttachments,
      uploadAttachment,
      deleteAttachment,
      downloadAttachment,
      verifyMasterPassword,
      unlockItemDetails,
      exportBundle,
      importBundle,
      createFolder,
      renameFolder,
      deleteFolder,
      changeMasterPassword,
      listItemRevisions,
      restoreItemRevision,
      listTrash,
      restoreTrashItem,
      restoreTrashFolder,
      purgeTrashItem,
      purgeTrashFolder,
      emptyTrash,
      importCSVRows,
    ],
  );

  return (
    <VaultContext.Provider value={value}>{children}</VaultContext.Provider>
  );
}

export function useVault(): VaultContextValue {
  const ctx = useContext(VaultContext);
  if (!ctx) throw new Error("useVault must be used within <VaultProvider>");
  return ctx;
}
