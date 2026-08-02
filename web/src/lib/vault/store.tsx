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

// Attachment is the decrypted client view: the name is decrypted with the
// vault key, while fileKeyCipher/Nonce stay wrapped until download (when the
// file key is unwrapped and used to decrypt the blob).
export type Attachment = {
  id: string;
  name: string;
  size: number;
  fileKeyCipher: string;
  fileKeyNonce: string;
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

async function decryptItem(
  key: Uint8Array,
  it: VaultItem,
): Promise<DecryptedItem> {
  const name = await decryptString(key, it.name_cipher, it.name_nonce);
  const dataJson = await decryptString(key, it.data_cipher, it.data_nonce);
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
      notes = await decryptString(key, it.notes_cipher, it.notes_nonce);
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
): Promise<ItemInput> {
  const name = await encryptString(key, draft.name);
  const data: ItemData = { fields: draft.fields };
  const dataEnc = await encryptString(key, JSON.stringify(data));
  let notesCipher = "";
  let notesNonce = "";
  if (draft.notes) {
    const notes = await encryptString(key, draft.notes);
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
  // exportBundle / importBundle wrap the encrypted backup/restore endpoints.
  exportBundle: () => Promise<VApi.ExportBundle>;
  importBundle: (
    folders: { name_cipher: string; name_nonce: string }[],
    items: VApi.ItemInput[],
  ) => Promise<VApi.ImportCounts>;
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
        } else {
          byId.set(raw.id, await decryptItem(key, raw));
        }
      }
      for (const f of res.folders) {
        if (f.deleted_at) {
          folderMap.delete(f.id);
          continue;
        }
        try {
          const name = await decryptString(key, f.name_cipher, f.name_nonce);
          folderMap.set(f.id, { id: f.id, name, revision: f.revision });
        } catch {
          folderMap.set(f.id, { id: f.id, name: "•••", revision: f.revision });
        }
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
        const wrapped = await encryptBytes(masterKey, vaultKey);
        const env: VApi.EnvelopeInput = {
          kdf_algorithm: "argon2id",
          kdf_salt: salt,
          kdf_memory_kib: params.memoryKiB,
          kdf_iterations: params.iterations,
          kdf_parallelism: params.parallelism,
          protected_vault_key: wrapped.cipher,
          protected_vault_nonce: wrapped.nonce,
        };
		await VApi.setup(env);
		keyRef.current = vaultKey;
		envelopeRef.current = {
			...env,
			version: 1,
			updated_at: new Date().toISOString(),
		};
        cursorRef.current = 0;
        itemsRef.current = [];
        foldersRef.current = [];
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
          vaultKey = await decryptBytes(
            masterKey,
            envNow.protected_vault_key,
            envNow.protected_vault_nonce,
          );
        } catch {
          zeroize(masterKey);
          throw new WrongMasterPassword();
        }
        zeroize(masterKey);
        keyRef.current = vaultKey;
        cursorRef.current = 0;
        itemsRef.current = [];
        foldersRef.current = [];
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

  const lock = useCallback(() => {
    zeroize(keyRef.current);
    keyRef.current = null;
    itemsRef.current = [];
    foldersRef.current = [];
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
      const input = await encryptItemInput(key, draft);
      const raw = await VApi.createItem(input);
      const dec = await decryptItem(key, raw);
      const next = [...itemsRef.current, dec].sort((a, b) =>
        a.name.localeCompare(b.name),
      );
      commitItems(next);
      commitCursor(raw.revision);
      return dec;
    },
    [commitItems, commitCursor],
  );

  const updateItem = useCallback(
    async (
      id: string,
      draft: DraftItem,
      ifRevision: number,
    ): Promise<DecryptedItem> => {
      const key = keyRef.current;
      if (!key) throw new Error("vault locked");
      const input = await encryptItemInput(key, draft);
      input.if_revision = ifRevision;
      const raw = await VApi.updateItem(id, input);
      const dec = await decryptItem(key, raw);
      const next = itemsRef.current
        .map((it) => (it.id === id ? dec : it))
        .sort((a, b) => a.name.localeCompare(b.name));
      commitItems(next);
      commitCursor(raw.revision);
      return dec;
    },
    [commitItems, commitCursor],
  );

  const deleteItem = useCallback(
    async (id: string, ifRevision: number): Promise<void> => {
      await VApi.deleteItem(id, ifRevision);
      commitItems(itemsRef.current.filter((it) => it.id !== id));
    },
    [commitItems],
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
          name = await decryptString(key, m.name_cipher, m.name_nonce);
        } catch {
          name = "attachment";
        }
        out.push({
          id: m.id,
          name,
          size: m.size_bytes,
          fileKeyCipher: m.file_key_cipher,
          fileKeyNonce: m.file_key_nonce,
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
      const fileKey = randomBytes(32);
      const sealed = await seal(fileKey, plain);
      const wrapped = await encryptBytes(key, fileKey);
      const nameEnc = await encryptString(key, file.name || "attachment");
      const meta: AttachmentMeta = await VApi.uploadAttachment(itemId, sealed, {
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
      const fileKey = await decryptBytes(
        key,
        att.fileKeyCipher,
        att.fileKeyNonce,
      );
      const plain = await open(fileKey, sealed);
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
        const candidate = await decryptBytes(
          masterKey,
          env.protected_vault_key,
          env.protected_vault_nonce,
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

  const exportBundle = useCallback(async () => {
    return VApi.exportVault();
  }, []);

  const importBundle = useCallback(
    async (
      folders: { name_cipher: string; name_nonce: string }[],
      items: VApi.ItemInput[],
    ) => {
      const counts = await VApi.importVault(folders, items);
      // Pull the freshly-imported rows into the decrypted cache.
      await syncAndDecrypt();
      return counts;
    },
    [syncAndDecrypt],
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
    const IDLE_MS = 5 * 60_000; // 5 minutes
    const HIDDEN_MS = 5 * 60_000; // 5 minutes hidden
    let idleTimer: number | undefined;
    let hiddenSince = 0;

    const resetIdle = () => {
      window.clearTimeout(idleTimer);
      idleTimer = window.setTimeout(() => lockRef.current(), IDLE_MS);
    };
    const onVisibility = () => {
      if (document.hidden) {
        hiddenSince = Date.now();
      } else if (hiddenSince && Date.now() - hiddenSince >= HIDDEN_MS) {
        lockRef.current();
        hiddenSince = 0;
      }
    };
    const events = ["mousemove", "keydown", "click", "scroll", "touchstart"];
    events.forEach((e) =>
      window.addEventListener(e, resetIdle, { passive: true }),
    );
    document.addEventListener("visibilitychange", onVisibility);
    resetIdle();
    return () => {
      window.clearTimeout(idleTimer);
      events.forEach((e) => window.removeEventListener(e, resetIdle));
      document.removeEventListener("visibilitychange", onVisibility);
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
      exportBundle,
      importBundle,
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
      exportBundle,
      importBundle,
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
