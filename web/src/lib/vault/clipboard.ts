// Clipboard helper with auto-clear.
//
// Copying a vault secret to the clipboard is convenient but leaves it sitting
// in the OS clipboard for any other app to read. copyAndAutoClear writes the
// value and then, after a delay, overwrites the clipboard again so the secret
// does not linger. The clear is best-effort: if the user copied something else
// in the meantime we still overwrite it (acceptable for a security tool), and
// clipboard write can be rejected by browser permissions.

import { readVaultSecuritySettings } from "@/lib/vault/security";

export async function copyAndAutoClear(
  value: string,
  clearAfterMs = readVaultSecuritySettings().clipboardClearSeconds * 1000,
): Promise<void> {
  if (!value) return;
  await navigator.clipboard.writeText(value);
  window.setTimeout(() => {
    // Avoid overwriting a newer user clipboard value when read permission is
    // available. If read fails, fall back to the safer secret-clearing write.
    navigator.clipboard
      .readText()
      .then((current) => {
        if (current === value) return navigator.clipboard.writeText("");
      })
      .catch(() => navigator.clipboard.writeText("").catch(() => {}));
  }, clearAfterMs);
}
