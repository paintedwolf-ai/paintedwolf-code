import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatVault } from "../../api/types.ts";
import { applyChatVault } from "../../chat/vault/chat-vault-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { createComposerVaultUnlock, VaultUnlockButton } from "./ComposerVaultUnlock.tsx";

const CHAT = "11111111-1111-4111-8111-111111111111";

function unlocked(): ChatVault {
  const now = Date.now();
  return {
    chat_session_id: CHAT, unlocked: true, unlocked_at: new Date(now).toISOString(),
    closes_at: new Date(now + 15 * 60_000).toISOString(), expires_at: new Date(now + 4 * 3_600_000).toISOString(),
  };
}

function setup(get: () => Promise<ChatVault>) {
  const lockChatVault = vi.fn(async () => ({ chat_session_id: CHAT, unlocked: false }));
  const client = stubClient({ getChatVault: vi.fn(get), lockChatVault });
  render(() => {
    const state = createComposerVaultUnlock({
      client: () => client, connected: () => true, sessionId: () => CHAT, revision: () => "r",
    });
    return <VaultUnlockButton state={state} />;
  });
  return { lockChatVault };
}

afterEach(() => {
  cleanup();
  resetSurfaceQueriesForTests();
  applyChatVault({ chat_session_id: CHAT, unlocked: false });
});

describe("composer vault unlock", () => {
  it("shows an unlocked chat and locks it on request", async () => {
    const h = setup(async () => unlocked());
    const button = await screen.findByTestId("composer-vault-unlock");
    expect(button.getAttribute("aria-label")).toContain("unlocked for this chat until");
    fireEvent.click(button);
    expect(h.lockChatVault).toHaveBeenCalledWith(CHAT);
    await waitFor(() => expect(screen.queryByTestId("composer-vault-unlock")).toBeNull());
  });

  it("follows the engine's events after the first read", async () => {
    setup(async () => ({ chat_session_id: CHAT, unlocked: false }));
    await waitFor(() => expect(screen.queryByTestId("composer-vault-unlock")).toBeNull());
    applyChatVault(unlocked());
    await screen.findByTestId("composer-vault-unlock");
    applyChatVault({ chat_session_id: CHAT, unlocked: false });
    await waitFor(() => expect(screen.queryByTestId("composer-vault-unlock")).toBeNull());
  });
});
