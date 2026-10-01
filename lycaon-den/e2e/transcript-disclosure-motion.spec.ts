import { setTranscriptSpacingForTest } from "./transcript-spacing-helpers.ts";
import { expect, type Locator, type Page } from "@playwright/test";
import { LIVE_CHAT_STAGE_SELECTOR } from "../shared/stage-selectors.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "../src/platform/scrolling/scrollport-motion.ts";
import {
  apiConfig,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

async function disclosureViewportState(summary: Locator) {
  return summary.evaluate((control) => {
    const viewport = control
      .closest(".den-shell-stage--chat")
      ?.querySelector<HTMLElement>(".den-chat-stream");
    const row = control.closest<HTMLElement>(".transcript-viewport-row");
    const section = row?.closest<HTMLElement>(".den-chat-stream-inner");
    return {
      summaryTop: control.getBoundingClientRect().top,
      rowTop: row?.getBoundingClientRect().top ?? -1,
      virtualStart: Number(row?.dataset.virtualStart ?? -1),
      beforeRunway: Number(
        section?.querySelector<HTMLElement>(
          '[data-transcript-runway="before"]',
        )?.style.height.replace("px", "") ?? -1,
      ),
      afterRunway: Number(
        section?.querySelector<HTMLElement>(
          '[data-transcript-runway="after"]',
        )?.style.height.replace("px", "") ?? -1,
      ),
      scrollTop: viewport?.scrollTop ?? -1,
      scrollHeight: viewport?.scrollHeight ?? -1,
      clientHeight: viewport?.clientHeight ?? -1,
    };
  });
}

async function disclosureCornerState(shell: Locator) {
  return shell.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      radius: style.borderRadius,
      clipPath: style.clipPath,
    };
  });
}

type DisclosureScrollTrace = {
  active: boolean;
  samples: Array<{ scrollTop: number; controlTop: number }>;
};

type TailBoundaryTrace = {
  active: boolean;
  overflow: number[];
  samples: Array<{
    phase: string;
    overflow: number;
    scrollTop: number;
    scrollHeight: number;
    clientHeight: number;
    viewportWidth: number;
    transcriptEnd: number;
    afterRunway: number;
  }>;
};

async function startTailBoundaryTrace(page: Page) {
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      const body = host.querySelector<HTMLElement>(".den-chat-stream-body");
      if (!body) throw new Error("transcript body is missing");
      const trace: TailBoundaryTrace = {
        active: true,
        overflow: [],
        samples: [],
      };
      (
        window as Window & { __denTailBoundaryTrace?: TailBoundaryTrace }
      ).__denTailBoundaryTrace = trace;
      const sample = () => {
        if (!trace.active) return;
        const transcriptEnd = host.querySelector<HTMLElement>(
          "[data-transcript-end]",
        );
        if (transcriptEnd) {
          const clearance =
            Number.parseFloat(getComputedStyle(body).paddingBottom) || 0;
          const overflow =
            host.getBoundingClientRect().bottom -
              transcriptEnd.getBoundingClientRect().bottom -
              clearance;
          trace.overflow.push(overflow);
          trace.samples.push({
            phase:
              (window as Window & { __denTailBoundaryPhase?: string })
                .__denTailBoundaryPhase ?? "",
            overflow,
            scrollTop: host.scrollTop,
            scrollHeight: host.scrollHeight,
            clientHeight: host.clientHeight,
            viewportWidth: host.clientWidth,
            transcriptEnd: transcriptEnd.getBoundingClientRect().bottom,
            afterRunway:
              Number.parseFloat(
                host.querySelector<HTMLElement>(
                  '[data-transcript-runway="after"]',
                )?.style.height ?? "",
              ) || 0,
          });
        }
        // ResizeObserver delivery follows rAF; sample after the render callbacks.
        requestAnimationFrame(() => setTimeout(sample, 0));
      };
      requestAnimationFrame(() => setTimeout(sample, 0));
    });
}

async function stopTailBoundaryTrace(page: Page): Promise<TailBoundaryTrace> {
  return page.evaluate(() => {
    const target = window as Window & {
      __denTailBoundaryTrace?: TailBoundaryTrace;
    };
    const trace = target.__denTailBoundaryTrace;
    if (!trace) throw new Error("tail boundary trace is missing");
    trace.active = false;
    delete target.__denTailBoundaryTrace;
    return trace;
  });
}

