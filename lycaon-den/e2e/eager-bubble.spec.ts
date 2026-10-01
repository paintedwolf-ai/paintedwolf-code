import { expect } from "@playwright/test";
import {
  apiConfig,
  bootstrapChatSession,
  holdPromptRequest,
  liveChatStage,
  settledChatSessionId,
  waitForChatComposerReady,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";

webE2e.describe("eager bubble", () => {
  webE2e("prompt shows instantly and converges to one bubble", async ({ page }) => {
    await bootstrapChatSession(page);
    const sessionId = await settledChatSessionId(page);
    await waitForSessionReady(page.request, sessionId);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);

    const prompt = "Reply with the word pong";
    const held = await holdPromptRequest(page, prompt);
    await composer.fill(prompt);
    await composer.press("Enter");

    // Keep host placement behind the optimistic assertion.
    await held.intercepted;
    const optimistic = stage
      .getByTestId("pending-send-bubble")
      .filter({ hasText: prompt });
    try {
      await expect(composer).toHaveValue("", { timeout: 30_000 });
      await expect(optimistic).toBeVisible({ timeout: 10_000 });
    } finally {
      held.release();
    }

    // The host echo replaces the optimistic bubble.
    await expect(
      stage.getByTestId("transcript-article-user").filter({
        hasText: prompt,
      }),
    ).toHaveCount(1, { timeout: 30_000 });
    await expect(stage.getByTestId("pending-send-bubble")).toHaveCount(0, {
      timeout: 30_000,
    });
  });

  webE2e("rapid consecutive sends both land, in order", async ({ page }) => {
    await bootstrapChatSession(page);
    const sessionId = await settledChatSessionId(page);
    await waitForSessionReady(page.request, sessionId);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);

    await composer.fill("First of two");
    await composer.press("Enter");
    await expect(composer).toHaveValue("", { timeout: 30_000 });
    await composer.fill("Second of two");
    await composer.press("Enter");
    await expect(composer).toHaveValue("", { timeout: 30_000 });

    // Each prompt remains visible through reconciliation.
    for (const text of ["First of two", "Second of two"]) {
      await expect(
        stage
          .locator('[data-testid="pending-send-bubble"], [data-testid="transcript-article-user"]')
          .filter({ hasText: text })
          .first(),
      ).toBeVisible({ timeout: 10_000 });
    }

    await expect(
      stage.getByTestId("transcript-article-user").filter({ hasText: "First of two" }),
    ).toHaveCount(1, { timeout: 30_000 });
    await expect(
      stage.getByTestId("transcript-article-user").filter({ hasText: "Second of two" }),
    ).toHaveCount(1, { timeout: 30_000 });
    await expect(stage.getByTestId("pending-send-bubble")).toHaveCount(0, {
      timeout: 30_000,
    });
    const userTexts = await stage
      .getByTestId("transcript-article-user")
      .allInnerTexts();
    const first = userTexts.findIndex((t) => t.includes("First of two"));
    const second = userTexts.findIndex((t) => t.includes("Second of two"));
    expect(first).toBeGreaterThanOrEqual(0);
    expect(second).toBeGreaterThan(first);

    const { apiUrl, token } = apiConfig();
    const response = await page.request.get(`${apiUrl}/v1/sessions/${sessionId}/bootstrap`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(response.ok()).toBeTruthy();
    const { transcript } = await response.json() as {
      transcript: { messages: Array<{ role: string; content: string }> };
    };
    expect(transcript.messages.filter((message) => message.role === "user")
      .map((message) => message.content)).toEqual(["First of two", "Second of two"]);
  });
});
