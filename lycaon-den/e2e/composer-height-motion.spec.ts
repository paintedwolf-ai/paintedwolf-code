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
};

type ComposerHeightMotion = { duration: number; easing: string };

type ComposerTrace = {
  samples: ComposerSample[];
  /** Declared by each height animation, so dropped frames cannot change them. */
  motions: ComposerHeightMotion[];
  sawLeaving: boolean;
};

type ComposerTraceWindow = Window & {
  __composerTrace?: ComposerTrace & { active: boolean; animations: Animation[]; release: () => void };
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
    const leaving = new MutationObserver(() => {
      trace.sawLeaving = true;
    });
    leaving.observe(shell, { subtree: true, attributes: true, attributeFilter: ["data-leaving"] });
    const animate = shell.animate;
    // The shell animates only its height.
    shell.animate = (...args: Parameters<typeof animate>) => {
      const animation = animate.apply(shell, args);
      const timing = animation.effect!.getTiming();
      trace.motions.push({ duration: Number(timing.duration), easing: timing.easing! });
      trace.animations.push(animation);
      return animation;
    };
    const trace = {
      active: true,
      samples: [] as ComposerSample[],
      motions: [] as ComposerHeightMotion[],
      animations: [] as Animation[],
      sawLeaving: false,
      release: () => {
        delete (shell as Partial<HTMLElement>).animate;
        leaving.disconnect();
      },
    };
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
      });
      requestAnimationFrame(sample);
    };
    requestAnimationFrame(sample);
  });
}

/** Waits until the traced shell has started a height animation and landed it. */
async function waitForComposerMotion(page: Page) {
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const animations = (window as ComposerTraceWindow).__composerTrace?.animations ?? [];
          return animations.length > 0 && animations.every((a) => a.playState !== "running");
        }),
      { timeout: 30_000 },
    )
    .toBe(true);
}

async function stopComposerTrace(page: Page): Promise<ComposerTrace> {
  return page.evaluate(async () => {
    const trace = (window as ComposerTraceWindow).__composerTrace;
    if (!trace) throw new Error("composer trace was not started");
    // Sample the landed height before the trace ends.
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    trace.active = false;
    trace.release();
    return { samples: trace.samples, motions: trace.motions, sawLeaving: trace.sawLeaving };
  });
}

function heightChange(samples: ComposerSample[]) {
  return { start: samples[0]!.height, end: samples[samples.length - 1]!.height };
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
    const shell = stage.locator(".den-composer");
    const height = () => shell.evaluate((el) => el.getBoundingClientRect().height);
    const restingHeight = await height();

    await startComposerTrace(stage);
    await page.keyboard.insertText("First line\nSecond line\nThird line\nFourth line");
    await waitForComposerMotion(page);
    const growth = await stopComposerTrace(page);

    const grew = heightChange(growth.samples);
    expect(grew.end).toBeGreaterThan(grew.start);
    expect(growth.motions.length).toBeGreaterThan(0);
    for (const motion of growth.motions) expect(motion.duration).toBeLessThanOrEqual(100);
    expectBottomEdgeHeld(growth.samples);

    // A keystroke that keeps the line count moves nothing.
    await startComposerTrace(stage);
    await page.keyboard.insertText(" more");
    await page.waitForTimeout(200);
    const steady = await stopComposerTrace(page);
    expect(steady.motions).toEqual([]);
    expect(new Set(steady.samples.map((sample) => Math.round(sample.height))).size).toBe(1);

    await composer.fill("");
    await expect.poll(height, { timeout: 30_000 }).toBeCloseTo(restingHeight, 0);
    const typed = `${"Quick typing wraps across several lines of the composer. ".repeat(6)}done`;
    await startComposerTrace(stage);
    await composer.pressSequentially(typed, { delay: 8 });
    await waitForComposerMotion(page);
    const typing = await stopComposerTrace(page);
    await expect(composer).toHaveValue(typed);
    const wrapped = heightChange(typing.samples);
    expect(wrapped.end).toBeGreaterThan(wrapped.start);
    expectBottomEdgeHeld(typing.samples);
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
      await waitForComposerMotion(page);
      const opening = await stopComposerTrace(page);

      const opened = heightChange(opening.samples);
      expect(opened.end).toBeGreaterThan(opened.start);
      expect(opening.motions.length).toBeGreaterThan(0);
      for (const motion of opening.motions) {
        expect(motion.duration).toBeGreaterThanOrEqual(140);
        expect(motion.duration).toBeLessThanOrEqual(400);
      }

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
      await waitForComposerMotion(page);
      const closing = await stopComposerTrace(page);

      const closed = heightChange(closing.samples);
      expect(closed.end).toBeLessThan(closed.start);
      expect(closing.motions.length).toBeGreaterThan(0);
      for (const motion of closing.motions) expect(motion.duration).toBeGreaterThanOrEqual(140);
      // Exit fading stays outside layout so the lane can collapse.
      expect(closing.sawLeaving).toBe(true);
      expectBottomEdgeHeld(closing.samples);
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
