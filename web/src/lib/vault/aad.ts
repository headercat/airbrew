// AAD (additional authenticated data) labels and helpers for the vault.
//
// Extracted from store.tsx so the crypto-version logic is testable in
// isolation and the store module stays focused on React state. Three crypto
// versions coexist:
//
//  v1 (legacy):   no AAD — ciphertext is bound only to the key.
//  v2 (AAD):      a fixed per-field label (AAD.folderName, AAD.itemName, …).
//  v3 (record):   the label includes the record id so a swapped cipher/nonce
//                 pair cannot decrypt under the wrong row.
//
// The decrypt helpers consult the row's crypto_version to pick the right AAD;
// the encrypt helpers always use v3. A background migration re-encrypts v1/v2
// rows as they are touched.

import { randomBytes } from "@/lib/vault/crypto";

export const RECORD_AAD_CRYPTO_VERSION = 3;
export const CURRENT_CRYPTO_VERSION = RECORD_AAD_CRYPTO_VERSION;

// Legacy (v2) fixed AAD labels.
export const AAD = {
  envelope: "airbrew:vault:envelope-key:v1",
  folderName: "airbrew:vault:folder-name:v1",
  itemName: "airbrew:vault:item-name:v1",
  itemData: "airbrew:vault:item-data:v1",
  itemNotes: "airbrew:vault:item-notes:v1",
  attachmentName: "airbrew:vault:attachment-name:v1",
  attachmentFileKey: "airbrew:vault:attachment-file-key:v1",
  attachmentPayload: "airbrew:vault:attachment-payload:v1",
} as const;

export function isLegacyCrypto(version?: number): boolean {
  return (version ?? 1) < RECORD_AAD_CRYPTO_VERSION;
}

function recordAAD(kind: string, id: string, field: string): string {
  return `airbrew:vault:${kind}:${id}:${field}:v3`;
}

export function folderAAD(
  id: string,
  field: "name",
  version?: number,
): string {
  if ((version ?? 1) >= RECORD_AAD_CRYPTO_VERSION) {
    return recordAAD("folder", id, field);
  }
  return AAD.folderName;
}

export function itemAAD(
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

export function attachmentAAD(
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

// vaultId generates a 21-character ID matching the server's internal/id
// alphabet so client-supplied IDs (crypto v3) are shape-compatible.
const ID_ALPHABET =
  "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function vaultId(): string {
  const bytes = randomBytes(21);
  let out = "";
  for (const b of bytes) out += ID_ALPHABET[b & 63];
  return out;
}
