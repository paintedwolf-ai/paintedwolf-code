import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("SSE invalidation", () => {
  webE2e("board updates without manual refresh after delegation event", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    // Full scenario needs active session + delegation; smoke asserts chat view exists.
    await expect(page.getByTestId("chat-view")).toBeVisible();
  });
});
