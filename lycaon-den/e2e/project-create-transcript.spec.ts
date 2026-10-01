import { expect } from "@playwright/test";

import { liveChatStage, webE2e } from "./helpers.ts";

webE2e("new draft shows its starter prompt immediately", async ({ page }) => {
  const prompt = `Build a small security alert triage dashboard ${Date.now()}`;

  await page.goto("/");
  // Wait for the hydrated Home stage.
  await expect(page.getByText("Harness").first()).toBeVisible({ timeout: 30_000 });
  await page.getByTestId("home-idea-input").fill(prompt);
  await page.getByTestId("home-idea-send").click();

  const stream = liveChatStage(page).getByTestId("chat-stream");
  await expect(stream).toBeVisible({ timeout: 60_000 });
  await expect(stream.getByText(prompt, { exact: true })).toBeVisible({
    timeout: 60_000,
  });
});
