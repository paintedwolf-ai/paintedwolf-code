import { createSignal } from "solid-js";
import type { ChatVault } from "../../api/types.ts";

/**
 * Each chat's unlock for values the person stored, as the engine last
 * reported it. The engine holds the state; `chat_vault` events and reads
 * replace a chat's entry whole.
 */
const [vaults, setVaults] = createSignal<Readonly<Record<string, ChatVault>>>({});

export function chatVault(chatSessionId: string | undefined): ChatVault | undefined {
  return chatSessionId ? vaults()[chatSessionId] : undefined;
}

export function chatUnlocked(chatSessionId: string | undefined): boolean {
  return chatVault(chatSessionId)?.unlocked === true;
}

export function applyChatVault(state: ChatVault): void {
  setVaults((previous) => ({ ...previous, [state.chat_session_id]: state }));
}