async function setTailBoundaryTracePhase(page: Page, phase: string) {
  await page.evaluate((nextPhase) => {
    (
      window as Window & { __denTailBoundaryPhase?: string }
    ).__denTailBoundaryPhase = nextPhase;
  }, phase);
}

async function startDisclosureScrollTrace(control: Locator) {
  await control.evaluate((element) => {
    const viewport = element
      .closest(".den-shell-stage--chat")
      ?.querySelector<HTMLElement>(".den-chat-stream");
    if (!viewport) throw new Error("chat viewport is missing");
    const trace: DisclosureScrollTrace = {
      active: true,
      samples: [
        {
          scrollTop: viewport.scrollTop,
          controlTop: element.getBoundingClientRect().top,
        },
      ],
    };
    (window as Window & { __denDisclosureScrollTrace?: DisclosureScrollTrace })
      .__denDisclosureScrollTrace = trace;
    const sample = () => {
      if (!trace.active) return;
      trace.samples.push({
        scrollTop: viewport.scrollTop,
        controlTop: element.getBoundingClientRect().top,
      });
      requestAnimationFrame(sample);
    };
    requestAnimationFrame(sample);
  });
}

async function stopDisclosureScrollTrace(
  control: Locator,
): Promise<DisclosureScrollTrace> {
  return control.evaluate(() => {
    const target = window as Window & {
      __denDisclosureScrollTrace?: DisclosureScrollTrace;
    };
    const trace = target.__denDisclosureScrollTrace;
    if (!trace) throw new Error("disclosure scroll trace is missing");
    trace.active = false;
    delete target.__denDisclosureScrollTrace;
    return trace;
  });
}

async function clickDisclosureWithoutReveal(control: Locator) {
  await control.dispatchEvent("pointerdown", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
    pointerId: 1,
    pointerType: "mouse",
  });
  await control.dispatchEvent("pointerup", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
    pointerId: 1,
    pointerType: "mouse",
  });
  await control.dispatchEvent("click", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
  });
}

function expectStableScrollTrace(
  trace: DisclosureScrollTrace,
  keys: ReadonlyArray<"scrollTop" | "controlTop"> = [
    "scrollTop",
    "controlTop",
  ],
) {
  expect(trace.samples.length).toBeGreaterThan(2);
  for (const key of keys) {
    const values = trace.samples.map((sample) => sample[key]);
    expect(
      Math.max(...values) - Math.min(...values),
      JSON.stringify({ key, samples: trace.samples }),
    ).toBeLessThanOrEqual(1);
  }
}

function expectNonOscillatingScrollTrace(trace: DisclosureScrollTrace) {
  expect(trace.samples.length).toBeGreaterThan(2);
  for (const key of ["scrollTop", "controlTop"] as const) {
    const directions = new Set<number>();
    for (let index = 1; index < trace.samples.length; index += 1) {
      const delta = trace.samples[index]![key] - trace.samples[index - 1]![key];
      if (Math.abs(delta) > 1) directions.add(Math.sign(delta));
    }
    expect(directions.size, JSON.stringify({ key, samples: trace.samples })).toBeLessThanOrEqual(1);
  }
}

async function transcriptTailGap(page: Page) {
  return page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      const body = host.querySelector<HTMLElement>(".den-chat-stream-body");
      const transcriptEnd = host.querySelector<HTMLElement>(
        "[data-transcript-end]",
      );
      const afterRunway = host.querySelector<HTMLElement>(
        '[data-transcript-runway="after"]',
      );
      const heldExtent = host.querySelector<HTMLElement>(
        "[data-scrollport-extent-hold]",
      );
      if (!body || !transcriptEnd) {
        throw new Error("transcript tail is unavailable");
      }
      return {
        gap:
          host.getBoundingClientRect().bottom -
          transcriptEnd.getBoundingClientRect().bottom,
        clearance: Number.parseFloat(getComputedStyle(body).paddingBottom) || 0,
        afterRunway: Number.parseFloat(afterRunway?.style.height ?? "") || 0,
        heldExtent: Number.parseFloat(heldExtent?.style.height ?? "") || 0,
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        clientHeight: host.clientHeight,
      };
    });
}

