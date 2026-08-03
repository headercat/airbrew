// Password health analysis for the vault.
//
// Because the server only sees ciphertext, health checks run entirely on the
// decrypted client cache. Three categories are reported:
//
//  - weak:   password fields that are too short, use a single character class,
//            or match a list of common passwords.
//  - reused: the same password value appearing on two or more items (each
//            reuse widens the blast radius if one site leaks it).
//  - old:    items whose password has not been updated in a long time, so the
//            user is nudged to rotate.
//
// The checks are intentionally conservative and heuristic — they never send
// data to the server and are meant as a nudge, not a verdict.

import type { DecryptedItem } from "@/lib/vault/store";

const COMMON_PASSWORDS = new Set([
  "password",
  "password1",
  "123456",
  "12345678",
  "qwerty",
  "abc123",
  "11111111",
  "iloveyou",
  "admin",
  "welcome",
  "letmein",
  "monkey",
  "dragon",
  "sunshine",
  "princess",
  "football",
  "baseball",
  "master",
  "login",
  "starwars",
]);

export type StrengthVerdict = "weak" | "fair" | "strong";

export type WeakEntry = {
  item: DecryptedItem;
  password: string;
  verdict: StrengthVerdict;
  reasons: string[];
};

export type ReusedEntry = {
  password: string;
  items: DecryptedItem[];
};

export type OldEntry = {
  item: DecryptedItem;
  ageDays: number;
};

export type HealthReport = {
  weak: WeakEntry[];
  reused: ReusedEntry[];
  old: OldEntry[];
  totalPasswords: number;
};

// oldPasswordDays is the cutoff (in days) after which a password is flagged as
// "old". Mirrors the common 1-year rotation guidance.
const oldPasswordDays = 365;

export function evaluatePasswordStrength(password: string): {
  verdict: StrengthVerdict;
  reasons: string[];
} {
  const reasons: string[] = [];
  if (!password) {
    return { verdict: "weak", reasons: ["empty"] };
  }
  if (COMMON_PASSWORDS.has(password.toLowerCase())) {
    return { verdict: "weak", reasons: ["common"] };
  }
  const classes = [
    /[a-z]/.test(password),
    /[A-Z]/.test(password),
    /\d/.test(password),
    /[^A-Za-z0-9]/.test(password),
  ].filter(Boolean).length;
  if (password.length < 8) reasons.push("short");
  if (password.length < 12 && classes < 3) reasons.push("low-variety");
  if (/(.)\1{3,}/.test(password)) reasons.push("repeated");
  if (/0123|1234|2345|3456|4567|5678|6789|abcd|qwer/i.test(password)) {
    reasons.push("sequence");
  }
  const score =
    (password.length >= 12 ? 1 : 0) +
    (password.length >= 16 ? 1 : 0) +
    (classes >= 3 ? 1 : 0) +
    (classes === 4 ? 1 : 0) -
    (reasons.length > 0 ? 1 : 0);
  const verdict: StrengthVerdict =
    score >= 3 ? "strong" : score >= 1 ? "fair" : "weak";
  return { verdict, reasons: reasons.length ? reasons : ["low-entropy"] };
}

// collectPasswords returns every non-empty password field across the vault,
// keyed by the item it belongs to. Fields named "CVV" or "CVC" are skipped so
// a card's security code is not mistaken for a login password.
function collectPasswords(
  items: DecryptedItem[],
): { item: DecryptedItem; password: string }[] {
  const out: { item: DecryptedItem; password: string }[] = [];
  for (const it of items) {
    if (it.reprompt) continue; // hidden — cannot evaluate
    for (const f of it.fields) {
      if (f.kind !== "password" || !f.value) continue;
      const name = f.name.toLowerCase();
      if (name === "cvv" || name === "cvc" || name === "security code")
        continue;
      out.push({ item: it, password: f.value });
    }
  }
  return out;
}

export function analyzeVaultHealth(items: DecryptedItem[]): HealthReport {
  const pwds = collectPasswords(items);
  const weak: WeakEntry[] = [];
  const byValue = new Map<string, DecryptedItem[]>();
  const old: OldEntry[] = [];
  const now = Date.now();

  for (const { item, password } of pwds) {
    const { verdict, reasons } = evaluatePasswordStrength(password);
    if (verdict === "weak") {
      weak.push({ item, password, verdict, reasons });
    }
    const group = byValue.get(password);
    if (group) group.push(item);
    else byValue.set(password, [item]);

    const ageDays = Math.floor(
      (now - new Date(item.updatedAt).getTime()) / 86_400_000,
    );
    if (ageDays >= oldPasswordDays) {
      old.push({ item, ageDays });
    }
  }

  const reused: ReusedEntry[] = [];
  for (const [password, group] of byValue) {
    if (group.length >= 2) reused.push({ password, items: group });
  }

  old.sort((a, b) => b.ageDays - a.ageDays);

  return { weak, reused, old, totalPasswords: pwds.length };
}
