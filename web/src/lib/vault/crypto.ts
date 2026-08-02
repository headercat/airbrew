// Zero-knowledge crypto layer for the password vault.
//
// The server only ever sees ciphertext. This module derives the master key
// from the user's master password (Argon2id, memory-hard), uses it to unwrap
// the stored vault key, and provides AES-256-GCM encrypt/decrypt for every
// item field. All primitives run in the browser: argon2id via hash-wasm and
// authenticated encryption via WebCrypto SubtleCrypto.
//
// Key hierarchy (see docs/passwords.md):
//   master password --Argon2id(salt)--> master key (32 B, memory only)
//   master key --AES-GCM--> wraps the vault key (envelope, synced)
//   vault key  --AES-GCM--> encrypts item name/data/notes

import { argon2id } from "hash-wasm";

export type KdfParams = {
  memoryKiB: number;
  iterations: number;
  parallelism: number;
};

// Defaults used at first-time setup. The server stores the chosen params
// per-user, so unlock always reads them back from the envelope.
export const DEFAULT_KDF_PARAMS: KdfParams = {
  memoryKiB: 65536, // 64 MiB
  iterations: 3,
  parallelism: 2,
};

const enc = new TextEncoder();
const dec = new TextDecoder();

// ---- encoding helpers ----

// bytesToB64 / b64ToBytes use the native base64 helpers with bulk conversion
// (faster than the previous per-byte charCode loop on large attachment blobs).
export function bytesToB64(bytes: Uint8Array): string {
  let bin = "";
  const chunk = 0x8000; // btoa accepts at most 8192 chars per call
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(bin);
}

export function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hex.substring(i * 2, i * 2 + 2), 16);
  }
  return out;
}

export function randomBytes(n: number): Uint8Array {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return b;
}

// ---- key derivation ----

// Worker singleton (lazily created). The worker keeps the heavy Argon2id pass
// off the main thread so unlock doesn't freeze the UI. We fall back to an
// in-line argon2id call if the worker can't be created (e.g. CSP / tests).
let kdfWorker: Worker | null = null;
let nextKdfId = 1;

function getKdfWorker(): Worker | null {
  if (kdfWorker) return kdfWorker;
  try {
    kdfWorker = new Worker(new URL("./kdf.worker.ts", import.meta.url), {
      type: "module",
    });
    return kdfWorker;
  } catch {
    return null;
  }
}

// deriveMasterKey runs Argon2id over the master password with the per-user
// salt and params, returning a 32-byte key. The result never leaves memory
// and is never sent to the server. Runs in a Web Worker when available.
export async function deriveMasterKey(
  password: string,
  saltB64: string,
  params: KdfParams,
): Promise<Uint8Array> {
  const salt = b64ToBytes(saltB64);
  const worker = getKdfWorker();
  if (worker) {
    const id = nextKdfId++;
    // Copy the salt so it is backed by a transferable buffer.
    const saltCopy = new Uint8Array(salt);
    return new Promise<Uint8Array>((resolve, reject) => {
      const onMessage = (e: MessageEvent) => {
        const msg = e.data as { id: number; ok: boolean; hex?: string; error?: string };
        if (msg.id !== id) return;
        worker.removeEventListener("message", onMessage);
        if (msg.ok && msg.hex) resolve(hexToBytes(msg.hex));
        else reject(new Error(msg.error ?? "kdf worker failed"));
      };
      worker.addEventListener("message", onMessage);
      worker.postMessage(
        {
          id,
          password,
          salt: saltCopy,
          memoryKiB: params.memoryKiB,
          iterations: params.iterations,
          parallelism: params.parallelism,
        },
        [saltCopy.buffer],
      );
    });
  }
  // Fallback (no worker available): run inline.
  const hex = await argon2id({
    password,
    salt,
    parallelism: params.parallelism,
    memorySize: params.memoryKiB,
    iterations: params.iterations,
    hashLength: 32,
  });
  return hexToBytes(hex);
}

export function generateVaultKey(): Uint8Array {
  return randomBytes(32);
}

export function generateSalt(): string {
  return bytesToB64(randomBytes(16));
}

// generatePassword produces a cryptographically random password of the given
// length over alphabet. It uses rejection sampling to avoid the modulo bias of
// "buf[i] % alphabet.length": values >= the largest multiple of the alphabet
// size are discarded and re-rolled, so every character is uniformly chosen.
const PASSWORD_ALPHABET =
  "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*";