async function expectTranscriptTailBounded(page: Page) {
  await expect.poll(async () => {
    const state = await transcriptTailGap(page);
    return (
      state.heldExtent <= 1 &&
      state.gap <= state.clearance + 2
    )
      ? "bounded"
      : JSON.stringify(state);
  }).toBe("bounded");
}

async function placeTranscriptAtTail(page: Page) {
  await page.evaluate(() => document.fonts.ready.then(() => undefined));
  await expect.poll(() => page.locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => host.scrollHeight > host.clientHeight ? "scrollable" : JSON.stringify({
      scrollHeight: host.scrollHeight,
      clientHeight: host.clientHeight,
      rows: host.querySelectorAll(".transcript-viewport-row").length,
      height: host.getBoundingClientRect().height,
      overflow: getComputedStyle(host).overflowY,
    }))).toBe("scrollable");
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      host.scrollTop = host.scrollHeight;
      host.dispatchEvent(new Event("scroll"));
    });
  // The initial scroll mounts rows that still need measurement.
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  ));
  await expectTranscriptTailBounded(page);
}

async function placeTranscriptNearTail(page: Page) {
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host, inputEvent) => {
      host.scrollTop = Math.max(
        0,
        host.scrollHeight - host.clientHeight - 36,
      );
      host.dispatchEvent(new Event("scroll"));
      host.dispatchEvent(new CustomEvent(inputEvent));
    }, DEN_SCROLLPORT_INPUT_EVENT);
}

async function positionDisclosure(
  page: Page,
  control: Locator,
  alignment: "top" | "center" | "bottom",
) {
  const viewport = page
    .locator(
      `${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`,
    )
    .first();
  const box = await viewport.boundingBox();
  if (!box) throw new Error("chat viewport is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const delta = await control.evaluate(
      (element, requestedAlignment) => {
        const viewport = element
          .closest(".den-shell-stage--chat")
          ?.querySelector<HTMLElement>(
            ".den-chat-stream",
          );
        if (!viewport) throw new Error("chat viewport is missing");
        const viewportBox = viewport.getBoundingClientRect();
        const controlBox = element.getBoundingClientRect();
        const inset = 12;
        const desiredTop =
          requestedAlignment === "top"
            ? viewportBox.top + inset
            : requestedAlignment === "bottom"
              ? viewportBox.bottom - controlBox.height - inset
              : viewportBox.top + (viewportBox.height - controlBox.height) / 2;
        return controlBox.top - desiredTop;
      },
      alignment,
    );
    if (Math.abs(delta) <= 2) break;
    await page.mouse.wheel(0, delta);
    await page.waitForTimeout(180);
  }
  // Reader scrolling pauses follow before the disclosure click.
  await page.mouse.wheel(0, -4);
  await page.waitForTimeout(180);
  await control.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}

async function toggleAndExpectControlled(
  control: Locator,
  shell: Locator,
  stream: Locator,
  opts?: { geometryAboveChanges?: boolean },
) {
  const before = await stream.evaluate((element) => element.scrollTop);
  const wasOpen = await shell.getAttribute("open");
  await startDisclosureScrollTrace(control);
  await clickDisclosureWithoutReveal(control);
  if (wasOpen === null) await expect(shell).toHaveAttribute("open", "");
  else await expect(shell).not.toHaveAttribute("open", "");
  await expect(shell).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const trace = await stopDisclosureScrollTrace(control);
  if (wasOpen === null) {
    expectStableScrollTrace(
      trace,
      opts?.geometryAboveChanges ? ["scrollTop"] : undefined,
    );
    expect(
      Math.abs(
        (await stream.evaluate((element) => element.scrollTop)) - before,
      ),
      JSON.stringify({ before, after: await stream.evaluate((element) => element.scrollTop), control: await control.textContent(), trace }),
    ).toBeLessThanOrEqual(1);
  } else {
    expectNonOscillatingScrollTrace(trace);
  }
}

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

