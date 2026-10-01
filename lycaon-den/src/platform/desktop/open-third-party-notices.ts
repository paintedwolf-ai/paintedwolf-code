import { invoke } from "@tauri-apps/api/core";
import { isTauriRuntime } from "../runtime.ts";

/** Open the shipped / generated THIRD-PARTY-NOTICES.md with the OS default app. */
export async function openThirdPartyNotices(): Promise<void> {
  if (!isTauriRuntime()) {
    throw new Error("Third-party license notices are available in the desktop app.");
  }
  await invoke("open_third_party_notices");
}
