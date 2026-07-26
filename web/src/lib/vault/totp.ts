// TOTP (RFC 6238) code generation for the password vault view screen.
//
// The stored field value is the shared secret (base32, or an otpauth:// URI).
// The view screen derives the rotating 6-digit code from it using WebCrypto
// HMAC-SHA1, exactly like authenticator apps. The secret never leaves the
// browser — only the derived code is shown.

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

function toBuf(u: Uint8Array): BufferSource {
  // See lib/vault/crypto.ts: TS 5.7+ widens Uint8Array to ArrayBufferLike.
  return u as unknown as BufferSource;
}

// parseSecret extracts the base32 secret from either a raw base32 string or an
// otpauth://totp/...?secret=... URI. Returns "" if none found.
export function parseSecret(raw: string): string {
  const s = raw.trim();
  if (s.startsWith("otpauth://")) {
    try {
      const url = new URL(s);
      const secret = url.searchParams.get("secret");
      if (secret) return secret;
    } catch {
      // fall through
    }
  }
  return s;
}

// base32Decode decodes RFC 4648 base32 (no padding required), ignoring spaces
// and lowercase. Invalid characters are skipped.
export function base32Decode(input: string): Uint8Array {
  const clean = input.replace(/\s/g, "").toUpperCase().replace(/=+$/, "");
  const out: number[] = [];
  let buf = 0;
  let bits = 0;
  for (const ch of clean) {
    const v = BASE32_ALPHABET.indexOf(ch);
    if (v < 0) continue;
    buf = (buf << 5) | v;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      out.push((buf >> bits) & 0xff);
    }
  }
  return new Uint8Array(out);
}

export type TotpCode = { code: string; remaining: number; period: number };

// generateTotp computes the current code for a base32 secret. period defaults
// to 30s and digits to 6, the authenticator-app defaults.
export async function generateTotp(
  secret: string,
  at: number = Date.now(),
  period = 30,
  digits = 6,
): Promise<TotpCode> {
  const keyBytes = base32Decode(parseSecret(secret));
  if (keyBytes.length === 0) {
    throw new Error("totp: empty or invalid secret");
  }
  const counter = Math.floor(at / 1000 / period);
  // 64-bit big-endian counter; counter fits in 32 bits until 2106.
  const counterBuf = new ArrayBuffer(8);
  const view = new DataView(counterBuf);
  view.setUint32(0, Math.floor(counter / 0x100000000));
  view.setUint32(4, counter >>> 0);

  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    toBuf(keyBytes),
    { name: "HMAC", hash: "SHA-1" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(
    await crypto.subtle.sign("HMAC", cryptoKey, counterBuf),
  );
  const offset = mac[mac.length - 1] & 0x0f;
  const bin =
    ((mac[offset] & 0x7f) << 24) |
    ((mac[offset + 1] & 0xff) << 16) |
    ((mac[offset + 2] & 0xff) << 8) |
    (mac[offset + 3] & 0xff);
  const code = (bin % 10 ** digits).toString().padStart(digits, "0");
  const elapsed = Math.floor(at / 1000) % period;
  return { code, remaining: period - elapsed, period };
}
