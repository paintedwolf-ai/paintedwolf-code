import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

const PROBE_PX = 240;

/**
 * Past any fill, growing a zero-width spacer at the end of a scrollport's extent grows its
 * range at least as much; shrinkable or row-flex extents absorb it instead.
 */
async function scrollportsAbsorbingRetainedRange(page: Page): Promise<string[]> {
  return page.evaluate((probePx) => {
    const failures: string[] = [];
    for (const frame of document.querySelectorAll<HTMLElement>('[data-den-scrollport="y"], [data-den-scrollport="both"]')) {
      const viewport = frame.querySelector<HTMLElement>(":scope > .den-scrollport__viewport");
      const extent = viewport?.querySelector<HTMLElement>(":scope > .den-scrollport__content");
      if (!viewport || !extent || viewport.clientHeight === 0) continue;
      const probe = document.createElement("div");
      probe.style.cssText = `height:${viewport.clientHeight}px;width:0;flex-shrink:0;pointer-events:none`;
      extent.appendChild(probe);
      const pastFill = viewport.scrollHeight;
      probe.style.height = `${viewport.clientHeight + probePx}px`;
      const grown = viewport.scrollHeight - pastFill;
      probe.remove();
      if (grown < probePx - 1) failures.push(`${frame.className.trim()}: +${grown}px of ${probePx}px`);
    }
    return failures;
  }, PROBE_PX);
}

async function seedActivityAtTail(page: Page, request: APIRequestContext): Promise<void> {
  const sessionId = await settledChatSessionId(page);
  const assistantId = crypto.randomUUID();
  const paths = ["README.md", "main.go", "go.mod"];
  const earlier = "The earlier answer walks through the layout, the spacing scale, and the icon set in enough detail to fill the window. ".repeat(6);
  // Earlier exchanges make the transcript overflow, so the card sits at the tail of a scrolled view.
  const history = Array.from({ length: 4 }, (_, index) => [
    { id: crypto.randomUUID(), role: "user" as const, content: `Question ${index + 1}`, created_at: "2026-09-22T11:00:00Z", seq: 2 * index + 1, ord: 2 * index + 1 },
    { id: crypto.randomUUID(), role: "assistant" as const, content: earlier, created_at: "2026-09-22T11:00:01Z", seq: 2 * index + 2, ord: 2 * index + 2 },
  ]).flat();
  const base = history.length;
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      ...history,
      { id: crypto.randomUUID(), role: "user", content: "Can we make the interface look more professional?", created_at: "2026-09-22T12:00:00Z", seq: base + 1, ord: base + 1 },
      {
        id: assistantId,
        role: "assistant",
        content: "",
        tool_calls: paths.map((path, index) => ({ id: `tc-read-${index}`, name: "read", args: { path } })),
        created_at: "2026-09-22T12:00:01Z",
        seq: base + 2,
        ord: base + 2,
      },
      ...paths.map((path, index) => ({
        id: crypto.randomUUID(),
        role: "tool" as const,
        content: `contents of ${path}`,
        tool_result: { content: `contents of ${path}`, tool: "read", tool_call_id: `tc-read-${index}`, assistant_message_id: assistantId },
        created_at: "2026-09-22T12:00:02Z",
        seq: base + 3 + index,
        ord: base + 3 + index,
      })),
      { id: crypto.randomUUID(), role: "assistant", content: "Done.", created_at: "2026-09-22T12:00:03Z", seq: base + 6, ord: base + 6 },
    ]),
  );
}

const stream = (page: Page) => liveChatStage(page).locator(".den-scrollport__viewport.den-chat-stream");

webE2e("every vertical scrollport extends its range with retained extent", async ({ page, request }) => {
  await page.setViewportSize({ width: 1100, height: 520 });
  await bootstrapChatSession(page, request);
  await seedActivityAtTail(page, request);
  await expect(liveChatStage(page).getByTestId("activity-span-card").first()).toBeVisible({ timeout: 30_000 });

  expect(await liveChatStage(page).locator('[data-den-scrollport="y"] > .den-scrollport__viewport.den-chat-stream').count()).toBe(1);
  expect(await scrollportsAbsorbingRetainedRange(page)).toEqual([]);
});

