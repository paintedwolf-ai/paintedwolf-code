import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

webE2e("walk keeps the selected chat item visible after virtual rows settle", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const messages = Array.from({ length: 40 }, (_, index) => {
    const assistantId = crypto.randomUUID();
    return [
      { id: crypto.randomUUID(), role: "user" as const, content: `Inspect file ${index}`, ord: index * 4 + 1 },
      {
        id: assistantId, role: "assistant" as const, content: "",
        tool_calls: [
          { id: `call-${index}`, name: "read", args: { path: `file-${index}.ts` } },
          { id: `call-${index}-next`, name: "read", args: { path: `next-${index}.ts` } },
        ],
        ord: index * 4 + 2,
      },
      {
        id: crypto.randomUUID(), role: "tool" as const, content: `Contents of file ${index}`,
        tool_result: {
          tool_call_id: `call-${index}`, assistant_message_id: assistantId,
          tool: "read", content: `Contents of file ${index}`,
        },
        ord: index * 4 + 3,
      },
      {
        id: crypto.randomUUID(), role: "tool" as const, content: `Next file ${index}`,
        tool_result: {
          tool_call_id: `call-${index}-next`, assistant_message_id: assistantId,
          tool: "read", content: `Next file ${index}`,
        },
        ord: index * 4 + 4,
      },
    ].map((message) => ({ ...message, seq: message.ord, created_at: "2026-09-03T00:00:00Z" }));
  }).flat();
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(messages));
  await expect(page.locator(".den-activity-span").first()).toBeAttached();
  await page.evaluate(async (id) => {
    const walkUrl = "/src/files/walk/walk-store.ts";
    const fixturesUrl = "/src/files/walk/walk-fixtures.ts";
    const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixtures = await import(/* @vite-ignore */ fixturesUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    const effects = Array.from({ length: 40 }, (_, index) => [
      { ...fixtures.walkEffectFixture(String(index), 1, index * 2 + 1), session_id: id },
      { ...fixtures.walkEffectFixture(`${index}-next`, 1, index * 2 + 2), session_id: id },
    ]).flat();
    await walk.enterWalk("walk-scroll-fixture", fixtures.walkClientFixture(() => fixtures.walkResponseFixture(effects)), id);
  }, sessionId);

  for (const index of [0, 25, 7, 38]) {
    await page.evaluate(async (at) => {
      const url = "/src/files/walk/walk-store.ts";
      const walk = await import(/* @vite-ignore */ url) as typeof import("../src/files/walk/walk-store.ts");
      walk.setWalkAt("walk-scroll-fixture", at);
    }, index * 2);
    const activity = page.locator(`.den-activity-span:has([data-tool-call-id="call-${index}"])`);
    const marked = activity.and(page.locator(".den-walk-step-mark"));
    await expect(marked).toHaveCount(1).catch(async (error: unknown) => {
      const diagnostic = await page.evaluate(async () => {
        const url = "/src/files/walk/walk-store.ts";
        const walk = await import(/* @vite-ignore */ url) as typeof import("../src/files/walk/walk-store.ts");
        const state = walk.walkState("walk-scroll-fixture");
        return {
          at: state.at, targetAt: state.targetAt, step: walk.currentWalkStep("walk-scroll-fixture"),
          streams: [...document.querySelectorAll<HTMLElement>(".den-chat-stream")].map((stream) => ({
            session: stream.dataset.sessionId, resident: stream.closest("[data-resident]")?.getAttribute("data-resident"),
            top: stream.scrollTop, height: stream.scrollHeight, viewport: stream.clientHeight,
            rows: [...stream.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map((row) => ({ index: row.dataset.index, calls: row.dataset.toolCallIds })),
            marks: [...stream.querySelectorAll<HTMLElement>(".den-walk-step-mark")].map((mark) => ({ tag: mark.tagName, cls: mark.className, call: mark.dataset.toolCallId })),
          })),
        };
      });
      throw new Error(`${String(error)}\nWalk state: ${JSON.stringify(diagnostic)}`);
    });
    await expect(marked).toBeInViewport({ ratio: 1 });
    const positions = await marked.evaluate(async (element) => {
      const positions: number[] = [];
      for (let frame = 0; frame < 30; frame += 1) {
        await new Promise(requestAnimationFrame);
        positions.push(element.getBoundingClientRect().top);
      }
      return positions.slice(-10);
    });
    expect(Math.max(...positions) - Math.min(...positions)).toBeLessThan(2);
    await expect(marked).toBeInViewport({ ratio: 1 });
    await activity.locator(":scope > summary").click();
    const command = activity.locator(`[data-tool-call-id="call-${index}"].den-walk-step-mark`);
    await expect(command).toHaveCount(1);
    await expect(command).toBeInViewport({ ratio: 1 });
    await expect(activity).not.toHaveClass(/den-walk-step-mark/);
    await page.evaluate(async (at) => {
      const url = "/src/files/walk/walk-store.ts";
      const walk = await import(/* @vite-ignore */ url) as typeof import("../src/files/walk/walk-store.ts");
      walk.setWalkAt("walk-scroll-fixture", at);
    }, index * 2 + 1);
    const next = activity.locator(`[data-tool-call-id="call-${index}-next"].den-walk-step-mark`);
    await expect(next).toHaveCount(1);
    await expect(next).toBeInViewport({ ratio: 1 });
    await expect(activity).toHaveAttribute("open", "");
    await activity.locator(":scope > summary").click();
    await expect(marked).toHaveCount(1);
    await expect(marked).toBeInViewport({ ratio: 1 });
  }
});
