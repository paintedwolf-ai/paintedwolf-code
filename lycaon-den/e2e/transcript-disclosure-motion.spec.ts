import { expect } from "@playwright/test";
import { setTranscriptSpacingForTest } from "./transcript-spacing-helpers.ts";
import { LIVE_CHAT_STAGE_SELECTOR } from "../shared/stage-selectors.ts";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";
import {
  disclosureViewportState,
  disclosureCornerState,
  startDisclosureScrollTrace,
  stopDisclosureScrollTrace,
  clickDisclosureWithoutReveal,
  expectStableScrollTrace,
  expectNonOscillatingScrollTrace,
  positionDisclosure,
  toggleAndExpectControlled,
} from "./transcript/disclosure-motion-helpers.ts";
import {
  startTailBoundaryTrace,
  stopTailBoundaryTrace,
  transcriptTailGap,
  expectTranscriptTailBounded,
} from "./transcript/tail-helpers.ts";

for (const expandedSpacing of [false, true]) {
webE2e(`transcript disclosures animate without moving the viewport${expandedSpacing ? " with expanded spacing" : ""}`, async ({
  page,
  request,
}) => {
  const ownershipWarnings: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "warning" && message.text().includes("computations created outside")) {
      ownershipWarnings.push(message.text());
    }
  });
  await bootstrapChatSession(page, request);
  if (expandedSpacing) await setTranscriptSpacingForTest(page, { rowGap: 1.9375, sectionGap: 2.5, turnGap: 4, partGap: 1.4286, paragraphGap: 1.2, userPaddingY: 1.2143 });
  const sessionId = await settledChatSessionId(page);
  const assistantMessageId = "00000000-0000-4000-8000-000000000901";

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      ...Array.from({ length: 14 }, (_, index) => ({
        id: `00000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Runway row ${index + 1}: ${"content ".repeat(10)}`,
        created_at: `2026-08-23T15:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
      {
        id: assistantMessageId,
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "tc-disclosure-motion-first",
            name: "read",
            args: { path: "first-long-output.txt" },
          },
          {
            id: "tc-disclosure-motion-middle",
            name: "read",
            args: { path: "middle-output.txt" },
          },
          {
            id: "tc-disclosure-motion-last",
            name: "read",
            args: { path: "last-output.txt" },
          },
        ],
        created_at: "2026-08-23T15:20:00Z",
        seq: 15,
        ord: 15,
      },
      {
        id: "00000000-0000-4000-8000-000000000902",
        role: "tool",
        content: Array.from(
          { length: 60 },
          (_, line) => `First tool output line ${line + 1}`,
        ).join("\n"),
        tool_result: {
          content: Array.from(
            { length: 60 },
            (_, line) => `First tool output line ${line + 1}`,
          ).join("\n"),
          tool: "read",
          tool_call_id: "tc-disclosure-motion-first",
          assistant_message_id: assistantMessageId,
        },
        created_at: "2026-08-23T15:20:01Z",
        seq: 16,
        ord: 16,
      },
      {
        id: "00000000-0000-4000-8000-000000000903",
        role: "tool",
        content: "Middle tool output",
        tool_result: {
          content: "Middle tool output",
          tool: "read",
          tool_call_id: "tc-disclosure-motion-middle",
          assistant_message_id: assistantMessageId,
        },
        created_at: "2026-08-23T15:20:02Z",
        seq: 17,
        ord: 17,
      },
      {
        id: "00000000-0000-4000-8000-000000000904",
        role: "tool",
        content: Array.from(
          { length: 30 },
          (_, line) => `Last tool output line ${line + 1}`,
        ).join("\n"),
        tool_result: {
          content: Array.from(
            { length: 30 },
            (_, line) => `Last tool output line ${line + 1}`,
          ).join("\n"),
          tool: "read",
          tool_call_id: "tc-disclosure-motion-last",
          assistant_message_id: assistantMessageId,
        },
        created_at: "2026-08-23T15:20:03Z",
        seq: 18,
        ord: 18,
      },
    ]),
  );

  const activity = page.getByTestId("activity-span-card").last();
  const summary = activity.locator(":scope > summary");
  const caret = summary.locator(".den-tool-chicklet-caret");
  const stream = page
    .locator(
      `${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`,
    )
    .first();
  await expect(activity).toBeVisible({ timeout: 30_000 });
  // Initial row measurements settle before disclosure growth.
  await page.waitForTimeout(300);

  await positionDisclosure(page, summary, "center");
  const before = await stream.evaluate((element) => ({
    scrollTop: element.scrollTop,
    scrollHeight: element.scrollHeight,
    clientHeight: element.clientHeight,
  }));
  const positionedState = await disclosureViewportState(summary);
  const positionedTail = await transcriptTailGap(page);
  expect(before.scrollHeight).toBeGreaterThan(before.clientHeight);
  const collapsedHeight = await activity.evaluate(
    (element) => element.getBoundingClientRect().height,
  );
  const collapsedCorner = await disclosureCornerState(activity);
  const summaryTop = await summary.evaluate(
    (element) => element.getBoundingClientRect().top,
  );
  const collapsedCaret = await caret.evaluate(
    (element) => getComputedStyle(element).transform,
  );

  await startDisclosureScrollTrace(summary);
  await clickDisclosureWithoutReveal(summary);
  await expect(activity).toHaveAttribute("open", "");
  await expect(activity).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const expandedHeight = await activity.evaluate(
    (element) => element.getBoundingClientRect().height,
  );
  const expandedCaret = await caret.evaluate(
    (element) => getComputedStyle(element).transform,
  );
  const expandedState = await disclosureViewportState(summary);
  const expandedCorner = await disclosureCornerState(activity);
  const openingTrace = await stopDisclosureScrollTrace(summary);
  expect(expandedHeight).toBeGreaterThan(collapsedHeight + 1);
  expect(collapsedCaret).not.toBe(expandedCaret);
  expect(expandedCorner.radius).toBe(collapsedCorner.radius);
  expect(
    Math.abs(expandedState.summaryTop - summaryTop),
    JSON.stringify({ positionedState, positionedTail, expandedState, openingTrace }),
  ).toBeLessThanOrEqual(1.5);
  expect(
    Math.abs(expandedState.scrollTop - before.scrollTop),
    JSON.stringify(expandedState),
  ).toBeLessThanOrEqual(1.5);
  expectStableScrollTrace(openingTrace);

  const nested = activity.locator(".den-tool-part > summary");
  await expect(nested).toHaveCount(3);
  const firstNested = nested.first();
  const firstNestedShell = firstNested.locator("..");
  await positionDisclosure(page, firstNested, "center");
  await toggleAndExpectControlled(
    firstNested,
    firstNestedShell,
    stream,
  );
  await expect(firstNestedShell).toHaveAttribute("open", "");

  const lastNested = nested.last();
  const lastNestedShell = lastNested.locator("..");
  await positionDisclosure(page, lastNested, "bottom");
  await toggleAndExpectControlled(lastNested, lastNestedShell, stream, {
    geometryAboveChanges: true,
  });
  await expect(firstNestedShell).not.toHaveAttribute("open", "");
  await expect(lastNestedShell).toHaveAttribute("open", "");

  await toggleAndExpectControlled(lastNested, lastNestedShell, stream);
  await expect(lastNestedShell).not.toHaveAttribute("open", "");

  const beforeParentClose = await disclosureViewportState(summary);
  await startDisclosureScrollTrace(summary);
  await clickDisclosureWithoutReveal(summary);
  await expect(activity).not.toHaveAttribute("open", "");
  await expect(activity).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const closedState = await disclosureViewportState(summary);
  const closedCorner = await disclosureCornerState(activity);
  expect(closedCorner.radius).toBe(collapsedCorner.radius);
  expect(
    closedState.summaryTop,
    JSON.stringify(closedState),
  ).toBeGreaterThanOrEqual(beforeParentClose.summaryTop - 1);
  expect(
    closedState.scrollTop,
    JSON.stringify(closedState),
  ).toBeLessThanOrEqual(beforeParentClose.scrollTop + 1);
  const closingTrace = await stopDisclosureScrollTrace(summary);
  expectNonOscillatingScrollTrace(closingTrace);
  await expectTranscriptTailBounded(page);

  await positionDisclosure(page, summary, "bottom");
  await startTailBoundaryTrace(page);
  await clickDisclosureWithoutReveal(summary);
  await expect(activity).toHaveAttribute("open", "");
  await page.mouse.wheel(0, 2_400);
  await expect(activity).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const boundaryTrace = await stopTailBoundaryTrace(page);
  expect(boundaryTrace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...boundaryTrace.overflow)).toBeLessThanOrEqual(2);
  await expectTranscriptTailBounded(page);
  await toggleAndExpectControlled(summary, activity, stream);

  await positionDisclosure(page, summary, "top");
  await toggleAndExpectControlled(summary, activity, stream);
  await toggleAndExpectControlled(summary, activity, stream);
  expect(ownershipWarnings).toEqual([]);
});

webE2e(`expanding a tall row at the tail preserves the reading position${expandedSpacing ? " with expanded spacing" : ""}`, async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  if (expandedSpacing) await setTranscriptSpacingForTest(page, { rowGap: 1.9375, sectionGap: 2.5, turnGap: 4, partGap: 1.4286, paragraphGap: 1.2, userPaddingY: 1.2143 });
  const sessionId = await settledChatSessionId(page);
  const assistantMessageId = "00000000-0000-4000-f000-000000000901";

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      ...Array.from({ length: 12 }, (_, index) => ({
        id: `00000000-0000-4000-f000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Tail runway ${index + 1}: ${"content ".repeat(12)}`,
        created_at: `2026-08-30T15:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
      {
        id: assistantMessageId,
        role: "assistant",
        content: "",
        // A span of one auto-opens its only part; two keep the tall one closed.
        tool_calls: [
          {
            id: "tc-tail-lead",
            name: "read",
            args: { path: "lead-output.txt" },
          },
          {
            id: "tc-tail-growth",
            name: "read",
            args: { path: "tall-output.txt" },
          },
        ],
        created_at: "2026-08-30T15:20:00Z",
        seq: 13,
        ord: 13,
      },
      {
        id: "00000000-0000-4000-f000-000000000902",
        role: "tool",
        content: "Lead tool output",
        tool_result: {
          content: "Lead tool output",
          tool: "read",
          tool_call_id: "tc-tail-lead",
          assistant_message_id: assistantMessageId,
        },
        created_at: "2026-08-30T15:20:01Z",
        seq: 14,
        ord: 14,
      },
      {
        id: "00000000-0000-4000-f000-000000000903",
        role: "tool",
        content: Array.from(
          { length: 240 },
          (_, line) => `Tall tool output line ${line + 1}`,
        ).join("\n"),
        tool_result: {
          content: Array.from(
            { length: 240 },
            (_, line) => `Tall tool output line ${line + 1}`,
          ).join("\n"),
          tool: "read",
          tool_call_id: "tc-tail-growth",
          assistant_message_id: assistantMessageId,
        },
        created_at: "2026-08-30T15:20:02Z",
        seq: 15,
        ord: 15,
      },
    ]),
  );

  const activity = page.getByTestId("activity-span-card").last();
  const stream = page
    .locator(
      `${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`,
    )
    .first();
  await expect(activity).toBeVisible({ timeout: 30_000 });
  await page.waitForTimeout(300);

  await clickDisclosureWithoutReveal(activity.locator(":scope > summary"));
  await expect(activity).toHaveAttribute("open", "");
  await expect(activity).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });

  const nested = activity.locator(".den-tool-part > summary").last();
  const nestedShell = nested.locator("..");
  await expect(nestedShell).not.toHaveAttribute("open", "");
  await page.waitForTimeout(300);

  // Returning to the bottom re-arms the scrollport tail pin.
  const box = await stream.boundingBox();
  if (!box) throw new Error("chat viewport is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, -400);
  await page.waitForTimeout(240);
  await page.mouse.wheel(0, 2_400);
  // Past the wheel settle window, so the click carries no residual input.
  await page.waitForTimeout(900);
  await expectTranscriptTailBounded(page);
  const before = await stream.evaluate((element) => element.scrollTop);
  const collapsedHeight = await nestedShell.evaluate(
    (element) => element.getBoundingClientRect().height,
  );

  await startDisclosureScrollTrace(nested);
  await clickDisclosureWithoutReveal(nested);
  await expect(nestedShell).toHaveAttribute("open", "");
  await expect(nestedShell).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const trace = await stopDisclosureScrollTrace(nested);
  const expandedHeight = await nestedShell.evaluate(
    (element) => element.getBoundingClientRect().height,
  );
  const after = await disclosureViewportState(nested);

  // The entire expansion stays at the clicked row, including animation frames.
  expect(expandedHeight - collapsedHeight).toBeGreaterThan(150);
  expect(Math.abs(after.scrollTop - before), JSON.stringify({ after, trace })).toBeLessThan(2);
  expect(Math.max(...trace.samples.map(sample => Math.abs(sample.scrollTop - before)))).toBeLessThan(2);
  expect(Math.max(...trace.samples.map(sample => Math.abs(sample.controlTop - trace.samples[0]!.controlTop)))).toBeLessThan(2);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  await page.getByTestId("stream-scroll-jump").click();
  await expect.poll(async () => {
    const { gap, clearance } = await transcriptTailGap(page);
    return Math.abs(gap - clearance);
  }).toBeLessThan(2);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
});

}
