import { expect } from "@playwright/test";
import type { Message, Session } from "../src/api/types.ts";
import {
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";

type PendingTurn = { pending: boolean; id: string; session_id: string };

/**
 * TB02 / D12 Linux native Den keyboard-navigation proof.
 *
 * Runs against the real desktop window under Xvfb (`PLAYWRIGHT_E2E=desktop` via
 * `scripts/e2e-den-desktop.sh`), drives keyboard navigation, reads back state
 * directly from the engine API, and captures a screenshot attachment.
 */
webE2e("TB02 native Den keyboard navigation, API readback, and screenshot capture", async ({ page, request }, testInfo) => {
  webE2e.setTimeout(120_000);

  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

  try {
    const prompt = await apiPostPrompt(request, sessionId, {
      text: "TB02 native Den keyboard navigation verification.",
    });
    expect(prompt.ok(), await prompt.text()).toBe(true);

    const pending = await apiJson<PendingTurn>(
      request,
      "GET",
      `/harness/llm/pending?wait=30000&session_id=${sessionId}`,
    );
    expect(pending.pending).toBe(true);

    await apiJson(request, "POST", "/harness/llm/respond", {
      id: pending.id,
      content: "TB02 native Den navigation ready.",
    });
    await waitForSessionReady(request, sessionId);
    await expect(
      liveChatStage(page).getByTestId("transcript-article-assistant").last(),
    ).toContainText("TB02 native Den navigation ready.");

    // Exercise real keyboard navigation in the desktop window.
    await page.keyboard.press("Tab");
    const firstFocused = await page.evaluate(() => document.activeElement?.tagName ?? "");
    await page.keyboard.press("Tab");
    const secondFocused = await page.evaluate(() => document.activeElement?.tagName ?? "");
    await page.keyboard.press("Escape");

    expect(firstFocused.length).toBeGreaterThan(0);
    expect(secondFocused.length).toBeGreaterThan(0);

    // Independent API readback of session & transcript state.
    const session = await apiJson<Session>(request, "GET", `/v1/sessions/${sessionId}`);
    const transcript = await apiJson<{ messages: Message[] }>(
      request,
      "GET",
      `/v1/sessions/${sessionId}/messages`,
    );
    expect(session.id).toBe(sessionId);
    expect(transcript.messages.some((m) => m.role === "assistant")).toBe(true);

    // Capture desktop window screenshot evidence.
    const screenshotPath = testInfo.outputPath("wea-native-den-linux.png");
    await page.screenshot({ path: screenshotPath, fullPage: false });
    await testInfo.attach("wea-native-den-linux", {
      path: screenshotPath,
      contentType: "image/png",
    });

    const evidence = {
      schema: "wea-native-den-linux-v1",
      tier: process.env.PLAYWRIGHT_E2E ?? "web",
      sessionId,
      keyboard: { firstFocused, secondFocused },
      apiReadback: {
        sessionStatus: session.status,
        messageCount: transcript.messages.length,
      },
      screenshotPath,
    };
    console.log("WEA_NATIVE_DEN_EVIDENCE:" + JSON.stringify(evidence));
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." }).catch(() => undefined);
    const pending = await apiJson<PendingTurn>(request, "GET", "/harness/llm/pending").catch(() => undefined);
    if (pending?.pending) {
      await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Done." }).catch(() => undefined);
    }
  }
});
