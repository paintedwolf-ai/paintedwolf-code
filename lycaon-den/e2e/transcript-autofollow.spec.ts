import { expect, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e,
  settledChatSessionId,
  transcriptMessages,
} from "./helpers.ts";

async function tailDistance(page: Page) {
  return liveChatStage(page).locator(".den-chat-stream").evaluate((stream) => {
    const end = stream.querySelector<HTMLElement>("[data-transcript-end]")!;
    const body = stream.querySelector<HTMLElement>(".den-chat-stream-body")!;
    return end.getBoundingClientRect().bottom +
      Number.parseFloat(getComputedStyle(body).paddingBottom) -
      stream.getBoundingClientRect().bottom;
  });
}

async function atTail(page: Page) {
  await expect.poll(async () => Math.abs(await tailDistance(page))).toBeLessThan(2);
}

async function seedConversation(page: Page, request: Parameters<typeof apiSeedSessionTranscript>[0], toolCount = 1, historyCount = 8) {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const assistantId = crypto.randomUUID();
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    ...Array.from({ length: historyCount }, (_, index) => ({
      id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
      content: `Conversation ${index + 1}. ${"Content that wraps over several lines. ".repeat(12)}`,
      created_at: "2026-09-13T12:00:00Z", seq: index + 1, ord: index + 1,
    })),
    { id: assistantId, role: "assistant", content: "", created_at: "2026-09-13T12:01:00Z", seq: 21, ord: 21,
      tool_calls: Array.from({ length: toolCount }, (_, index) => ({ id: `follow-read-${index}`, name: "read", args: { path: `result-${index}.txt` } })) },
    ...Array.from({ length: toolCount }, (_, index) => ({
      id: crypto.randomUUID(), role: "tool" as const, content: "Result", created_at: "2026-09-13T12:01:01Z", seq: 22 + index, ord: 22 + index,
      tool_result: { tool: "read", tool_call_id: `follow-read-${index}`, assistant_message_id: assistantId,
        content: Array.from({ length: 80 }, (_, line) => `Output line ${line + 1}`).join("\n") },
    })),
  ]));
  const activity = liveChatStage(page).getByTestId("activity-span-card").last();
  const stream = liveChatStage(page).locator(".den-chat-stream");
  await expect(activity).toBeAttached();
  await expect(activity).toHaveAttribute("data-count", String(toolCount));
  await expect(activity).toHaveAttribute("data-status", "done");
  // The launcher exit changes the viewport height.
  await expect(liveChatStage(page).getByTestId("composer-chrome-slot-launcher")).toHaveCount(0);
  if (historyCount === 0) {
    await expect.poll(() => stream.evaluate(element => element.scrollHeight - element.clientHeight)).toBe(0);
    return { sessionId, activity, stream, nextSeq: 22 + toolCount };
  }
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  await expect(activity).toBeVisible();
  return { sessionId, activity, stream, nextSeq: 22 + toolCount };
}

modelIndependentWebE2e("End at the collapsed activity tail resumes autofollow without movement", async ({ page, request }) => {
  const { sessionId, activity, stream } = await seedConversation(page, request);
  const summary = activity.locator(":scope > summary");
  await summary.click();
  await expect(activity).not.toHaveAttribute("data-animating", "true");
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  await summary.click();
  await expect(activity).not.toHaveAttribute("open", "");
  await atTail(page);
  await stream.focus();
  await page.keyboard.press("End");
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "assistant", content: "Following resumed. " + "New answer content. ".repeat(45),
      created_at: "2026-09-13T12:02:00Z", seq: 23, ord: 23 },
  ]));
  await expect(page.getByText("Following resumed.", { exact: false }).first()).toBeAttached();
  await atTail(page);
});

modelIndependentWebE2e("a layout clamp at the followed tail returns to the tail", async ({ page, request }) => {
  const { stream } = await seedConversation(page, request);
  // A size container mounting above the reader clamps the offset in WebKit's interleaved
  // layout pass after the range is restored; no later layout change follows to repin it.
  await stream.evaluate((host) => { host.scrollTop -= 150; });
  await atTail(page);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
});

modelIndependentWebE2e("expanded action lists scroll with the transcript and resume autofollow", async ({ page, request }) => {
  // Activity cards virtualize their actions past 100 entries.
  const { sessionId, activity, stream, nextSeq } = await seedConversation(page, request, 120);
  await activity.locator(":scope > summary").click();
  await expect(activity).not.toHaveAttribute("data-animating", "true");
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  const output = activity.getByTestId("virtual-card-list");
  expect(await output.evaluate(element => ({
    overflow: getComputedStyle(element).overflowY,
    scrolls: element.scrollHeight > element.clientHeight + 1,
  }))).toEqual({ overflow: "visible", scrolls: false });
  const before = await stream.evaluate(element => element.scrollTop);
  await activity.locator(".den-activity-span-item").last().hover();
  await page.mouse.wheel(0, -200);
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(before - 50);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "assistant", content: "Answer after reading output. " + "New answer content. ".repeat(45),
      created_at: "2026-09-13T12:02:00Z", seq: nextSeq, ord: nextSeq },
  ]));
  await expect(page.getByText("Answer after reading output.", { exact: false }).first()).toBeAttached();
  await atTail(page);
});