webE2e("viewport contractions never leave the transcript below its latest row", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  await page.setViewportSize({ width: 720, height: 620 });
  const sessionId = await settledChatSessionId(page);

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      ...Array.from({ length: 24 }, (_, index) => ({
        id: `00000000-0000-4000-a000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Resize runway ${index + 1}: ${"wrapping content ".repeat(28)}`,
        created_at: `2026-08-23T16:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ]),
  );

  await placeTranscriptAtTail(page);

  for (const size of [
    { width: 1_440, height: 620 },
    { width: 820, height: 760 },
    { width: 1_280, height: 840 },
    { width: 720, height: 620 },
  ]) {
    await page.setViewportSize(size);
    await expectTranscriptTailBounded(page);
  }

  await placeTranscriptNearTail(page);
  for (const size of [
    { width: 1_520, height: 540 },
    { width: 680, height: 900 },
    { width: 1_360, height: 700 },
    { width: 760, height: 640 },
    { width: 1_440, height: 820 },
  ]) {
    await page.setViewportSize(size);
  }
  await expectTranscriptTailBounded(page);
});

webE2e("composer asks and approvals cannot publish space below the transcript", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 18 }, (_, index) => ({
        id: `00000000-0000-4000-b000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Composer runway ${index + 1}: ${"content ".repeat(18)}`,
        created_at: `2026-08-23T17:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row").getByText("Composer runway 18:", { exact: false }),
  ).toBeAttached({ timeout: 30_000 });
  await placeTranscriptAtTail(page);

  const { apiUrl, token } = apiConfig();
  const headers = {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
  const stage = liveChatStage(page);
  await startTailBoundaryTrace(page);

  // Textarea resizing leaves optional-card presence unchanged.
  const composer = stage.getByTestId("chat-composer");
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const composerScrollTop = await stream.evaluate((element) => element.scrollTop);
  await setTailBoundaryTracePhase(page, "composer growth");
  await composer.fill(
    Array.from({ length: 14 }, (_, index) =>
      `Composer growth line ${index + 1}: ${"wrapping text ".repeat(8)}`,
    ).join("\n"),
  );
  // Following keeps the latest row in view above the growing composer.
  await expect
    .poll(() => stream.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(composerScrollTop);
  await expect.poll(async () => {
    const state = await transcriptTailGap(page);
    return Math.abs(state.gap - state.clearance) <= 2
      ? "at tail"
      : JSON.stringify(state);
  }).toBe("at tail");
  await setTailBoundaryTracePhase(page, "composer contraction");
  await composer.fill("");
  await expectTranscriptTailBounded(page);

  await setTailBoundaryTracePhase(page, "ask dock");
  const ask = await request.post(`${apiUrl}/harness/ask_user`, {
    headers,
    data: {
      session_id: sessionId,
      prompt: `Choose a direction. ${"More context. ".repeat(20)}`,
    },
  });
  expect(ask.ok(), await ask.text()).toBeTruthy();
  const askDock = stage.getByTestId("ask-user-dock");
  await expect(askDock).toBeVisible({ timeout: 30_000 });
  await askDock.getByTestId("ask-user-dock-minimize").click();
  await expect(askDock).toHaveAttribute("data-minimized", "");
  await askDock.getByTestId("ask-user-dock-expand").click();
  await expect(askDock).not.toHaveAttribute("data-minimized", "");

  await setTailBoundaryTracePhase(page, "approval details");
  const approval = await request.post(
    `${apiUrl}/harness/checkpoints/tool_approval`,
    {
      headers,
      data: {
        session_id: sessionId,
        command: `printf '%s' '${"approval detail ".repeat(12)}'`,
      },
    },
  );
  expect(approval.ok(), await approval.text()).toBeTruthy();
  const card = stage.getByTestId("tool-approval-card");
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.getByTestId("approval-card-minimize").click();
  await card.getByTestId("approval-card-expand").click();

  const resolveApproval = async (checkpointId: string) => {
    await setTailBoundaryTracePhase(page, "approval resolution");
    const pending = stage.locator(
      `[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`,
    );
    const resolved = page.waitForResponse(
      (response) =>
        response.url().includes(`/checkpoints/${checkpointId}`) &&
        response.request().method() === "POST",
      { timeout: 30_000 },
    );
    await pending.getByTestId("approval-approve-primary").click();
    expect((await resolved).ok()).toBeTruthy();
    await expect(pending).toHaveCount(0, { timeout: 30_000 });
  };

  const firstCheckpointId = await card.getAttribute("data-checkpoint-id");
  expect(firstCheckpointId).toBeTruthy();
  if (!firstCheckpointId) throw new Error("approval checkpoint id is missing");
  await resolveApproval(firstCheckpointId);
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);
  await expectTranscriptTailBounded(page);

  const injectApproval = async (command: string) => {
    const response = await request.post(
      `${apiUrl}/harness/checkpoints/tool_approval`,
      { headers, data: { session_id: sessionId, command } },
    );
    const body = (await response.json()) as { checkpoint_id?: string };
    expect(response.ok(), JSON.stringify(body)).toBeTruthy();
    expect(body.checkpoint_id).toBeTruthy();
    if (!body.checkpoint_id) throw new Error("injected checkpoint id is missing");
    return body.checkpoint_id;
  };

  const tallCheckpointId = await injectApproval(
    `printf '%s' '${"large replacement approval ".repeat(28)}'`,
  );
  const shortCheckpointId = await injectApproval("pwd");
  const tallCard = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${tallCheckpointId}"]`,
  );
  await expect(tallCard).toBeVisible({ timeout: 30_000 });
  await expect(stage.getByTestId("checkpoint-queue-row")).toHaveCount(1);
  await expect(tallCard.getByRole("button", {name:"Approval details in Files",exact:true})).toBeVisible();

  const shortCard = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${shortCheckpointId}"]`,
  );
  // Queue replacement keeps slot presence stable.
  await setTailBoundaryTracePhase(page, "approval replacement");
  await stage
    .locator(
      `[data-testid="checkpoint-queue-row"][data-checkpoint-id="${shortCheckpointId}"]`,
    )
    .click();
  await expect(shortCard).toBeVisible();
  await expectTranscriptTailBounded(page);
  await stage
    .locator(
      `[data-testid="checkpoint-queue-row"][data-checkpoint-id="${tallCheckpointId}"]`,
    )
    .click();
  await expect(tallCard).toBeVisible();
  await expectTranscriptTailBounded(page);
  await resolveApproval(tallCheckpointId);

  await expect(shortCard).toBeVisible({ timeout: 30_000 });
  await expectTranscriptTailBounded(page);
  await resolveApproval(shortCheckpointId);
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);

  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow), JSON.stringify(trace.samples.filter((sample) => sample.overflow > 2)))
    .toBeLessThanOrEqual(2);
});

