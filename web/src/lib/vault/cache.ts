// IndexedDB-backed ciphertext cache for the vault.
//
// To render the vault instantly after unlock (and to allow viewing the last-
// synced state while offline), we persist the most recent SyncResponse payload
// — ciphertext rows plus the cursor — in IndexedDB, keyed by a stable user id
// derived from the envelope's KDF salt. The cache stores CIPHERTEXT only: the
// vault key is never persisted, so a locked browser never holds plaintext on
// disk. On unlock we decrypt the cached ciphertext with the freshly derived
// key for an immediate render, then a delta sync reconciles with the server.
//
// The store is the only writer; if the shape ever changes, bump CACHE_VERSION
// so older blobs are discarded.

const DB_NAME = "airbrew";
const STORE = "vault";
const CACHE_VERSION = 1;

export type VaultCache = {
  version: number;
  userId: string;
  cursor: number;
  folders: unknown[];
  items: unknown[];
  updatedAt: number;
};

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function keyFor(userId: string): string {
  return `${STORE}:${userId}`;
}

// userIdForEnvelope derives a stable per-user cache key from the envelope's
// salt. Two different master passwords on the same account share a salt (and
// thus a cache), which is correct: they unlock the same ciphertext.
export function userIdForEnvelope(saltB64: string): string {
  return saltB64.replace(/[^A-Za-z0-9]/g, "").slice(0, 24) || "default";
}

export async function putVaultCache(
  c: Omit<VaultCache, "version" | "updatedAt">,
): Promise<void> {
  try {
    const db = await openDB();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, "readwrite");
      tx.objectStore(STORE).put(
        {
          ...c,
          version: CACHE_VERSION,
          updatedAt: Date.now(),
        } satisfies VaultCache,
        keyFor(c.userId),
      );
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
    db.close();
  } catch {
    // IndexedDB may be unavailable (private mode, quota); the cache is a hint.
  }
}

export async function getVaultCache(
  userId: string,
): Promise<VaultCache | null> {
  try {
    const db = await openDB();
    const result = await new Promise<VaultCache | null>((resolve, reject) => {
      const tx = db.transaction(STORE, "readonly");
      const req = tx.objectStore(STORE).get(keyFor(userId));
      req.onsuccess = () => resolve((req.result as VaultCache) ?? null);
      req.onerror = () => reject(req.error);
    });
    db.close();
    if (result && result.version !== CACHE_VERSION) return null;
    return result;
  } catch {
    return null;
  }
}

export async function clearVaultCache(userId: string): Promise<void> {
  try {
    const db = await openDB();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, "readwrite");
      tx.objectStore(STORE).delete(keyFor(userId));
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
    db.close();
  } catch {
    // ignore
  }
}
