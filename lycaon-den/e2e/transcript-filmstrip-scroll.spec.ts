import { expect } from "@playwright/test";
import { buildTestFilmstripZip } from "../src/chat/visual/filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "../src/chat/visual/filmstrip-zip.ts";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e,
  settledChatSessionId,
  transcriptMessages,
} from "./helpers.ts";

modelIndependentWebE2e("filmstrip thumbnails pass vertical scrolling through to chat", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const artifactId = crypto.randomUUID();
  await page.route(`**/v1/sessions/${sessionId}/artifacts/${artifactId}`, (route) => route.fulfill({
    contentType: FILMSTRIP_MIME,
    body: Buffer.from(buildTestFilmstripZip(false, 12)),
  }));
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    ...Array.from({ length: 12 }, (_, index) => ({
      id: crypto.randomUUID(), role: "user" as const,
      content: `Earlier message ${index}: ${"Conversation history. ".repeat(40)}`,
      created_at: new Date(Date.UTC(2026, 8, 14, 10, 0, index)).toISOString(),
      seq: index + 1, ord: index + 1,
    })),
    {
      id: crypto.randomUUID(), role: "tool", content: "Captured steps",
      created_at: "2026-09-14T10:01:00Z", seq: 13, ord: 13,
      tool_result: {
        content: "Captured steps",
        visual: { id: artifactId, mime: FILMSTRIP_MIME, source: "capture", caption: "Captured steps" },
      },
    },
    {
      id: crypto.randomUUID(), role: "assistant", content: "Review the captured steps.",
      artifact_ids: [artifactId], created_at: "2026-09-14T10:02:00Z", seq: 14, ord: 14,
    },
    {
      id: crypto.randomUUID(), role: "user", content: "This note follows the captured steps. ".repeat(30),
      created_at: "2026-09-14T10:03:00Z", seq: 15, ord: 15,
    },
  ]));
  const chat = liveChatStage(page);
  const stream = chat.locator(".den-chat-stream");
  const rail = chat.getByTestId("transcript-visual-filmstrip-rail").last();
  await expect(rail).toBeVisible({ timeout: 30_000 });
  const viewport = rail.locator(":scope > .den-scrollport__viewport");
  await expect(viewport).toHaveAttribute("data-overlayscrollbars-viewport", /./);
  await expect.poll(() => viewport.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);
  const thumbnail = rail.locator(".den-transcript-visual-filmstrip__step-thumb").first();
  const hoverVisibleThumbnail = async () => {
    const box = await thumbnail.boundingBox();
    const viewportBox = await stream.boundingBox();
    if (!box || !viewportBox) throw new Error("Thumbnail geometry is unavailable");
    const top = Math.max(box.y, viewportBox.y);
    const bottom = Math.min(box.y + box.height, viewportBox.y + viewportBox.height);
    expect(bottom).toBeGreaterThan(top);
    // Locator hover can change the offset under test.
    await page.mouse.move(box.x + box.width / 2, (top + bottom) / 2);
  };
  // Seeded row estimates and decoded images settle across multiple frames.
  await expect.poll(() => thumbnail.evaluate(async (el) => {
    const stream = el.closest<HTMLElement>(".den-chat-stream")!;
    const moduleUrl = "/src/chat/stream/stream-scroll.ts";
    const { streamTailOffset } = await import(/* @vite-ignore */ moduleUrl);
    const sample = () => [el.getBoundingClientRect().top, stream.scrollTop, stream.scrollHeight, stream.clientHeight];
    const initial = sample();
    for (let frame = 0; frame < 10; frame++) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      if (sample().some((value, index) => Math.abs(value - initial[index]!) > 1)) return false;
    }
    return Math.abs(streamTailOffset(stream) - stream.scrollTop) <= 1;
  })).toBe(true);
  await hoverVisibleThumbnail();
  // Row measurements refine document offsets while preserving visible content.
  const thumbnailTop = () => thumbnail.evaluate((el) => el.getBoundingClientRect().top);
  const before = await thumbnailTop();
  await page.mouse.wheel(0, -200);
  await expect.poll(thumbnailTop).toBeGreaterThan(before + 180);
  await expect(chat.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");

  await hoverVisibleThumbnail();
  const raised = await thumbnailTop();
  // Remain outside the tail-follow zone while checking downward native input.
  await page.mouse.wheel(0, 40);
  await expect.poll(thumbnailTop).toBeLessThan(raised - 20);

  await hoverVisibleThumbnail();
  const vertical = await thumbnailTop();
  await page.mouse.wheel(180, 0);
  await expect.poll(() => viewport.evaluate((el) => el.scrollLeft)).toBeGreaterThan(50);
  await expect.poll(async () => Math.abs(await thumbnailTop() - vertical)).toBeLessThan(2);
  const step = rail.getByRole("option", { name: "Step 4", exact: true });
  await step.click();
  await expect(step).toHaveAttribute("aria-selected", "true");
  await expect(chat.getByTestId("transcript-visual-filmstrip-reading").last()).toContainText("Step 4");
});
