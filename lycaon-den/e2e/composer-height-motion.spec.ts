import { expect, type Locator, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import {
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

type ComposerSample = {
  time: number;
  height: number;
  shellBottom: number;
  bodyBottom: number;
  padBottom: number;
  /** Combined stream and composer height stays fixed during resizing. */
  column: number;
  leaving: boolean;
};

type ComposerTraceWindow = Window & {
  __composerTrace?: { active: boolean; samples: ComposerSample[] };
};

webE2e.afterEach(async ({ page }, info) => {
  if (info.status === info.expectedStatus) return;
  const samples = await page.evaluate(() => (window as ComposerTraceWindow).__composerTrace?.samples);
  if (samples) {
    const path = info.outputPath("composer-geometry.json");
    writeFileSync(path, JSON.stringify(samples));
    await info.attach("composer-geometry", { path, contentType: "application/json" });
  }
});

async function startComposerTrace(stage: Locator) {
  await stage.evaluate((host) => {
    const shell = host.querySelector<HTMLElement>(".den-composer");
    const body = shell?.querySelector<HTMLElement>(":scope > .den-composer-body");
    const pad = shell?.querySelector<HTMLElement>(".den-composer-action-pad");
    const stream = host.querySelector<HTMLElement>(".den-chat-stream");
    if (!shell || !body || !pad || !stream) throw new Error("composer geometry is missing");
    const trace = { active: true, samples: [] as ComposerSample[] };
    (window as ComposerTraceWindow).__composerTrace = trace;
    const sample = (time: number) => {
      if (!trace.active) return;
      const frame = shell.getBoundingClientRect();
      trace.samples.push({
        time,
        height: frame.height,
        shellBottom: frame.bottom,
        bodyBottom: body.getBoundingClientRect().bottom,
        padBottom: pad.getBoundingClientRect().bottom,
        column: stream.getBoundingClientRect().height + frame.height,
        leaving: Boolean(shell.querySelector("[data-leaving]")),
      });
      requestAnimationFrame(sample);
    };
    requestAnimationFrame(sample);
  });
}

async function stopComposerTrace(page: Page): Promise<ComposerSample[]> {
  return page.evaluate(() => {
    const trace = (window as ComposerTraceWindow).__composerTrace;
    if (!trace) throw new Error("composer trace was not started");
    trace.active = false;
    return trace.samples;
  });
}

/** Measures elapsed motion between the first moving and settled frames. */
function motionSpan(samples: ComposerSample[]) {
  const start = samples[0]!.height;
  const end = samples[samples.length - 1]!.height;
  const moving = samples.findIndex((sample) => Math.abs(sample.height - start) > 0.5);
  const settled = samples.findIndex(
    (sample, index) => index >= moving && Math.abs(sample.height - end) <= 0.5,
  );
  const between = samples.filter(
    (sample) =>
      sample.height > Math.min(start, end) + 0.5 &&
      sample.height < Math.max(start, end) - 0.5,
  );
  return {
    start,
    end,
    intermediateFrames: between.length,
    ms: moving < 0 || settled < 0 ? 0 : samples[settled]!.time - samples[moving]!.time,
  };
}

function expectBottomEdgeHeld(samples: ComposerSample[]) {
  const padBottom = samples[0]!.padBottom;
  const column = samples[0]!.column;
  for (const sample of samples) {
    expect(Math.abs(sample.padBottom - padBottom)).toBeLessThanOrEqual(0.5);
    // The bottom border separates the shell and body edges.
    expect(Math.abs(sample.shellBottom - 1 - sample.bodyBottom)).toBeLessThanOrEqual(1);
    expect(Math.abs(sample.column - column)).toBeLessThanOrEqual(1);
  }
}

webE2e.describe("composer height motion", () => {
  webE2e("clearing a scrolled draft restores the empty composer height", async ({ page }) => {
    await bootstrapChatSession(page);
    const stage = liveChatStage(page);
    const composer = stage.getByTestId("chat-composer");
    const shell = stage.locator(".den-composer");
    const height = () => shell.evaluate((el) => el.getBoundingClientRect().height);
    await composer.click();
    const restingHeight = await height();

    for (let answer = 0; answer < 3; answer += 1) {
      await composer.fill("A line in the answer\n".repeat(20));
      await expect.poll(height).toBeGreaterThan(restingHeight + 50);
      await stage.locator(".den-composer-scroll > .den-scrollport__viewport").evaluate((el) => {
        el.scrollTop = el.scrollHeight;
      });
      await composer.hover();
      await page.mouse.wheel(0, 700);
      await composer.fill("");
      await expect.poll(height).toBeCloseTo(restingHeight, 0);
    }
  });

  webE2e("draft growth eases on a short curve and keeps pace with typing", async ({ page }) => {
    await bootstrapChatSession(page);
    const stage = liveChatStage(page);
    await expect(stage.getByTestId("session-workflow-launcher")).toBeVisible();
    const composer = stage.getByTestId("chat-composer");
    await composer.click();

    await startComposerTrace(stage);
    await page.keyboard.insertText("First line\nSecond line\nThird line\nFourth line");
    await page.waitForTimeout(400);
    const growth = await stopComposerTrace(page);

    const grew = motionSpan(growth);
    expect(grew.end).toBeGreaterThan(grew.start);
    expect(grew.ms).toBeLessThanOrEqual(100);
    expectBottomEdgeHeld(growth);

    // A keystroke that keeps the line count moves nothing.
    await startComposerTrace(stage);
    await page.keyboard.insertText(" more");
    await page.waitForTimeout(200);
    const steady = await stopComposerTrace(page);
    expect(new Set(steady.map((sample) => Math.round(sample.height))).size).toBe(1);

    await composer.fill("");
    await page.waitForTimeout(300);
    const typed = `${"Quick typing wraps across several lines of the composer. ".repeat(6)}done`;
    await startComposerTrace(stage);
    await composer.pressSequentially(typed, { delay: 8 });
    await page.waitForTimeout(300);
    const typing = await stopComposerTrace(page);
    await expect(composer).toHaveValue(typed);
    expect(typing[typing.length - 1]!.height).toBeGreaterThan(typing[0]!.height);
    expectBottomEdgeHeld(typing);
  });

  webE2e("the activity lane opens and closes on the disclosure timeline", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const stage = liveChatStage(page);
    const indicator = stage.getByTestId("thinking-indicator");

    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      await startComposerTrace(stage);
      const prompt = await apiPostPrompt(request, sessionId, { text: "Hold the turn open." });
      expect(prompt.ok(), await prompt.text()).toBe(true);
      await expect(indicator).toBeVisible({ timeout: 30_000 });
      await page.waitForTimeout(450);
      const opening = await stopComposerTrace(page);

      const opened = motionSpan(opening);
      expect(opened.end).toBeGreaterThan(opened.start);
      expect(opened.intermediateFrames).toBeGreaterThan(2);
      expect(opened.ms).toBeGreaterThanOrEqual(150);
      expect(opened.ms).toBeLessThanOrEqual(400);

      await startComposerTrace(stage);
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." });
      const pending = await apiJson<{ pending: boolean; id: string }>(
        request,
        "GET",
        "/harness/llm/pending",
      );
      if (pending.pending) {
        await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Done." });
      }
      await expect(indicator).toHaveCount(0, { timeout: 30_000 });
      await page.waitForTimeout(150);
      const closing = await stopComposerTrace(page);

      const closed = motionSpan(closing);
      expect(closed.end).toBeLessThan(closed.start);
      expect(closed.intermediateFrames).toBeGreaterThan(2);
      expect(closed.ms).toBeGreaterThanOrEqual(150);
      // Exit fading stays outside layout so the lane can collapse.
      expect(closing.some((sample) => sample.leaving)).toBe(true);
      expectBottomEdgeHeld(closing);
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." });
      const pending = await apiJson<{ pending: boolean; id: string }>(
        request,
        "GET",
        "/harness/llm/pending",
      );
      if (pending.pending) {
        await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Done." });
      }
    }
  });
});
