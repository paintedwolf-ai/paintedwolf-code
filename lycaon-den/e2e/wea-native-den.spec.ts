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

type FocusSnapshot = {
  tagName: string;
  testId: string;
  role: string;
  ariaLabel: string;
  key: string;
};

async function readFocusSnapshot(page: import("@playwright/test").Page): Promise<FocusSnapshot> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    const tagName = el?.tagName ?? "";
    const testId = el?.getAttribute("data-testid") ?? "";
    const role = el?.getAttribute("role") ?? "";
    const ariaLabel = el?.getAttribute("aria-label") ?? "";
    return {
      tagName,
      testId,
      role,
      ariaLabel,
      key: `${tagName}|${testId}|${role}|${ariaLabel}`,
    };
  });
}

/**
 * TB02 / D12 Linux native Den keyboard-navigation proof.
 *
 * Qualifies ONLY the real desktop tier (`PLAYWRIGHT_E2E=desktop` under Xvfb via
 * `scripts/e2e-den-desktop.sh`). Refuses to pass on the web tier. Drives a specific
 * keyboard focus transition from the chat composer input to an interactive control,
 * verifies exact before/after focus identity and resulting UI state, reads back
 * session & transcript state from the live API, and captures a screenshot attachment.
 */
webE2e("TB02 native Den desktop keyboard focus transition, API readback, and screenshot capture", async ({ page, request }, testInfo) => {
  // D12: A browser-tier run never satisfies a native desktop slot.
  expect(
    process.env.PLAYWRIGHT_E2E,
    "TB02 native Den spec must run under PLAYWRIGHT_E2E=desktop (never web tier)",
  ).toBe("desktop");

  webE2e.setTimeout(120_000);

  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

  try {
    const prompt = await apiPostPrompt(request, sessionId, {
      text: "TB02 native Den desktop keyboard navigation verification.",
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
      content: "TB02 native Den desktop navigation ready.",
    });
    await waitForSessionReady(request, sessionId);
    const stage = liveChatStage(page);
    await expect(stage.getByTestId("transcript-article-assistant").last()).toContainText(
      "TB02 native Den desktop navigation ready.",
    );

    // Anchor focus on a known starting landmark (composer textarea/input in the active chat stage).
    const composer = stage.locator("textarea, [contenteditable='true'], input[type='text']").first();
    await expect(composer).toBeVisible({ timeout: 15_000 });
    await composer.focus();

    const beforeFocus = await readFocusSnapshot(page);
    expect(["TEXTAREA", "INPUT", "DIV"]).toContain(beforeFocus.tagName);

    // Drive a real keyboard focus transition via Tab and verify focus actually moved to a distinct interactive element.
    await page.keyboard.press("Tab");
    const afterTabFocus = await readFocusSnapshot(page);
    expect(afterTabFocus.tagName).not.toBe("");
    expect(afterTabFocus.tagName).not.toBe("BODY");
    expect(afterTabFocus.key).not.toBe(beforeFocus.key);

    // Reverse transition via Shift+Tab should return focus to the starting landmark.
    await page.keyboard.press("Shift+Tab");
    const afterShiftTabFocus = await readFocusSnapshot(page);
    expect(afterShiftTabFocus.key).toBe(beforeFocus.key);

    // Independent API readback of session & transcript state.
    const session = await apiJson<Session>(request, "GET", `/v1/sessions/${sessionId}`);
    const transcript = await apiJson<{ messages: Message[] }>(
      request,
      "GET",
      `/v1/sessions/${sessionId}/messages`,
    );
    expect(session.id).toBe(sessionId);
    expect(session.status).toBe("idle");
    expect(
      transcript.messages.some(
        (m) => m.role === "assistant" && (m.content ?? "").includes("TB02 native Den desktop navigation ready."),
      ),
    ).toBe(true);

    // Capture desktop window screenshot evidence.
    const screenshotPath = testInfo.outputPath("wea-native-den-linux.png");
    await page.screenshot({ path: screenshotPath, fullPage: false });
    await testInfo.attach("wea-native-den-linux", {
      path: screenshotPath,
      contentType: "image/png",
    });

    const evidence = {
      schema: "wea-native-den-linux-v1",
      tier: process.env.PLAYWRIGHT_E2E,
      sessionId,
      keyboardTransition: {
        before: beforeFocus,
        afterTab: afterTabFocus,
        afterShiftTab: afterShiftTabFocus,
        returnedToOrigin: afterShiftTabFocus.key === beforeFocus.key,
      },
      apiReadback: {
        sessionId: session.id,
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
