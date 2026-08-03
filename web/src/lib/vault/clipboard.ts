// Clipboard helper with auto-clear.
//
// Copying a vault secret to the clipboard is convenient but leaves it sitting
// in the OS clipboard for any other app to read. copyAndAutoClear writes the
// value and then, after a delay, attempts to read the clipboard back and only
// clears it when it still holds our secret — so we never clobber a value the
// user copied in the meantime. If the read is unavailable (non-HTTPS, missing
// permission, background tab) we do nothing rather than risk overwriting an
// unrelated copy with an empty string.

import { readVaultSecuritySettings } from "@/lib/vault/security";

export async function copyAndAutoClear(
  value: string,
  clearAfterMs = readVaultSecuritySettings().clipboardClearSeconds * 1000,
): Promise<void> {
  if (!value) return;
  try {
    await navigator.clipboard.writeText(value);
  } catch {
    // Clipboard write denied (permissions, non-HTTPS, etc.): nothing to clear.
    return;
  }
  window.setTimeout(() => {
    navigator.clipboard
      .readText()
      .then((current) => {
        // Only overwrite when the clipboard still holds our secret.
        if (current === value) return navigator.clipboard.writeText("");
      })
      .catch(() => {
        // Read rejected — can't tell whether the user copied something else,
        // so leave the clipboard alone rather than risk erasing an unrelated
        // value.
      });
  }, clearAfterMs);
}
