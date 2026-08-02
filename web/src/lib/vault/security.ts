export const VAULT_SECURITY_SETTINGS_KEY = "airbrew.vault.security";

export type VaultSecuritySettings = {
  autoLockMinutes: number;
  clipboardClearSeconds: number;
};

export const DEFAULT_VAULT_SECURITY_SETTINGS: VaultSecuritySettings = {
  autoLockMinutes: 5,
  clipboardClearSeconds: 30,
};

const AUTO_LOCK_OPTIONS = [1, 5, 15, 30] as const;
const CLIPBOARD_OPTIONS = [15, 30, 60, 120] as const;

export function readVaultSecuritySettings(): VaultSecuritySettings {
  if (typeof localStorage === "undefined") {
    return DEFAULT_VAULT_SECURITY_SETTINGS;
  }
  try {
    const raw = localStorage.getItem(VAULT_SECURITY_SETTINGS_KEY);
    if (!raw) return DEFAULT_VAULT_SECURITY_SETTINGS;
    const parsed = JSON.parse(raw) as Partial<VaultSecuritySettings>;
    return normalizeVaultSecuritySettings(parsed);
  } catch {
    return DEFAULT_VAULT_SECURITY_SETTINGS;
  }
}

export function writeVaultSecuritySettings(
  settings: VaultSecuritySettings,
): VaultSecuritySettings {
  const next = normalizeVaultSecuritySettings(settings);
  localStorage.setItem(VAULT_SECURITY_SETTINGS_KEY, JSON.stringify(next));
  window.dispatchEvent(new CustomEvent("vault-security-settings-changed"));
  return next;
}

export function vaultSecurityOptions() {
  return {
    autoLockMinutes: AUTO_LOCK_OPTIONS,
    clipboardClearSeconds: CLIPBOARD_OPTIONS,
  };
}

function normalizeVaultSecuritySettings(
  settings: Partial<VaultSecuritySettings>,
): VaultSecuritySettings {
  return {
    autoLockMinutes: nearestOption(
      settings.autoLockMinutes,
      AUTO_LOCK_OPTIONS,
      DEFAULT_VAULT_SECURITY_SETTINGS.autoLockMinutes,
    ),
    clipboardClearSeconds: nearestOption(
      settings.clipboardClearSeconds,
      CLIPBOARD_OPTIONS,
      DEFAULT_VAULT_SECURITY_SETTINGS.clipboardClearSeconds,
    ),
  };
}

function nearestOption<T extends readonly number[]>(
  value: unknown,
  options: T,
  fallback: number,
): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return fallback;
  return options.reduce((best, cur) =>
    Math.abs(cur - value) < Math.abs(best - value) ? cur : best,
  );
}

export type PasswordStrength = {
  score: number;
  labelKey: string;
  acceptable: boolean;
  feedbackKeys: string[];
};

const COMMON_PASSWORDS = new Set([
  "password",
  "password1",
  "qwerty",
  "qwerty123",
  "letmein",
  "admin",
  "welcome",
  "iloveyou",
  "airbrew",
]);

export function evaluateMasterPassword(password: string): PasswordStrength {
  const feedbackKeys: string[] = [];
  const lower = password.toLowerCase();
  const uniqueChars = new Set(password).size;
  const classes = [
    /[a-z]/.test(password),
    /[A-Z]/.test(password),
    /\d/.test(password),
    /[^A-Za-z0-9]/.test(password),
  ].filter(Boolean).length;

  let score = 0;
  if (password.length >= 12) score += 1;
  if (password.length >= 15) score += 2;
  if (password.length >= 20) score += 1;
  if (classes >= 3) score += 1;
  if (classes === 4) score += 1;
  if (uniqueChars >= Math.min(10, password.length)) score += 1;
  if (/\s/.test(password) && password.length >= 15) score += 1;

  if (password.length < 15) feedbackKeys.push("passwords.strength.length");
  if (classes < 3) feedbackKeys.push("passwords.strength.variety");
  if (/(.)\1{3,}/.test(password)) {
    score -= 2;
    feedbackKeys.push("passwords.strength.repeated");
  }
  if (/0123|1234|2345|3456|4567|5678|6789|abcd|qwer/i.test(password)) {
    score -= 1;
    feedbackKeys.push("passwords.strength.sequence");
  }
  if (COMMON_PASSWORDS.has(lower)) {
    score = 0;
    feedbackKeys.push("passwords.strength.common");
  }

  score = Math.max(0, Math.min(5, score));
  const acceptable =
    password.length >= 15 &&
    score >= 3 &&
    classes >= 2 &&
    !COMMON_PASSWORDS.has(lower);
  const labelKey =
    score >= 5
      ? "passwords.strength.excellent"
      : score >= 3
        ? "passwords.strength.good"
        : score >= 2
          ? "passwords.strength.fair"
          : "passwords.strength.weak";

  return { score, labelKey, acceptable, feedbackKeys };
}
