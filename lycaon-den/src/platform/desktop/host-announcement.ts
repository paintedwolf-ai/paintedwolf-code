/** Speaks an announcement through the host where live regions do not reach the screen reader. */
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

/**
 * WebKitGTK raises no accessibility event for a live-region change, so Orca
 * never hears one; the Linux host speaks it instead. Elsewhere the live region
 * alone announces.
 */
export async function announceThroughHost(text: string): Promise<void> {
  if (!text.trim() || !isTauriRuntime() || tauriPlatform() !== "linux") return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("den_announce", { text });
  } catch {
    /* The live region remains the announcement of record. */
  }
}
