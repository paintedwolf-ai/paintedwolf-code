import { mkdirSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import { expect } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e as test, settledChatSessionId, transcriptMessages } from "./helpers.ts";

test("transcript geometry keeps measurement work bounded during scrolling and resizing", async ({ page, request }, info) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(Array.from({ length: 180 }, (_, index) => ({
    id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
    content: `Message ${index}. ${"Measured reading content. ".repeat(30)}`,
    created_at: "2026-09-15T12:00:00Z", seq: index + 1, ord: index + 1,
  }))));
  const stream = liveChatStage(page).locator(".den-chat-stream");
  await expect(stream.locator(".transcript-viewport-row").getByText("Message 179.", { exact: false })).toBeAttached();
  await stream.focus();
  await page.keyboard.press("End");
  await page.evaluate(async () => { for (let n = 0; n < 30; n++) await new Promise(requestAnimationFrame); });
  await stream.press("PageUp");
  await page.waitForTimeout(400);
  await stream.evaluate((host) => { host.scrollTop = host.scrollHeight - host.clientHeight * 2; });
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  const sample = await stream.evaluate(async (host) => {
    const rect = Element.prototype.getBoundingClientRect;
    const computedStyle = window.getComputedStyle;
    let styleReads = 0;
    window.getComputedStyle = (...args) => {
      if (args[0] === document.documentElement || args[0] === host || host.contains(args[0])) styleReads++;
      return computedStyle(...args);
    };
    let reads = 0;
    let maxRows = 0;
    const frameTimes: number[] = [];
    Element.prototype.getBoundingClientRect = function () {
      if (this === host || host.contains(this)) reads++;
      return rect.call(this);
    };
    let before = performance.now();
    const frame = async () => {
      await new Promise(requestAnimationFrame);
      const now = performance.now();
      frameTimes.push(now - before);
      before = now;
      maxRows = Math.max(maxRows, host.querySelectorAll(".transcript-viewport-row[data-index]").length);
    };
    try {
      for (let index = 0; index < 60; index++) {
        host.scrollTop = Math.max(0, host.scrollTop - 60);
        await frame();
      }
      const scrollReads = reads;
      const scrollStyleReads = styleReads;
      const body = host.querySelector<HTMLElement>(".den-chat-stream-inner")!;
      for (let index = 0; index < 30; index++) {
        body.style.width = `${100 - Math.sin(index / 29 * Math.PI) * 25}%`;
        await frame();
      }
      body.style.removeProperty("width");
      for (let index = 0; index < 10; index++) await frame();
      const resizeReads = reads - scrollReads;
      const resizeStyleReads = styleReads - scrollStyleReads;
      const idleStyleStart = styleReads;
      const idleStart = reads;
      for (let index = 0; index < 30; index++) await frame();
      return { scrollReads, resizeReads, idleReads: reads - idleStart, scrollStyleReads, resizeStyleReads, idleStyleReads: styleReads - idleStyleStart, maxRows, frameTimes };
    } finally {
      Element.prototype.getBoundingClientRect = rect;
      window.getComputedStyle = computedStyle;
    }
  });
  const report = { browser: info.project.use.browserName, ...sample };
  const output = process.env.TRANSCRIPT_PERF_REPORT ?? info.outputPath("transcript-performance.json");
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(output, JSON.stringify(report, null, 2));
  expect(sample.maxRows).toBeLessThan(30);
  expect(sample.scrollReads).toBeLessThan(800);
  expect(sample.resizeReads).toBeLessThan(1000);
  expect(sample.scrollStyleReads).toBeLessThan(450);
  expect(sample.resizeStyleReads).toBeLessThan(450);
  expect(sample.idleReads).toBe(0);
  expect(sample.idleStyleReads).toBe(0);
});
