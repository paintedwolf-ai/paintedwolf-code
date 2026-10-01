import { isTauriRuntime } from "../runtime.ts";

let last: number | undefined;
let pending = Promise.resolve();

export async function setWaitingBadge(count: number): Promise<void> {
  if (!isTauriRuntime()) return;
  const next = Math.max(0, Math.floor(count));
  // Serialize writes so an older request cannot replace a newer badge.
  pending = pending.then(async () => {
    if (next === last) return;
    try {
      const { invoke } = await import("@tauri-apps/api/core");
      await invoke("den_set_badge_count", { count: next });
      last = next;
    } catch (err) {
      last = undefined;
      console.debug("[badge] set failed", err);
    }
  });
  return pending;
}
