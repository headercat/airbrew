// KDF Web Worker.
//
// Argon2id (64 MiB / 3 iterations by default) is memory- and CPU-hard; running
// it on the main thread during vault unlock freezes the UI for ~1-2s. This
// worker runs the derivation off the main thread so the unlock form stays
// responsive. crypto.ts delegates deriveMasterKey here via a singleton worker.
//
// The worker only ever holds the master password and derived key transiently
// in its own isolate; they never leave the browser and are not sent to the
// server.

import { argon2id } from "hash-wasm";

export type KdfRequest = {
  id: number;
  password: string;
  // salt is transferred as a Uint8Array (transferable).
  salt: Uint8Array;
  memoryKiB: number;
  iterations: number;
  parallelism: number;
};

self.onmessage = async (e: MessageEvent<KdfRequest>) => {
  const { id, password, salt, memoryKiB, iterations, parallelism } = e.data;
  try {
    const hex = await argon2id({
      password,
      salt,
      parallelism,
      memorySize: memoryKiB,
      iterations,
      hashLength: 32,
    });
    (self as unknown as Worker).postMessage({ id, ok: true, hex });
  } catch (err) {
    (self as unknown as Worker).postMessage({
      id,
      ok: false,
      error: err instanceof Error ? err.message : String(err),
    });
  }
};
