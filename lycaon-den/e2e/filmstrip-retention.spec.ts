import { expect } from "@playwright/test";
import { buildTestFilmstripZip } from "../src/chat/visual/filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "../src/chat/visual/filmstrip-zip.ts";
import {
  apiSeedSessionTranscript, bootstrapChatSession, liveChatStage,
  modelIndependentWebE2e, settledChatSessionId, transcriptMessages,
} from "./helpers.ts";

modelIndependentWebE2e("filmstrip virtualization reuses its fetch and frame URLs", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const artifactId = crypto.randomUUID();
  let fetches = 0;
  await page.route(`**/v1/sessions/${sessionId}/artifacts/${artifactId}`, (route) => {
    fetches++;
    return route.fulfill({ contentType: FILMSTRIP_MIME, body: Buffer.from(buildTestFilmstripZip(false, 12)) });
  });
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    ...Array.from({ length: 80 }, (_, index) => ({
      id: crypto.randomUUID(), role: "user" as const,
      content: `Earlier message ${index}: ${"Conversation history. ".repeat(40)}`,
      created_at: new Date(Date.UTC(2026, 8, 14, 10, 0, index)).toISOString(),
      seq: index + 1, ord: index + 1,
    })),
    {
      id: crypto.randomUUID(), role: "tool", content: "Captured steps",
      created_at: "2026-09-14T10:02:00Z", seq: 81, ord: 81,
      tool_result: {
        content: "Captured steps",
        visual: { id: artifactId, mime: FILMSTRIP_MIME, source: "capture", caption: "Captured steps" },
      },
    },
    {
      id: crypto.randomUUID(), role: "assistant", content: "Review the captured steps.",
      artifact_ids: [artifactId], created_at: "2026-09-14T10:03:00Z", seq: 82, ord: 82,
    },
  ]));
  const chat = liveChatStage(page);
  const stream = chat.locator(".den-chat-stream");
  const strip = chat.getByTestId("transcript-visual-filmstrip");
  const rail = chat.getByTestId("transcript-visual-filmstrip-rail").last();
  await expect(rail).toBeVisible({ timeout: 30_000 });
  const firstSrc = await rail.locator("img").first().getAttribute("src");
  expect(firstSrc).toMatch(/^blob:/);
  const waitForTail = () => expect.poll(() => stream.evaluate(async (el) => {
    const moduleUrl = "/src/chat/stream/stream-scroll.ts";
    const { streamTailOffset } = await import(/* @vite-ignore */ moduleUrl);
    return Math.abs(streamTailOffset(el) - el.scrollTop);
  })).toBeLessThanOrEqual(1);
  for (let cycle = 0; cycle < 3; cycle++) {
    await waitForTail();
    const box = await stream.boundingBox();
    if (!box) throw new Error("Chat scroll geometry is unavailable");
    // Target the stream's padding, clear of nested media scrollports.
    await page.mouse.move(box.x + 4, box.y + box.height / 2);
    for (let step = 0; step < 4; step++) await page.mouse.wheel(0, -1200);
    await expect.poll(async () => {
      if (await strip.count() === 0) return "unmounted";
      return stream.evaluate((el) => JSON.stringify({
        offset: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight,
        firstRow: el.querySelector(".transcript-viewport-row")?.textContent?.slice(0, 50),
      }));
    }).toBe("unmounted");
    await chat.getByTestId("stream-scroll-jump").click();
    await expect(rail).toBeVisible();
    await waitForTail();
    await expect(rail.locator("img").first()).toHaveAttribute("src", firstSrc!);
  }
  expect(fetches).toBe(1);
});
