import { expect, type Page } from "@playwright/test";
import type { HarnessApi } from "../src/platform/harness/harness-driver.ts";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  holdPromptRequest,
  liveChatStage,
  openNewChatSession,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
  waitForChatComposerReady,
  webE2e,
} from "./helpers.ts";

async function manualReplies(page: Page) {
  await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.manual());
}

async function finishReply(page: Page) {
  await page.evaluate(async () => {
    const llm = (window as unknown as { __harness: HarnessApi }).__harness.llm;
    await llm.pending(30_000);
    await llm.auto("Finished.");
    await llm.respond({ text: "Finished." });
  });
}

async function tailDistance(page: Page) {
  return liveChatStage(page).locator(".den-chat-stream").evaluate((host) => {
    const body = host.querySelector<HTMLElement>(".den-chat-stream-body");
    const end = host.querySelector<HTMLElement>("[data-transcript-end]");
    if (!body || !end) throw new Error("Missing transcript tail");
    return host.getBoundingClientRect().bottom - end.getBoundingClientRect().bottom -
      (Number.parseFloat(getComputedStyle(body).paddingBottom) || 0);
  });
}

async function readerPosition(page: Page) {
  return liveChatStage(page).locator(".den-chat-stream").evaluate((host) => {
    const top = host.getBoundingClientRect().top;
    const row = [...host.querySelectorAll<HTMLElement>("[data-msg-id]")]
      .find((node) => node.getBoundingClientRect().bottom > top);
    return row ? { key: row.dataset.msgId, offset: top - row.getBoundingClientRect().top } : null;
  });
}