webE2e("approval resolution cannot leave the viewport below the transcript", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 20 }, (_, index) => ({
        id: `00000000-0000-4000-c000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Approval removal runway ${index + 1}: ${"content ".repeat(20)}`,
        created_at: `2026-08-23T18:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row").getByText("Approval removal runway 20:", { exact: false }),
  ).toBeAttached({ timeout: 30_000 });
  await placeTranscriptAtTail(page);

  const { apiUrl, token } = apiConfig();
  const headers = {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
  const injected = await request.post(
    `${apiUrl}/harness/checkpoints/tool_approval`,
    {
      headers,
      data: {
        session_id: sessionId,
        command: `printf '%s' '${"approval removal detail ".repeat(36)}'`,
      },
    },
  );
  const injectedBody = (await injected.json()) as { checkpoint_id?: string };
  expect(injected.ok(), JSON.stringify(injectedBody)).toBeTruthy();
  const checkpointId = injectedBody.checkpoint_id;
  if (!checkpointId) throw new Error("injected checkpoint id is missing");

  const stage = liveChatStage(page);
  const card = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`,
  );
  await expect(card).toBeVisible({ timeout: 30_000 });
  await expect(card.getByRole("button", {name:"Approval details in Files",exact:true})).toBeVisible();
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  const resolved = page.waitForResponse(
    (response) =>
      response.url().includes(`/checkpoints/${checkpointId}`) &&
      response.request().method() === "POST",
    { timeout: 30_000 },
  );
  await card.getByTestId("approval-approve-primary").click();
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const streamBox = await stream.boundingBox();
  if (!streamBox) throw new Error("chat viewport is missing");
  await page.mouse.move(
    streamBox.x + streamBox.width / 2,
    streamBox.y + streamBox.height / 2,
  );
  await page.mouse.wheel(0, 2_400);
  expect((await resolved).ok()).toBeTruthy();

  await expect(card).toHaveCount(0, { timeout: 30_000 });
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);
  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow)).toBeLessThanOrEqual(2);

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: "00000000-0000-4000-c000-000000000021",
        role: "assistant",
        content: `Approval follow-up: ${"new tail content ".repeat(40)}`,
        created_at: "2026-08-23T18:21:00Z",
        seq: 21,
        ord: 21,
      },
    ]),
  );
  await expect(stage.locator(".transcript-viewport-row").getByText("Approval follow-up:", { exact: false })).toBeVisible({
    timeout: 30_000,
  });
  await expectTranscriptTailBounded(page);
});

