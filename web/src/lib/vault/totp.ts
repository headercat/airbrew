// TOTP (RFC 6238) code generation for the password vault view screen.
//
// The stored field value is the shared secret (base32, or an otpauth:// URI).
// The view screen derives the rotating code from it using WebCrypto
// HMAC-SHA1/256/512, exactly like authenticator apps. The secret never leaves
// the browser — only the derived code is shown.

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

function toBuf(u: Uint8Array): BufferSource {
  // See lib/vault/crypto.ts: TS 5.7+ widens Uint8Array to ArrayBufferLike.
  return u as unknown as BufferSource;
}

export type TotpConfig = {
  secret: string;
  period: number;
  digits: number;
  algorithm: "SHA1" | "SHA256" | "SHA512";
  issuer?: string;
  label?: string;
};

// parseTOTPConfig extracts the base32 secret and optional parameters from
// either a raw base32 string or an otpauth://totp/...?secret=...&period=...
// &digits=...&algorithm=... URI. Defaults follow RFC 6238 (SHA-1, 6 digits,
// 30s). Invalid values fall back to the defaults rather than throwing so a
// malformed URI still produces a code.
export function parseTOTPConfig(raw: string): TotpConfig {
  const cfg: TotpConfig = {
    secret: "",
    period: 30,
    digits: 6,
    algorithm: "SHA1",
  };
  const s = raw.trim();
  if (!s.startsWith("otpauth://")) {
    cfg.secret = s;
    return cfg;
  }
  try {
    const url = new URL(s);
    // Label is the path after /totp/: "issuer:account" or "account".
    const label = decodeURIComponent(url.pathname.replace(/^\//, ""));
    if (label) {
      cfg.label = label;
      const colon = label.indexOf(":");
      if (colon > 0) cfg.issuer = label.slice(0, colon).trim();
    }
    const secret = url.searchParams.get("secret");
    if (secret) cfg.secret = secret;
    const issuer = url.searchParams.get("issuer");
    if (issuer) cfg.issuer = issuer;
    const period = url.searchParams.get("period");
    if (period) {
      const n = parseInt(period, 10);
      if (Number.isFinite(n) && n > 0 && n <= 600) cfg.period = n;
    }
    const digits = url.searchParams.get("digits");
    if (digits) {
      const n = parseInt(digits, 10);
      if (n === 6 || n === 7 || n === 8) cfg.digits = n;
    }
    const alg = url.searchParams.get("algorithm");
    if (alg) {
      const norm = alg.toUpperCase();
      if (norm === "SHA256" || norm === "SHA512") cfg.algorithm = norm;
    }
  } catch {
    // fall through with defaults
  }
  return cfg;
}

// parseSecret remains as a thin compatibility wrapper for callers that only
// need the base32 secret.
export function parseSecret(raw: string): string {
  return parseTOTPConfig(raw).secret;
}

// base32Decode decodes RFC 4648 base32 (no padding required), ignoring spaces
// and lowercase. Unlike a lenient decoder it returns null when the input
// contains characters outside the base32 alphabet, so the UI can report an
// invalid secret instead of silently deriving the wrong code.
export function base32Decode(input: string): Uint8Array | null {
  const clean = input.replace(/\s/g, "").toUpperCase().replace(/=+$/, "");
  const out: number[] = [];
  let buf = 0;
  let bits = 0;
  for (const ch of clean) {
    const v = BASE32_ALPHABET.indexOf(ch);
    if (v < 0) return null;
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

// generateTotp computes the current code for a base32 secret or otpauth URI.
// period, digits and algorithm are read from the URI when present, defaulting
// to the RFC 6238 values (30s / 6 digits / SHA-1).
export async function generateTotp(
  input: string,
  at: number = Date.now(),
): Promise<TotpCode> {
  const cfg = parseTOTPConfig(input);
  const keyBytes = base32Decode(cfg.secret);
  if (!keyBytes || keyBytes.length === 0) {
    throw new Error("totp: empty or invalid base32 secret");
  }
  const { period, digits, algorithm } = cfg;
  const counter = Math.floor(at / 1000 / period);
  // 64-bit big-endian counter; counter fits in 32 bits until 2106.
  const counterBuf = new ArrayBuffer(8);
  const view = new DataView(counterBuf);
  view.setUint32(0, Math.floor(counter / 0x100000000));
  view.setUint32(4, counter >>> 0);

  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    toBuf(keyBytes),
    { name: "HMAC", hash: algorithm },
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
