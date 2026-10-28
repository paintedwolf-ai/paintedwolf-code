import { isTauriRuntime } from "../platform/runtime.ts";

/** How long a copied secret stays available for paste before Den clears it. */
export const SECRET_COPY_LIFETIME_MS = 60_000;

/** Identifies one native clipboard generation without reading its contents. */
export type SecretClipboardCopy = number;

export async function writeSecretClipboardText(text: string): Promise<SecretClipboardCopy> {
  if (!isTauriRuntime()) throw new Error("Secret clipboard is unavailable");
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<number>("write_secret_text", { text });
}

export async function clearSecretClipboardText(copy: SecretClipboardCopy): Promise<void> {
  if (!isTauriRuntime()) return;
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("clear_secret_text", { copy });
}

/**
 * The single clear timer for copied secrets. Remasking never clears; a newer
 * copy restarts the timer, and native ownership decides whether ours is still current.
 */
export function secretClipboardLease(adapter = {
  write: writeSecretClipboardText,
  clear: clearSecretClipboardText,
}) {
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const write = async (text: string): Promise<boolean> => {
    const request = ++generation;
    const copy = await adapter.write(text);
    if (request !== generation) return false;
    clearTimeout(timer);
    timer = setTimeout(() => void adapter.clear(copy).catch(() => {}), SECRET_COPY_LIFETIME_MS);
    return true;
  };
  return { write };
}