modelIndependentWebE2e("End resumes plain-chat autofollow after text selection", async ({ page, request }) => {
  const { sessionId, stream } = await seedConversation(page, request);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "assistant", content: "Select this answer. " + "Ordinary conversation. ".repeat(30),
      created_at: "2026-09-13T12:02:00Z", seq: 23, ord: 23 },
  ]));
  const prose = liveChatStage(page).locator(".assistant-prose p").last();
  await expect(prose).toContainText("Select this answer.");
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  await prose.dblclick();
  await expect.poll(() => page.evaluate(() => window.getSelection()?.toString().length ?? 0)).toBeGreaterThan(0);
  await stream.click({ position: { x: 5, y: 5 } });
  await expect.poll(() => page.evaluate(() => window.getSelection()?.isCollapsed)).toBe(true);
  await stream.focus();
  await page.keyboard.press("End");
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "assistant", content: "Next plain answer. " + "Ordinary conversation. ".repeat(40),
      created_at: "2026-09-13T12:03:00Z", seq: 24, ord: 24 },
  ]));
  await expect(page.getByText("Next plain answer.", { exact: false }).first()).toBeAttached();
  await atTail(page);
});

for (const reducedMotion of ["no-preference", "reduce"] as const) {
  modelIndependentWebE2e(`short chat keeps following when expansion fits with ${reducedMotion} motion`, async ({ page, request }) => {
    await page.emulateMedia({ reducedMotion });
    const { sessionId, activity, stream, nextSeq } = await seedConversation(page, request, 2, 0);
    await activity.locator(":scope > summary").click();
    await expect(activity).not.toHaveAttribute("data-animating", "true");
    expect(await stream.evaluate(host => host.scrollHeight - host.clientHeight)).toBe(0);
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
      { id: crypto.randomUUID(), role: "assistant", content: "Following after a small expansion. " + "New content. ".repeat(300),
        created_at: "2026-09-13T12:02:00Z", seq: nextSeq, ord: nextSeq },
    ]));
    await expect(page.getByText("Following after a small expansion.", { exact: false }).first()).toBeAttached();
    await atTail(page);
  });

  modelIndependentWebE2e(`short chat expansion preserves the reader with ${reducedMotion} motion`, async ({ page, request }) => {
    await page.emulateMedia({ reducedMotion });
    const { sessionId, activity, stream, nextSeq } = await seedConversation(page, request, 48, 0);
    const summary = activity.locator(":scope > summary");
    const sample = stream.evaluate(async host => {
      const summary = host.querySelector<HTMLElement>('[data-testid="activity-span-card"] > summary')!;
      const read = () => ({ offset: host.scrollTop, summaryTop: summary.getBoundingClientRect().top });
      const samples = [read()];
      const until = performance.now() + 1800;
      while (performance.now() < until) {
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        samples.push(read());
      }
      return samples;
    });
    const expandAndAppend = async () => {
      await summary.click();
      await expect(activity).not.toHaveAttribute("data-animating", "true");
      await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
      await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
        { id: crypto.randomUUID(), role: "assistant", content: "Arriving while reading expanded content. " + "More text. ".repeat(100),
          created_at: "2026-09-13T12:02:00Z", seq: nextSeq, ord: nextSeq },
      ]));
      await expect(page.getByText("Arriving while reading expanded content.", { exact: false }).first()).toBeAttached();
    };
    const [samples] = await Promise.all([sample, expandAndAppend()]);
    expect(await stream.evaluate(host => host.scrollHeight - host.clientHeight)).toBeGreaterThan(300);
    expect(Math.max(...samples.map(s => Math.abs(s.offset)))).toBeLessThan(2);
    expect(Math.max(...samples.map(s => Math.abs(s.summaryTop - samples[0]!.summaryTop)))).toBeLessThan(2);
    expect(await stream.evaluate(host => host.scrollTop)).toBe(0);
    await page.getByTestId("stream-scroll-jump").click();
    await atTail(page);
  });
}

