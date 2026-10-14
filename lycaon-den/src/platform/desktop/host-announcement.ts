/** Speaks an announcement through the host where live regions do not reach the screen reader. */
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

/**
 * WebKitGTK raises no text-inserted event for a live-region change, and Orca
 * presents live regions only from those events, so on Linux the host is the
 * sole announcement channel. Live regions must stay silent there, so a
 * WebKitGTK that later emits those events cannot make a reader speak twice.
 */
export function hostSpeaksAnnouncements(): boolean {
  return isTauriRuntime() && tauriPlatform() === "linux";
}

export async function announceThroughHost(text: string): Promise<void> {
  if (!text.trim() || !hostSpeaksAnnouncements()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("den_announce", { text });
  } catch {
    /* An announcement the host cannot deliver is dropped; the transcript itself remains readable. */
  }
}
