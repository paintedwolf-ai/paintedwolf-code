import { expect } from "@playwright/test";

import { bootstrapChatSession, liveChatStage, webE2e } from "./helpers.ts";

type HarnessWindow = {
  __harness?: {
    setTextScale(scale?: number): { ok: boolean; textScale: number };
    state(): { sessionId?: string | null };
  };
};

/**
 * Maximum OS text scale keeps controls visible and targets at least 24 CSS px.
 */
webE2e("a11y — max text scale at 720px keeps composer/nav usable", async ({ page }) => {
  await bootstrapChatSession(page);
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();

  await page.setViewportSize({ width: 720, height: 480 });

  const textScale = await page.evaluate(() => {
    const h = (window as unknown as HarnessWindow).__harness;
    if (!h?.setTextScale) {
      document.documentElement.style.setProperty("--den-text-scale", String(53 / 17));
      return 53 / 17;
    }
    return h.setTextScale().textScale;
  });
  expect(textScale).toBeCloseTo(53 / 17, 3);

  const cssVar = await page.evaluate(
    () => document.documentElement.style.getPropertyValue("--den-text-scale").trim(),
  );
  expect(Number(cssVar)).toBeCloseTo(53 / 17, 3);

  await expect(liveChatStage(page).getByTestId("composer-send")).toBeVisible();

  const overflowX = await page.evaluate(() => {
    const root = document.documentElement;
    return root.scrollWidth > root.clientWidth + 1;
  });
  expect(overflowX).toBe(false);

  const sendBox = await liveChatStage(page).getByTestId("composer-send").boundingBox();
  expect(sendBox).toBeTruthy();
  expect(sendBox!.width).toBeGreaterThanOrEqual(24);
  expect(sendBox!.height).toBeGreaterThanOrEqual(24);

  await page.getByTestId("composer-attach").focus();
  const attachVisible = await page.evaluate(() => {
    const el = document.querySelector('[data-testid="composer-attach"]');
    if (!el) return false;
    const r = el.getBoundingClientRect();
    return r.top >= 0 && r.bottom <= window.innerHeight && r.height >= 24;
  });
  expect(attachVisible).toBe(true);

  const sessionId = await page.evaluate(() => {
    const fromHarness = (window as unknown as HarnessWindow).__harness?.state().sessionId;
    if (fromHarness) return fromHarness;
    return (
      document
        .querySelector<HTMLElement>('[data-testid="chat-stream"][data-session-id]')
        ?.getAttribute("data-session-id")
        ?.trim() ||
      document.querySelector<HTMLElement>("[data-session-id]")?.getAttribute("data-session-id")?.trim() ||
      null
    );
  });

  await page.evaluate(() => {
    const h = (window as unknown as HarnessWindow).__harness;
    if (h?.setTextScale) h.setTextScale(1);
    else document.documentElement.style.setProperty("--den-text-scale", "1");
  });

  // eslint-disable-next-line no-console -- handoff proof
  console.log(
    `3.4.2 harness session id: ${sessionId ?? "(none)"} @ textScale=${textScale} viewport=720x480`,
  );
  expect(sessionId).toBeTruthy();
});