/** A transcript scrolled to its end with the last activity card expanded, and the pointer on its header. */
async function expandedCardAtBottom(page: Page, request: APIRequestContext) {
  await page.setViewportSize({ width: 1100, height: 520 });
  await bootstrapChatSession(page, request);
  await seedActivityAtTail(page, request);
  const card = liveChatStage(page).getByTestId("activity-span-card").last();
  const summary = card.locator(":scope > summary");
  await expect(summary).toBeVisible({ timeout: 30_000 });
  await summary.click();
  await expect(card).not.toHaveAttribute("data-animating", "true");
  await page.mouse.move(600, 200);
  for (let i = 0; i < 8; i += 1) await page.mouse.wheel(0, 200);
  await expect.poll(() => stream(page).evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(2);
  await page.waitForTimeout(700);
  const box = (await summary.boundingBox())!;
  const x = box.x + 40;
  const y = box.y + box.height - 8;
  await page.mouse.move(x, y);
  return { card, summary, x, y, top: box.y };
}

/**
 * Records the pressed header's top as each frame is painted: a task queued from a frame callback
 * runs after that frame renders, so a clamp repaired later in the frame is not hidden.
 */
async function trackPaintedTop(page: Page, summary: Locator): Promise<() => Promise<number[]>> {
  await summary.evaluate((el) => {
    const w = window as unknown as { __paintedTops: number[]; __stopPainted: boolean };
    w.__paintedTops = [];
    w.__stopPainted = false;
    const tick = () => setTimeout(() => {
      w.__paintedTops.push(el.getBoundingClientRect().top);
      if (!w.__stopPainted) requestAnimationFrame(tick);
    }, 0);
    requestAnimationFrame(tick);
  });
  return () => page.evaluate(() => {
    const w = window as unknown as { __paintedTops: number[]; __stopPainted: boolean };
    w.__stopPainted = true;
    return w.__paintedTops;
  });
}

webE2e("a card collapsed at the tail holds through a click sequence, then glides back", async ({ page, request }) => {
  const { card, summary, top } = await expandedCardAtBottom(page, request);
  const painted = await trackPaintedTop(page, summary);
  // Quick presses that follow the first land on the same control.
  await page.mouse.down({ clickCount: 1 });
  await page.mouse.up({ clickCount: 1 });
  await page.waitForTimeout(120);
  await page.mouse.down({ clickCount: 2 });
  await page.mouse.up({ clickCount: 2 });
  await page.mouse.down({ clickCount: 3 });
  await page.mouse.up({ clickCount: 3 });
  await expect(card).not.toHaveAttribute("data-animating", "true");

  // Scroll offsets land on device pixels, so the header holds within one on every painted frame:
  // a frame that shows it elsewhere is where the next click lands.
  expect(Math.abs((await summary.boundingBox())!.y - top)).toBeLessThanOrEqual(1);
  const tops = await painted();
  const moved = tops.filter((frameTop) => Math.abs(frameTop - tops[0]!) > 1);
  expect(moved, `painted header tops ${tops.map(Math.round).join(", ")}`).toEqual([]);

  // With the pointer still resting, the space below the last card closes on its own.
  const held = await stream(page).evaluate((el) => el.scrollTop);
  await expect.poll(() => stream(page).evaluate((el) => el.scrollTop), { intervals: [16] }).toBeLessThan(held - 20);
  await expect.poll(() => stream(page).evaluate((el) => el.scrollTop > el.scrollHeight - el.clientHeight - 2)).toBe(true);
});

webE2e("wheeling up after a collapse at the tail never pulls back", async ({ page, request }) => {
  const { x, y } = await expandedCardAtBottom(page, request);
  await page.mouse.click(x, y);
  const offsets = [await stream(page).evaluate((el) => el.scrollTop)];
  for (let notch = 0; notch < 8; notch += 1) {
    await page.mouse.wheel(0, -60);
    await page.waitForTimeout(60);
    offsets.push(await stream(page).evaluate((el) => el.scrollTop));
  }
  await page.waitForTimeout(900);
  offsets.push(await stream(page).evaluate((el) => el.scrollTop));
  const pulledBack = offsets.slice(1).filter((offset, index) => offset > offsets[index]! + 1);
  expect(pulledBack, `offsets ${offsets.map(Math.round).join(", ")}`).toEqual([]);
  expect(offsets.at(-1)!).toBeLessThan(offsets[0]! - 100);
});

/** Blank space between the newest content and the bottom of the chat viewport. */
function spaceBelowNewest(page: Page): Promise<number> {
  return stream(page).evaluate((el) => {
    const end = el.querySelector("[data-transcript-end]");
    if (!end) return 0;
    return Math.max(0, el.getBoundingClientRect().bottom - end.getBoundingClientRect().bottom);
  });
}

webE2e("the newest card is the end of the chat: a wheel never scrolls further past it", async ({ page, request }) => {
  const { card, x, y } = await expandedCardAtBottom(page, request);
  // At the tail, the chat's own end inset is all that lies below the newest card.
  const inset = await spaceBelowNewest(page);
  await page.mouse.click(x, y);
  await expect(card).not.toHaveAttribute("data-animating", "true");
  const held = (await spaceBelowNewest(page)) - inset;
  expect(held).toBeGreaterThan(40);
  // Reading back up inside the space the collapse left, then turning around: input may shrink
  // that space, never grow it.
  const up = -Math.round(held / 4);
  const seen = [await spaceBelowNewest(page)];
  for (const deltaY of [up, up, 120, 120, 120, 120, 120, 120]) {
    await page.mouse.wheel(0, deltaY);
    await page.waitForTimeout(40);
    seen.push(await spaceBelowNewest(page));
  }
  const grew = seen.slice(1).filter((gap, index) => gap > seen[index]! + 1);
  expect(grew, `space below the newest card ${seen.map(Math.round).join(", ")}`).toEqual([]);
  // Once input settles, the space closes by itself.
  await expect.poll(() => spaceBelowNewest(page), { timeout: 5_000 }).toBeLessThanOrEqual(inset + 1);
});
