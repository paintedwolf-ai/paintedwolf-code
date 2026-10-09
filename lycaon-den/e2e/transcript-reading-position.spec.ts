import { expect, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e,
  openNewChatSession,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
} from "./helpers.ts";

const SCROLLER = ".den-chat-stream";

function conversation(label: string, count: number, firstOrd = 1) {
  return transcriptMessages(
    Array.from({ length: count }, (_, index) => ({
      id: crypto.randomUUID(),
      role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
      content: `${label} ${firstOrd + index}: ${"a reading position holds its row ".repeat(12)}`,
      created_at: new Date(Date.UTC(2026, 7, 23, 17, 0, firstOrd + index)).toISOString(),
      seq: firstOrd + index,
      ord: firstOrd + index,
    })),
  );
}

/** The row under the scrollport top and how far the top sits into it. */
async function readingRow(page: Page) {
  return liveChatStage(page).locator(SCROLLER).evaluate((scroller) => {
    const top = scroller.getBoundingClientRect().top;
    for (const row of scroller.querySelectorAll<HTMLElement>(
      ".transcript-viewport-row[data-msg-id]",
    )) {
      const rect = row.getBoundingClientRect();
      if (rect.bottom > top) {
        return { key: row.dataset.msgId ?? "", offset: Math.round(top - rect.top), label: row.textContent?.slice(0, 30) };
      }
    }
    return null;
  });
}

/** Distance of the transcript end above the scrollport's bottom clearance. */
async function tailGap(page: Page) {
  return liveChatStage(page).locator(SCROLLER).evaluate((scroller) => {
    const body = scroller.querySelector<HTMLElement>(".den-chat-stream-body");
    const end = scroller.querySelector<HTMLElement>("[data-transcript-end]");
    if (!body || !end) throw new Error("transcript tail is unavailable");
    const clearance = Number.parseFloat(getComputedStyle(body).paddingBottom) || 0;
    return Math.round(
      scroller.getBoundingClientRect().bottom -
        end.getBoundingClientRect().bottom -
        clearance,
    );
  });
}

async function wheelUpIntoHistory(page: Page) {
  const box = await liveChatStage(page).locator(SCROLLER).boundingBox();
  if (!box) throw new Error("chat scroller is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let step = 0; step < 10; step += 1) await page.mouse.wheel(0, -400);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  await expect.poll(() => liveChatStage(page).locator(SCROLLER).evaluate(async (host) => {
    const url = "/src/platform/scrolling/scrollport-motion.ts";
    const { scrollportMotionForViewport } = await import(/* @vite-ignore */ url);
    return scrollportMotionForViewport(host)?.input.isDirectInputActive();
  })).toBe(false);
}

for (const count of [40, 180]) {
modelIndependentWebE2e(`a reopened session with ${count} messages returns to the row the reader left`, async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, conversation("Reading", count));
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").first(),
  ).toBeVisible({ timeout: 30_000 });
  await wheelUpIntoHistory(page);
  const left = await readingRow(page);
  expect(left).not.toBeNull();

  const other = await openNewChatSession(page);
  expect(other).not.toBe(sessionId);
  await apiSeedSessionTranscript(request, other, conversation("Other conversation", 8));
  await expect(liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").first()).toContainText("Other conversation");
  await openSessionRow(page, sessionId);

  await expect.poll(async () => {
    const back = await readingRow(page);
    return back?.key === left!.key && Math.abs(back.offset - left!.offset) <= 2
      ? "same row"
      : JSON.stringify({ left, back });
  }).toBe("same row");
  await expect(liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").first()).toContainText("Reading");
  await expect(liveChatStage(page).getByText("Other conversation", { exact: false })).toHaveCount(0);
});
}

modelIndependentWebE2e("jump to latest keeps following as rows arrive", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, conversation("Earlier", 30));
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").first(),
  ).toBeVisible({ timeout: 30_000 });
  await wheelUpIntoHistory(page);

  await page.getByTestId("stream-scroll-jump").click();
  await expect.poll(() => tailGap(page).then((gap) => Math.abs(gap) <= 2)).toBe(true);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");

  for (let batch = 0; batch < 3; batch += 1) {
    await apiSeedSessionTranscript(request, sessionId, conversation("Later", 2, 31 + batch * 2));
    await expect(
      liveChatStage(page).locator(".transcript-viewport-row").getByText(`Later ${32 + batch * 2}:`, { exact: false }),
    ).toBeAttached();
    await expect.poll(() => tailGap(page).then((gap) => Math.abs(gap) <= 2)).toBe(true);
  }
});

modelIndependentWebE2e("rows arriving below a reader never move their row", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, conversation("Stable", 30));
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").first(),
  ).toBeVisible({ timeout: 30_000 });
  await wheelUpIntoHistory(page);
  const reading = await readingRow(page);

  await apiSeedSessionTranscript(request, sessionId, conversation("Below", 4, 31));
  await expect(liveChatStage(page).getByTestId("transcript-live-announcement")).toContainText("Below 34:");

  await expect.poll(async () => {
    const now = await readingRow(page);
    return now?.key === reading!.key && Math.abs(now.offset - reading!.offset) <= 1
      ? "still"
      : JSON.stringify({ reading, now });
  }).toBe("still");
});