webE2e("delayed transcript content cannot publish scroll space past its end", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 18 }, (_, index) => ({
        id: `00000000-0000-4000-d000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Delayed content runway ${index + 1}: ${"content ".repeat(18)}`,
        created_at: `2026-08-23T19:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  const stage = liveChatStage(page);
  const lastRow = stage.locator(".transcript-viewport-row[data-msg-id]").last();
  await lastRow.evaluate((row) => {
    const delayed = document.createElement("div");
    delayed.dataset.testDelayedGeometry = "";
    delayed.style.height = "720px";
    row.append(delayed);
  });
  await page.waitForTimeout(100);
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const box = await stream.boundingBox();
  if (!box) throw new Error("chat viewport is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, 2_400);
  await lastRow.evaluate((row) => {
    row.querySelector("[data-test-delayed-geometry]")?.remove();
  });
  await page.setViewportSize({ width: 760, height: 680 });
  await expectTranscriptTailBounded(page);

  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow)).toBeLessThanOrEqual(2);
});

webE2e("split sidebar restoration stays inside the transcript tail", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1_500, height: 720 });
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 26 }, (_, index) => ({
        id: `00000000-0000-4000-e000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Split resize runway ${index + 1}: ${"width-sensitive transcript content ".repeat(14)}`,
        created_at: `2026-08-23T20:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );

  await page
    .getByTestId("focused-project-nav")
    .getByRole("button", { name: "Files", exact: true })
    .click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();

  await expect(liveChatStage(page).locator('.transcript-viewport-row[data-msg-id]').first()).toBeVisible();
  await expect.poll(() => liveChatStage(page).locator('.den-chat-stream').evaluate((host) => host.scrollHeight > host.clientHeight)).toBe(true);
  await placeTranscriptAtTail(page);
  const row = liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").last();
  const beforeDrag = await row.boundingBox();
  const divider = page.getByTestId("split-divider");
  const edge = await divider.boundingBox();
  expect(beforeDrag).not.toBeNull();
  expect(edge).not.toBeNull();
  await page.mouse.move(edge!.x + edge!.width / 2, edge!.y + 60);
  await page.mouse.down();
  try {
    await page.mouse.move(edge!.x + 120, edge!.y + 60, { steps: 8 });
    // Dragging changes both row width and wrapped text height.
    await expect.poll(async () => Math.abs((await row.boundingBox())!.width - beforeDrag!.width)).toBeGreaterThan(50);
    await expect.poll(async () => Math.abs((await row.boundingBox())!.height - beforeDrag!.height)).toBeGreaterThan(5);
  } finally {
    await page.mouse.up();
  }

  await page.setViewportSize({ width: 1_300, height: 720 });
  const restore = page.getByTestId("nav-auto-restore-btn").first();
  await expect(restore).toBeVisible();
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  await setTailBoundaryTracePhase(page, "first restore click");
  await restore.click();
  await setTailBoundaryTracePhase(page, "first widen");
  await page.setViewportSize({ width: 1_700, height: 720 });
  await expect(page.getByTestId("nav-collapse-btn")).toBeVisible();
  await setTailBoundaryTracePhase(page, "second contraction");
  await page.setViewportSize({ width: 1_280, height: 720 });
  await expect(restore).toBeVisible();
  await setTailBoundaryTracePhase(page, "second restore click");
  await restore.click();
  await setTailBoundaryTracePhase(page, "second widen");
  await page.setViewportSize({ width: 1_700, height: 720 });

  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  const worst = trace.samples.reduce((left, right) =>
    right.overflow > left.overflow ? right : left,
  );
  expect(worst.overflow, JSON.stringify(worst)).toBeLessThanOrEqual(2);
});
