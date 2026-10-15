/** Speaks an announcement through the host where live regions do not reach the screen reader. */
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

export type AnnouncementPoliteness = "polite" | "assertive";

/**
 * WebKitGTK raises no text-inserted event for a live-region change, and Orca
 * presents live regions only from those events, so on Linux the host is the
 * sole announcement channel and the live-region bridge keeps the regions
 * themselves silent.
 */
export function hostSpeaksAnnouncements(): boolean {
  return isTauriRuntime() && tauriPlatform() === "linux";
}

export async function announceThroughHost(
  text: string,
  politeness: AnnouncementPoliteness = "polite",
): Promise<void> {
  if (!text.trim() || !hostSpeaksAnnouncements()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("den_announce", { text, politeness });
  } catch {
    /* An announcement the host cannot deliver is dropped; the page itself remains readable. */
  }
}