modelIndependentWebE2e("activity expansion holds the reading position until End resumes follow", async ({ page, request }) => {
  const { sessionId, activity, stream } = await seedConversation(page, request);
  const summary = activity.locator(":scope > summary");
  const box = await summary.boundingBox();
  if (!box) throw new Error("activity summary is missing");
  const before = box.y;
  const trace = stream.evaluate(async host => {
    const summary = host.querySelector<HTMLElement>('[data-testid="activity-span-card"] > summary')!;
    const read = () => ({ offset: host.scrollTop, summaryTop: summary.getBoundingClientRect().top, height: host.scrollHeight, viewport: host.clientHeight });
    const samples = [read()];
    const until = performance.now() + 1000;
    while (performance.now() < until) {
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      const sample = read();
      if (JSON.stringify(sample) !== JSON.stringify(samples.at(-1))) samples.push(sample);
    }
    return samples;
  });
  const [samples] = await Promise.all([trace, (async () => {
    // A locator click can reveal the summary before dispatching the input.
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
    await expect(activity).toHaveAttribute("open", "");
    await expect(activity).not.toHaveAttribute("data-animating", "true");
  })()]);
  expect(Math.max(...samples.map(sample => Math.abs(sample.summaryTop - before))), JSON.stringify(samples)).toBeLessThan(2);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "assistant", content: "Reading after expansion. " + "New content. ".repeat(100),
      created_at: "2026-09-13T12:02:00Z", seq: 23, ord: 23 },
  ]));
  await expect(page.getByText("Reading after expansion.", { exact: false }).first()).toBeAttached();
  await expect.poll(async () => Math.abs(await summary.evaluate(element => element.getBoundingClientRect().top) - before)).toBeLessThanOrEqual(1);
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
});

modelIndependentWebE2e("idle virtualized history stays on the same visible row", async ({ page, request }) => {
  const { sessionId, stream } = await seedConversation(page, request);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(
    Array.from({ length: 180 }, (_, index) => ({
      id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
      content: `History ${index}. ${"Stable reading content. ".repeat(30)}`,
      created_at: "2026-09-13T12:03:00Z", seq: index + 23, ord: index + 23,
    })),
  ));
  await expect(page.getByText("History 179.", { exact: false }).first()).toBeAttached();
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  const box = await stream.boundingBox();
  if (!box) throw new Error("chat stream is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, -1600);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  // Direct input settlement includes the initial row measurements.
  await expect.poll(() => stream.evaluate(async (host) => {
    const url = "/src/platform/scrolling/scrollport-motion.ts";
    const { scrollportMotionForViewport } = await import(/* @vite-ignore */ url);
    return scrollportMotionForViewport(host)?.isDirectInputActive();
  })).toBe(false);
  const samples = await stream.evaluate(async (host) => {
    const read = () => {
      const top = host.getBoundingClientRect().top;
      const row = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
        .find((el) => el.getBoundingClientRect().bottom > top);
      return { key: row?.dataset.msgId, offset: row ? row.getBoundingClientRect().top - top : 0, scroll: host.scrollTop };
    };
    const samples = [read()];
    const until = performance.now() + 3000;
    while (performance.now() < until) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      samples.push(read());
    }
    return samples;
  });
  expect(samples[0]!.key).toBeTruthy();
  expect(samples.every((sample) => sample.key === samples[0]!.key)).toBe(true);
  expect(Math.max(...samples.map((sample) => Math.abs(sample.offset - samples[0]!.offset)))).toBeLessThan(2);
  expect(Math.max(...samples.map((sample) => Math.abs(sample.scroll - samples[0]!.scroll)))).toBeLessThan(2);
});

modelIndependentWebE2e("expanded tool cards never bounce during later transcript updates", async ({ page, request }) => {
  const { sessionId, activity, stream } = await seedConversation(page, request);
  await activity.locator(":scope > summary").click();
  await expect(activity).not.toHaveAttribute("data-animating", "true");
  await stream.focus();
  await page.keyboard.press("End");
  await atTail(page);
  const output = activity.getByRole("button", { name: "Output in Files", exact: true });
  await expect(output).toBeVisible();
  const sample = stream.evaluate(async (host) => {
    const offsets = [host.scrollTop];
    const until = performance.now() + 4000;
    while (performance.now() < until) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      offsets.push(host.scrollTop);
    }
    return offsets;
  });
  const append = async () => {
    for (let index = 0; index < 3; index++) {
      const assistantId = crypto.randomUUID();
      const callId = `later-command-${index}`;
      await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
        { id: assistantId, role: "assistant", content: "", created_at: "2026-09-14T12:00:00Z",
          seq: 23 + index * 2, ord: 23 + index * 2,
          tool_calls: [{ id: callId, name: "command", args: { command: "pwd" } }] },
        { id: crypto.randomUUID(), role: "tool", content: "/tmp", created_at: "2026-09-14T12:00:00Z",
          seq: 24 + index * 2, ord: 24 + index * 2,
          tool_result: { tool: "command", tool_call_id: callId, assistant_message_id: assistantId, content: "/tmp" } },
      ]));
      // Include the delayed status/render pass between arrivals.
      await page.waitForTimeout(900);
    }
  };
  const [offsets] = await Promise.all([sample, append()]);
  let furthest = offsets[0]!;
  let backward = 0;
  for (const offset of offsets) {
    backward = Math.max(backward, furthest - offset);
    furthest = Math.max(furthest, offset);
  }
  expect(backward, "the complete arrival must not clamp upward and then repin").toBeLessThan(2);
  await atTail(page);
});