export function generatePassword(len = 20, alphabet = PASSWORD_ALPHABET): string {
  const max = Math.floor(0xffffffff / alphabet.length) * alphabet.length;
  const buf32 = new Uint32Array(len);
  let out = "";
  for (let i = 0; i < len; ) {
    crypto.getRandomValues(buf32);
    for (let j = 0; j < buf32.length && i < len; j++) {
      const r = buf32[j];
      if (r < max) {
        out += alphabet[r % alphabet.length];
        i++;
      }
    }
  }
  return out;
}

// ---- AES-256-GCM via WebCrypto ----

// Our Uint8Arrays are always ArrayBuffer-backed, but TS 5.7+ lib.dom widens
// Uint8Array to ArrayBufferLike, which WebCrypto's BufferSource rejects.
function buf(u: Uint8Array): BufferSource {
  return u as unknown as BufferSource;
}

function gcm(nonce: Uint8Array, aad?: string): AesGcmParams {
  const params: AesGcmParams = { name: "AES-GCM", iv: buf(nonce) };
  if (aad) params.additionalData = buf(enc.encode(aad));
  return params;
}

async function importAesKey(raw: Uint8Array): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", buf(raw), { name: "AES-GCM" }, false, [
    "encrypt",
    "decrypt",
  ]);
}

export type Cipher = { cipher: string; nonce: string };

// encryptString encrypts utf-8 plaintext under key, returning base64 cipher
// and a fresh 12-byte random nonce.
export async function encryptString(
  key: Uint8Array,
  plaintext: string,
  aad?: string,
): Promise<Cipher> {
  const cryptoKey = await importAesKey(key);
  const nonce = randomBytes(12);
  const ct = await crypto.subtle.encrypt(
    gcm(nonce, aad),
    cryptoKey,
    buf(enc.encode(plaintext)),
  );
  return { cipher: bytesToB64(new Uint8Array(ct)), nonce: bytesToB64(nonce) };
}

export async function decryptString(
  key: Uint8Array,
  cipher: string,
  nonce: string,
  aad?: string,
): Promise<string> {
  const cryptoKey = await importAesKey(key);
  const pt = await crypto.subtle.decrypt(
    gcm(b64ToBytes(nonce), aad),
    cryptoKey,
    buf(b64ToBytes(cipher)),
  );
  return dec.decode(new Uint8Array(pt));
}

// encryptBytes wraps a raw key (used for the vault-key envelope). Same scheme
// as encryptString but for arbitrary bytes.
export async function encryptBytes(
  key: Uint8Array,
  data: Uint8Array,
  aad?: string,
): Promise<Cipher> {
  const cryptoKey = await importAesKey(key);
  const nonce = randomBytes(12);
  const ct = await crypto.subtle.encrypt(
    gcm(nonce, aad),
    cryptoKey,
    buf(data),
  );
  return { cipher: bytesToB64(new Uint8Array(ct)), nonce: bytesToB64(nonce) };
}

export async function decryptBytes(
  key: Uint8Array,
  cipher: string,
  nonce: string,
  aad?: string,
): Promise<Uint8Array> {
  const cryptoKey = await importAesKey(key);
  const pt = await crypto.subtle.decrypt(
    gcm(b64ToBytes(nonce), aad),
    cryptoKey,
    buf(b64ToBytes(cipher)),
  );
  return new Uint8Array(pt);
}

// seal encrypts data and prepends the 12-byte nonce, returning a single
// self-describing blob (nonce || ciphertext). Used for attachment payloads,
// which are stored as opaque bytes and need no separate nonce column.
export async function seal(
  key: Uint8Array,
  data: Uint8Array,
  aad?: string,
): Promise<Uint8Array> {
  const cryptoKey = await importAesKey(key);
  const nonce = randomBytes(12);
  const ct = await crypto.subtle.encrypt(
    gcm(nonce, aad),
    cryptoKey,
    buf(data),
  );
  const cipherBytes = new Uint8Array(ct);
  const out = new Uint8Array(12 + cipherBytes.length);
  out.set(nonce, 0);
  out.set(cipherBytes, 12);
  return out;
}

// open is the inverse of seal: it splits the leading 12-byte nonce off and
// decrypts the remainder.
export async function open(
  key: Uint8Array,
  sealed: Uint8Array,
  aad?: string,
): Promise<Uint8Array> {
  if (sealed.length < 13) throw new Error("open: payload too short");
  const cryptoKey = await importAesKey(key);
  const nonce = sealed.slice(0, 12);
  const cipherBytes = sealed.slice(12);
  const pt = await crypto.subtle.decrypt(
    gcm(nonce, aad),
    cryptoKey,
    buf(cipherBytes),
  );
  return new Uint8Array(pt);
}