webE2e.describe("pending transcript time", () => {
  webE2e.use({ timezoneId: "UTC" });
  webE2e.afterEach(async ({ page }) => {
    await page.evaluate(async () => {
      const harness = (window as unknown as { __harness?: HarnessApi }).__harness;
      if (harness?.llm) await harness.llm.auto("Finished.");
    });
  });
  for (const scenario of ["new chat", "finished turn", "multiline draft", "scrolled chat"] as const) {
    webE2e(`submitted bubble stays fixed through confirmation from a ${scenario}`, async ({ page, request }, testInfo) => {
      await bootstrapChatSession(page, request);
      const stage = liveChatStage(page);
      const composer = await waitForChatComposerReady(page);
      await manualReplies(page);
      if (scenario !== "new chat") {
        await composer.fill("Earlier turn.");
        await composer.press("Enter");
        await page.evaluate(async () => {
          const llm = (window as unknown as { __harness: HarnessApi }).__harness.llm;
          await llm.pending(30_000);
          const text = "Earlier answer.\n\n" + "History that fills the chat viewport. ".repeat(300);
          await llm.auto(text);
          await llm.respond({ text });
          await (window as unknown as { __harness: HarnessApi }).__harness.waitForIdle();
        });
        await expect(stage.getByTestId("turn-tail").first()).not.toHaveAttribute("aria-hidden", "true");
        await expect(stage.getByTestId("thinking-indicator")).toHaveCount(0);
        await manualReplies(page);
      }
      if (scenario === "scrolled chat") {
        const stream = stage.locator(".den-chat-stream");
        await stream.focus();
        await page.keyboard.press("Home");
        await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBe(0);
      }
      const marker = "Keep this message in place.";
      const prompt = scenario === "multiline draft" ? Array(8).fill(marker).join("\n") : marker;
      const held = await holdPromptRequest(page, prompt);
      await composer.fill(prompt);
      const sampling = stage.evaluate(async (root, text) => {
        const samples: { top: number; height: number; pending: boolean; composerHeight: number; offset: number }[] = [];
        const until = performance.now() + 30_000;
        let confirmedFrames = 0;
        while (performance.now() < until && confirmedFrames < 30) {
          // Resize observers settle geometry after animation callbacks, before paint.
          await new Promise<void>(resolve => requestAnimationFrame(() => setTimeout(resolve, 0)));
          const bubble = [...root.querySelectorAll<HTMLElement>(".bubble--user")]
            .find(element => element.textContent?.includes(text));
          if (!bubble) continue;
          const rect = bubble.getBoundingClientRect();
          const pending = bubble.dataset.testid === "pending-send-bubble";
          samples.push({
            top: rect.top, height: rect.height, pending,
            composerHeight: root.querySelector(".den-composer")?.getBoundingClientRect().height ?? 0,
            offset: root.querySelector(".den-chat-stream")?.scrollTop ?? 0,
          });
          if (!pending) confirmedFrames += 1;
        }
        return samples;
      }, marker);
      try {
        await composer.press("Enter");
        await held.intercepted;
        await expect(stage.getByTestId("pending-send-bubble")).toBeVisible();
        await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
      } finally {
        held.release();
      }
      await expect(stage.getByTestId("transcript-article-user").filter({ hasText: prompt })).toBeVisible();
      const samples = await sampling;
      await testInfo.attach("submission-layout", { body: JSON.stringify(samples, null, 2), contentType: "application/json" });
      expect(samples.some(sample => sample.pending)).toBe(true);
      expect(samples.some(sample => !sample.pending)).toBe(true);
      expect(Math.max(...samples.map(sample => sample.top)) - Math.min(...samples.map(sample => sample.top))).toBeLessThan(2);
      expect(Math.max(...samples.map(sample => sample.height)) - Math.min(...samples.map(sample => sample.height))).toBeLessThan(2);
      await finishReply(page);
    });
  }
  for (const scenario of ["first send", "new day", "quiet hour"] as const) {
    webE2e(`${scenario} has its final date spacing before confirmation`, async ({ page, request }, testInfo) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      await manualReplies(page);
      let markerTestId = "transcript-day";
      if (scenario !== "first send") {
        const age = scenario === "new day" ? 48 * 60 * 60_000 : 61 * 60_000;
        const now = new Date();
        const previous = new Date(now.getTime() - age);
        if (scenario === "quiet hour" && previous.toISOString().slice(0, 10) === now.toISOString().slice(0, 10)) {
          markerTestId = "transcript-gap-time";
        }
        await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
          id: crypto.randomUUID(), role: "assistant", content: "Earlier conversation.",
          created_at: previous.toISOString(), seq: 1, ord: 1,
        }]));
        await expect(liveChatStage(page).getByText("Earlier conversation.", { exact: true })).toBeVisible();
      }
      const stage = liveChatStage(page);
      const composer = await waitForChatComposerReady(page);
      const prompt = `Check ${scenario}.`;
      const held = await holdPromptRequest(page, prompt);
      await composer.fill(prompt);
      await composer.press("Enter");
      await held.intercepted;
      const pending = stage.getByTestId("pending-send-bubble");
      let operationId: string | null = null;
      let before: { top: number; height: number; gap: number } | null = null;
      try {
        await expect(pending).toBeVisible();
        operationId = await pending.getAttribute("data-operation-id");
        await expect(stage.getByTestId(markerTestId).last()).toBeVisible();
        before = await pending.evaluate((bubble) => {
          const row = bubble.closest<HTMLElement>(".transcript-viewport-row");
          const previous = row?.previousElementSibling;
          if (!row || !previous) throw new Error("Pending row has no time marker");
          const bounds = bubble.getBoundingClientRect();
          return { top: bounds.top, height: bounds.height, gap: bounds.top - previous.getBoundingClientRect().bottom };
        });
        await page.screenshot({ path: testInfo.outputPath(`${scenario.replaceAll(" ", "-")}-pending.png`) });
      } finally {
        held.release();
      }
      if (!operationId || !before) throw new Error("Pending row was not measured");
      const confirmed = stage.locator(`[data-msg-id="${operationId}"]`).getByTestId("transcript-article-user");
      await expect(confirmed).toBeVisible();
      await expect(pending).toHaveCount(0);
      const after = await confirmed.evaluate((bubble) => {
        const previous = bubble.closest(".transcript-viewport-row")?.previousElementSibling;
        if (!previous) throw new Error("Confirmed row has no time marker");
        const bounds = bubble.getBoundingClientRect();
        return { top: bounds.top, height: bounds.height, gap: bounds.top - previous.getBoundingClientRect().bottom };
      });
      expect(Math.abs(after.height - before.height)).toBeLessThanOrEqual(1);
      expect(Math.abs(after.top - before.top)).toBeLessThanOrEqual(1);
      expect(Math.abs(after.gap - before.gap)).toBeLessThanOrEqual(1);
      await finishReply(page);
      await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
    });
  }

  webE2e("a failed first send removes its date and a retry follows with reduced motion", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await manualReplies(page);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);
    const prompt = "Retry this message.";
    let fail = true;
    await page.route("**/v1/sessions/*/prompts", async (route) => {
      if (fail) {
        fail = false;
        await route.abort("failed");
      } else {
        await route.continue();
      }
    });
    await composer.fill(prompt);
    await composer.press("Enter");
    await expect(composer).toHaveValue(prompt);
    await expect(stage.getByTestId("pending-send-bubble")).toHaveCount(0);
    await expect(stage.getByTestId("transcript-day")).toHaveCount(0);
    const held = await holdPromptRequest(page, prompt);
    await composer.press("Enter");
    await held.intercepted;
    try {
      await expect(stage.getByTestId("pending-send-bubble")).toBeVisible();
      await expect(stage.getByTestId("transcript-day")).toHaveCount(1);
    } finally {
      held.release();
    }
    await expect(stage.getByTestId("transcript-article-user").filter({ hasText: prompt })).toHaveCount(1);
    await expect(stage.getByTestId("transcript-day")).toHaveCount(1);
    await finishReply(page);
    await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
  });

  webE2e("scrolling away during a delayed send survives confirmation and subsequent output", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages(
      Array.from({ length: 100 }, (_, i) => ({
        id: crypto.randomUUID(), role: i % 2 ? "assistant" as const : "user" as const,
        content: `History ${i}: ${"Keep this reading position. ".repeat(15)}`,
        created_at: new Date(Date.now() - 60_000 + i).toISOString(), ord: i + 1, seq: i + 1,
      })),
    ));
    await manualReplies(page);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);
    const held = await holdPromptRequest(page, "Delayed send.");
    await composer.fill("Delayed send.");
    await composer.press("Enter");
    await held.intercepted;
    let before: Awaited<ReturnType<typeof readerPosition>> = null;
    try {
      await expect(stage.getByTestId("pending-send-bubble")).toBeVisible();
      await expect.poll(async () => Math.abs(await tailDistance(page)) <= 2).toBe(true);
      const box = await stage.locator(".den-chat-stream").boundingBox();
      if (!box) throw new Error("Missing scrollport");
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      for (let step = 0; step < 8; step += 1) await page.mouse.wheel(0, -400);
      await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
      await expect.poll(() => stage.locator(".den-chat-stream").evaluate(async (host) => {
        const url = "/src/platform/scrolling/scrollport-motion.ts";
        const { scrollportMotionForViewport } = await import(/* @vite-ignore */ url);
        return scrollportMotionForViewport(host)?.isDirectInputActive();
      })).toBe(false);
      before = await readerPosition(page);
    } finally {
      held.release();
    }
    if (!before) throw new Error("No history row at the reading position");
    await finishReply(page);
    const expected = before;
    await expect.poll(async () => {
      const after = await readerPosition(page);
      return after !== null && after.key === expected.key && Math.abs(after.offset - expected.offset) <= 2;
    }).toBe(true);
    await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
    await page.getByTestId("stream-scroll-jump").click();
    await expect.poll(async () => Math.abs(await tailDistance(page)) <= 2).toBe(true);
    await expect(stage.getByTestId("transcript-article-user").filter({ hasText: "Delayed send." })).toBeVisible();
  });

  webE2e("reopening preserves the first day label above its message", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages(
      Array.from({ length: 40 }, (_, i) => ({
        id: crypto.randomUUID(), role: i % 2 ? "assistant" as const : "user" as const,
        content: `Reading ${i}: ${"Keep this reading position. ".repeat(15)}`,
        created_at: new Date(Date.now() - 60_000 + i).toISOString(), ord: i + 1, seq: i + 1,
      })),
    ));
    const stage = liveChatStage(page);
    await expect(stage.locator("[data-msg-id]").first()).toBeVisible();
    const box = await stage.locator(".den-chat-stream").boundingBox();
    if (!box) throw new Error("Missing scrollport");
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.wheel(0, -100_000);
    await expect.poll(() => stage.locator(".den-chat-stream").evaluate((host) => host.scrollTop)).toBe(0);
    await expect(stage.getByTestId("transcript-day")).toBeVisible();
    await expect.poll(() => stage.locator(".den-chat-stream").evaluate(async (host) => {
      const url = "/src/platform/scrolling/scrollport-motion.ts";
      const { scrollportMotionForViewport } = await import(/* @vite-ignore */ url);
      return scrollportMotionForViewport(host)?.isDirectInputActive();
    })).toBe(false);
    const before = await readerPosition(page);
    await openNewChatSession(page);
    await openSessionRow(page, sessionId);
    await expect.poll(async () => {
      const after = await readerPosition(page);
      return after !== null && before !== null && after.key === before.key && Math.abs(after.offset - before.offset) <= 2
        ? "same row"
        : JSON.stringify({ before, after });
    }).toBe("same row");
  });
});
