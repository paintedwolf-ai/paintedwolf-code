import { isTauriRuntime } from "../platform/runtime.ts";

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

/** A remasked reveal also clears a copy whose native write finishes late. */
export function secretClipboardLease(adapter = {
  write: writeSecretClipboardText,
  clear: clearSecretClipboardText,
}) {
  let generation = 0;
  let owned: SecretClipboardCopy | undefined;
  const clear = () => {
    generation++;
    const copy = owned;
    owned = undefined;
    if (copy !== undefined) void adapter.clear(copy).catch(() => {});
  };
  const write = async (text: string): Promise<boolean> => {
    const request = ++generation;
    const copy = await adapter.write(text);
    if (request !== generation) {
      await adapter.clear(copy);
      return false;
    }
    owned = copy;
    return true;
  };
  return { clear, write };
}
