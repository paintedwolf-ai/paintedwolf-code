import { expect } from "@playwright/test";
import {
  bootstrapActiveProject,
  liveChatStage,
  openNewChatSession,
  webE2e,
} from "./helpers.ts";

// Covers providerless onboarding through API state.
webE2e.describe("first run without a provider", () => {
  webE2e("den:harness — chat points a provider-less user at setup", async ({
    page,
    request,
  }) => {
    // Isolate provider state to this test.
    await page.route("**/v1/providers", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: { providers: [] } });
        return;
      }
      await route.continue();
    });

    await bootstrapActiveProject(page, request);
    await openNewChatSession(page);

    const banner = liveChatStage(page).getByTestId("no-provider-banner");
    await expect(banner).toBeVisible({ timeout: 90_000 });

    const composerInput = liveChatStage(page).getByTestId("chat-composer");
    await expect(composerInput).toBeDisabled();
    await expect(composerInput).toHaveAttribute(
      "placeholder",
      /AI providers/i,
    );

    await liveChatStage(page).getByTestId("no-provider-open-settings").click();
    await expect(page.getByTestId("providers-settings").first()).toBeVisible({
      timeout: 30_000,
    });
  });
});
