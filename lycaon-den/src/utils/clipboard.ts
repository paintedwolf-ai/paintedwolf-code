import { isTauriRuntime } from "../platform/runtime.ts";

/** Distinguishes an empty clipboard from an unreadable one. */
export type ClipboardRead =
  | { readable: true; text: string }
  | { readable: false };

/** Best-effort clipboard write; no-op when unavailable. */
export async function copyTextToClipboard(text: string): Promise<void> {
  try {
    await writeClipboardText(text);
  } catch {
    /* ignore */
  }
}

/** Clipboard write that throws when the platform has no clipboard. */
export async function writeClipboardText(text: string): Promise<void> {
  if (isTauriRuntime()) {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("write_clipboard_text", { text });
    return;
  }
  if (typeof navigator === "undefined" || !navigator.clipboard?.writeText) {
    throw new Error("clipboard unavailable");
  }
  await navigator.clipboard.writeText(text);
}

/** Reads the clipboard, reporting whether the read established anything. */
export async function readClipboardText(): Promise<ClipboardRead> {
  try {
    if (isTauriRuntime()) {
      const { invoke } = await import("@tauri-apps/api/core");
      return { readable: true, text: (await invoke<string>("read_clipboard_text")) ?? "" };
    }
    return { readable: true, text: (await navigator.clipboard.readText()) ?? "" };
  } catch {
    return { readable: false };
  }
}
