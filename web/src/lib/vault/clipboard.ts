// Clipboard helper with auto-clear.
//
// Copying a vault secret to the clipboard is convenient but leaves it sitting
// in the OS clipboard for any other app to read. copyAndAutoClear writes the
// value and then, after a delay, overwrites the clipboard again so the secret
// does not linger. The clear is best-effort: if the user copied something else
// in the meantime we still overwrite it (acceptable for a security tool), and
// clipboard write can be rejected by browser permissions.

const DEFAULT_CLEAR_AFTER_MS = 30_000;

export async function copyAndAutoClear(
  value: string,
  clearAfterMs = DEFAULT_CLEAR_AFTER_MS,
): Promise<void> {
  if (!value) return;
  await navigator.clipboard.writeText(value);
  window.setTimeout(() => {
    // Only nudge the clipboard clear; ignore failures (permission, tab gone).
    navigator.clipboard.writeText("").catch(() => {});
  }, clearAfterMs);
}
