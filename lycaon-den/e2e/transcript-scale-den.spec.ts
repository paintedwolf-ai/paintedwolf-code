import { expect, type Page } from "@playwright/test";
import type { Message } from "../src/api/types.ts";
import {
  apiGetActiveWorkflowRun,
  apiSeedSessionTranscript,
  apiStartWorkflowRun,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

const SESSION_LEN = 500;
const MAX_MOUNTED_TRANSCRIPT_ROWS = 80;

type Msg = Pick<Message, "id" | "content" | "created_at" | "seq" | "ord"> & { role: "user" | "assistant" };

function synthMessages(n: number): Msg[] {
  return Array.from({ length: n }, (_, i) => {
    const ord = i + 1;
    return {
      id: crypto.randomUUID(),
      role: (ord % 2 === 0 ? "user" : "assistant") as "user" | "assistant",
      content: `scale row ${ord}`,
      created_at: "2026-01-01T00:00:00Z",
      seq: ord,
      ord,
    };
  });
}

async function mountedRowCount(page: Page): Promise<number> {
  return page.locator(".transcript-viewport-row").count();
}

async function scrollSurfaceContract(page: Page): Promise<{
  activitySettles: boolean;
  customVerticalScrollbar: boolean;
  gutter: string;
  scrollbarWidth: string;
}> {
  return page.evaluate(async () => {
    const host = document.querySelector<HTMLElement>(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream");
    if (!host) throw new Error("chat scroll host is unavailable");

    host.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    const active = host.hasAttribute("data-den-scrolling");
    host.dispatchEvent(new Event("scrollend"));
    await Promise.resolve();

    return {
      activitySettles:
        active &&
        !host.hasAttribute("data-den-scrolling") &&
        !document.documentElement.hasAttribute("data-den-scrolling"),
      customVerticalScrollbar:
        host.parentElement?.querySelector(".os-scrollbar-vertical .os-scrollbar-handle") !=
        null,
      gutter: getComputedStyle(host).scrollbarGutter,
      scrollbarWidth: getComputedStyle(host).scrollbarWidth,
    };
  });
}

async function wheelStreamUp(page: Page): Promise<void> {
  const viewport = page
    .locator(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream")
    .first();
  const box = await viewport.boundingBox();
  if (!box) throw new Error("transcript viewport has no wheel target");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, -1_200);
}

async function scrollStreamToMiddle(page: Page): Promise<void> {
  await page.evaluate(() => {
    const host = document.querySelector(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream");
    if (!(host instanceof HTMLElement)) return;
    host.scrollTop = Math.max(
      0,
      (host.scrollHeight - host.clientHeight) / 2,
    );
    host.dispatchEvent(new Event("scroll"));
  });
}

async function transcriptRunwayStatus(page: Page): Promise<string> {
  return page.evaluate(() => {
    const host = document.querySelector(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream");
    if (!(host instanceof HTMLElement)) return "unavailable: host";
    const stream = host.querySelector('[data-testid="message-stream"]');
    if (!(stream instanceof HTMLElement)) return "unavailable: stream";
    const rows = [
      ...stream.querySelectorAll<HTMLElement>(".transcript-viewport-row"),
    ];
    if (rows.length === 0 || host.clientHeight <= 0) {
      return "unavailable: rows or viewport";
    }

    const viewportRect = host.getBoundingClientRect();
    const firstTop = Math.min(
      ...rows.map((row) => row.getBoundingClientRect().top),
    );
    const lastBottom = Math.max(
      ...rows.map((row) => row.getBoundingClientRect().bottom),
    );
    const maxScroll = Math.max(
      0,
      host.scrollHeight - host.clientHeight,
    );
    const tolerance = 2;
    const beforePx = viewportRect.top - firstTop;
    const afterPx = lastBottom - viewportRect.bottom;
    const beforeCovered =
      host.scrollTop <= host.clientHeight + tolerance ||
      beforePx >= host.clientHeight - tolerance;
    const afterCovered =
      maxScroll - host.scrollTop <= host.clientHeight + tolerance ||
      afterPx >= host.clientHeight - tolerance;
    return beforeCovered && afterCovered
      ? "covered"
      : `before=${beforePx}/${host.clientHeight} after=${afterPx}/${host.clientHeight} scroll=${host.scrollTop}/${maxScroll}`;
  });
}

async function transcriptRowFlowStatus(page: Page): Promise<string> {
  return page.evaluate(() => {
    const rows = [
      ...document.querySelectorAll<HTMLElement>(".transcript-viewport-row"),
    ];
    for (const row of rows) {
      if (getComputedStyle(row).position === "absolute") {
        return `absolute:${row.dataset.msgId ?? "unknown"}`;
      }
    }
    for (let index = 1; index < rows.length; index += 1) {
      const previous = rows[index - 1]!.getBoundingClientRect();
      const current = rows[index]!.getBoundingClientRect();
      if (current.top + 0.5 < previous.bottom) {
        const previousId = rows[index - 1]!.dataset.msgId ?? "unknown";
        const currentId = rows[index]!.dataset.msgId ?? "unknown";
        return `overlap:${previousId}:${currentId}:${previous.bottom - current.top}`;
      }
    }
    return "separated";
  });
}

type TranscriptScrollState = {
  index: number | null;
  scrollTop: number;
  maxTop: number;
};

type TranscriptScrollSample = TranscriptScrollState & {
  viewportHeight: number;
  firstVisibleKey: string | null;
  firstVisibleTop: number | null;
};

async function transcriptScrollSample(page: Page): Promise<TranscriptScrollSample> {
  return page.evaluate(() => {
    const viewport = document.querySelector<HTMLElement>(
      ".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream",
    );
    if (!viewport) throw new Error("transcript viewport is unavailable");
    const viewportTop = viewport.getBoundingClientRect().top;
    const row = [
      ...document.querySelectorAll<HTMLElement>(
        ".transcript-viewport-row[data-index]",
      ),
    ].find((candidate) => candidate.getBoundingClientRect().bottom > viewportTop);
    const index = Number.parseInt(row?.dataset.index ?? "", 10);
    return {
      index: Number.isFinite(index) ? index : null,
      scrollTop: viewport.scrollTop,
      maxTop: Math.max(0, viewport.scrollHeight - viewport.clientHeight),
      viewportHeight: viewport.clientHeight,
      firstVisibleKey: row?.dataset.msgId ?? null,
      firstVisibleTop: row
        ? row.getBoundingClientRect().top - viewportTop
        : null,
    };
  });
}

async function settleTranscriptLayout(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready;
    await new Promise<void>((resolve, reject) => {
      let previous = "";
      let stableFrames = 0;
      let frames = 0;
      const sample = () => {
        const viewport = document.querySelector<HTMLElement>(
          ".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream",
        );
        if (!viewport) return "";
        const rows = [
          ...document.querySelectorAll<HTMLElement>(
            ".transcript-viewport-row[data-index]",
          ),
        ];
        return [
          viewport.scrollHeight,
          viewport.clientHeight,
          viewport.scrollTop,
          ...rows.flatMap((row) => {
            const rect = row.getBoundingClientRect();
            return [row.dataset.msgId ?? "", rect.top, rect.height];
          }),
        ].join(":");
      };
      const tick = () => {
        frames += 1;
        const next = sample();
        const animating = document.getAnimations().some((animation) =>
          (animation.playState === "running" || animation.pending) &&
          Number.isFinite(animation.effect?.getComputedTiming().endTime),
        );
        stableFrames = !animating && next && next === previous ? stableFrames + 1 : 0;
        previous = next;
        if (stableFrames >= 4) {
          resolve();
          return;
        }
        if (frames >= 120) {
          reject(new Error("transcript layout did not settle"));
          return;
        }
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
  });
}

async function startTranscriptScrollTrace(page: Page): Promise<void> {
  await page.evaluate(() => {
    const viewport = document.querySelector<HTMLElement>(
      ".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream",
    );
    if (!viewport) throw new Error("transcript viewport is unavailable");
    const traceWindow = window as typeof window & {
      __transcriptScrollTrace?: TranscriptScrollSample[];
      __stopTranscriptScrollTrace?: () => void;
    };
    traceWindow.__stopTranscriptScrollTrace?.();
    const samples: TranscriptScrollSample[] = [];
    let sampleFrame: number | undefined;
    let sampleTimer: ReturnType<typeof setTimeout> | undefined;
    const sample = () => {
      const viewportTop = viewport.getBoundingClientRect().top;
      const row = [
        ...document.querySelectorAll<HTMLElement>(
          ".transcript-viewport-row[data-index]",
        ),
      ].find((candidate) => candidate.getBoundingClientRect().bottom > viewportTop);
      const parsed = Number.parseInt(row?.dataset.index ?? "", 10);
      samples.push({
        index: Number.isFinite(parsed) ? parsed : null,
        scrollTop: viewport.scrollTop,
        maxTop: Math.max(0, viewport.scrollHeight - viewport.clientHeight),
        viewportHeight: viewport.clientHeight,
        firstVisibleKey: row?.dataset.msgId ?? null,
        firstVisibleTop: row
          ? row.getBoundingClientRect().top - viewportTop
          : null,
      });
    };
    const onScroll = () => {
      if (sampleFrame !== undefined) return;
      sampleFrame = requestAnimationFrame(() => {
        sampleFrame = undefined;
        // ResizeObserver applies virtual row compensation before paint.
        // A frame callback sees the intermediate layout, not the painted position.
        sampleTimer = setTimeout(() => { sampleTimer = undefined; sample(); }, 0);
      });
    };
    viewport.addEventListener("scroll", onScroll, { passive: true });
    sample();
    traceWindow.__transcriptScrollTrace = samples;
    traceWindow.__stopTranscriptScrollTrace = () => {
      viewport.removeEventListener("scroll", onScroll);
      if (sampleFrame !== undefined) cancelAnimationFrame(sampleFrame);
      if (sampleTimer !== undefined) clearTimeout(sampleTimer);
      sample();
      delete traceWindow.__stopTranscriptScrollTrace;
    };
  });
}

async function stopTranscriptScrollTrace(
  page: Page,
): Promise<TranscriptScrollSample[]> {
  return page.evaluate(() => {
    const traceWindow = window as typeof window & {
      __transcriptScrollTrace?: TranscriptScrollSample[];
      __stopTranscriptScrollTrace?: () => void;
    };
    traceWindow.__stopTranscriptScrollTrace?.();
    const samples = traceWindow.__transcriptScrollTrace ?? [];
    delete traceWindow.__transcriptScrollTrace;
    return samples;
  });
}

function forwardScrollReversal(
  samples: readonly TranscriptScrollSample[],
): string | null {
  for (let index = 1; index < samples.length; index += 1) {
    const previous = samples[index - 1]!;
    const current = samples[index]!;
    // This fixed corpus keeps row order stable, including time rows and tool spans.
    const before = previous.index;
    const after = current.index;
    // Virtual range compensation can decrease scrollTop while the reading row stays still.
    const reversed = before !== null && after !== null
      ? after < before || (after === before && current.firstVisibleTop !== null && previous.firstVisibleTop !== null && current.firstVisibleTop > previous.firstVisibleTop + 1)
      : current.scrollTop + 1 < previous.scrollTop;
    if (reversed) {
      return `sample=${index} scroll=${previous.scrollTop}->${current.scrollTop} max=${previous.maxTop}->${current.maxTop} row=${before}:${previous.firstVisibleKey}@${previous.firstVisibleTop}->${after}:${current.firstVisibleKey}@${current.firstVisibleTop}`;
    }
  }
  return null;
}

/** Wheels to each end, failing if the first visible message runs against the wheel. */
async function transcriptWheelRoundTripStatus(
  page: Page,
  orderOf: ReadonlyMap<string, number>,
): Promise<string> {
  const order = (sample: TranscriptScrollSample) =>
    sample.firstVisibleKey === null ? undefined : orderOf.get(sample.firstVisibleKey);
  const viewport = page
    .locator(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream")
    .first();
  await viewport.hover();
  const directions = [
    { name: "up", deltaY: -2_400, atBoundary: (s: TranscriptScrollState) => s.scrollTop <= 2 },
    { name: "down", deltaY: 2_400, atBoundary: (s: TranscriptScrollState) => s.maxTop - s.scrollTop <= 2 },
  ] as const;
  for (const direction of directions) {
    let previous = await transcriptScrollSample(page);
    for (let step = 0; step < 100; step += 1) {
      if (direction.atBoundary(previous)) break;
      await page.mouse.wheel(0, direction.deltaY);
      await settleTranscriptLayout(page);
      const current = await transcriptScrollSample(page);
      const before = order(previous);
      const after = order(current);
      if (
        before !== undefined &&
        after !== undefined &&
        ((direction.name === "up" && after > before) ||
          (direction.name === "down" && after < before))
      ) {
        return `${direction.name}:order=${before}->${after} scroll=${Math.round(previous.scrollTop)}->${Math.round(current.scrollTop)} max=${Math.round(current.maxTop)}`;
      }
      previous = current;
    }
    if (!direction.atBoundary(previous)) {
      return `${direction.name}:boundary-not-reached scroll=${Math.round(previous.scrollTop)}/${Math.round(previous.maxTop)}`;
    }
  }
  return "stable";
}

async function transcriptBottomBoundaryStatus(page: Page): Promise<string> {
  await settleTranscriptLayout(page);
  return page.evaluate(() => {
    const host = document.querySelector<HTMLElement>(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream");
    const composer = host?.closest(".den-shell-stage--chat")?.querySelector<HTMLElement>(
      ".den-chat-composer-dock",
    );
    const track = host?.parentElement?.querySelector<HTMLElement>(
      ":scope > .os-scrollbar-vertical .os-scrollbar-track",
    );
    const handle = track?.querySelector<HTMLElement>(".os-scrollbar-handle");
    if (!host || !composer || !track || !handle) {
      return "unavailable";
    }
    const maxTop = Math.max(0, host.scrollHeight - host.clientHeight);
    const trackRect = track.getBoundingClientRect();
    const handleRect = handle.getBoundingClientRect();
    const composerRect = composer.getBoundingClientRect();
    const endGap = maxTop - host.scrollTop;
    const thumbGap = trackRect.bottom - handleRect.bottom;
    const composerGap = composerRect.top - trackRect.bottom;
    return endGap <= 2 && thumbGap >= -1 && thumbGap <= 4 &&
      composerGap >= -1 && composerGap <= 12
      ? "aligned"
      : `end=${endGap} thumb=${thumbGap} composer=${composerGap}`;
  });
}

async function transcriptWheelHandlingStatus(page: Page): Promise<string> {
  return page.evaluate(() => {
    const host = document.querySelector(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream");
    if (!(host instanceof HTMLElement)) return "unavailable: host";
    const event = new WheelEvent("wheel", {
      bubbles: true,
      cancelable: true,
      deltaY: -120,
      deltaMode: WheelEvent.DOM_DELTA_PIXEL,
    });
    host.dispatchEvent(event);
    return event.defaultPrevented ? "consumed" : "native";
  });
}

/** Walk scroll positions until a row with data-msg-id is mounted. */
async function revealMsgByScroll(page: Page, msgId: string): Promise<boolean> {
  await settleTranscriptLayout(page);
  const viewport = page
    .locator(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream")
    .first();
  await viewport.hover();
  // Loading history preserves the reader's row, which may lie below this target.
  await page.mouse.wheel(0, -await viewport.evaluate((el) => el.scrollHeight));
  await settleTranscriptLayout(page);
  const row = page.locator(`[data-msg-id="${msgId}"]`);
  for (let i = 0; i < 80; i += 1) {
    if ((await row.count()) > 0) return true;
    await page.mouse.wheel(0, 1_200);
    await page.waitForTimeout(100);
  }
  return (await row.count()) > 0;
}

webE2e.describe("transcript scale den", () => {
  webE2e(
    "live row growth keeps following rows in normal flow",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      const growingId = crypto.randomUUID();
      const followingId = crypto.randomUUID();
      const growingContent = Array.from(
        { length: 40 },
        (_, index) =>
          `Paragraph ${index + 1} carries enough text to wrap while the accepted response reveals into the narrow conversation column.`,
      ).join("\n\n");
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages([
          {
            id: growingId,
            role: "assistant",
            content: growingContent,
            created_at: "2026-01-01T00:00:00Z",
            seq: 1,
            ord: 1,
          },
          {
            id: followingId,
            role: "user",
            content: "This row stays below the growing response.",
            created_at: "2026-01-01T00:00:01Z",
            seq: 2,
            ord: 2,
          },
        ]),
      );

      await expect(page.locator(`[data-msg-id="${followingId}"]`)).toBeVisible({
        timeout: 10_000,
      });
      await expect(
        page.locator(`[data-msg-id="${growingId}"] .assistant-prose`),
      ).toHaveCount(1, { timeout: 10_000 });
      for (let sample = 0; sample < 20; sample += 1) {
        expect(await transcriptRowFlowStatus(page)).toBe("separated");
        await page.waitForTimeout(50);
      }
      expect(await transcriptRowFlowStatus(page)).toBe("separated");
    },
  );

  webE2e(
    "activity and diff arrivals preserve the visible transcript anchor",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      const base = Array.from({ length: 40 }, (_, index) => {
        const ord = index + 1;
        return {
          id: crypto.randomUUID(),
          role: ord % 2 === 0 ? ("user" as const) : ("assistant" as const),
          content: `anchor runway row ${ord}`,
          created_at: "2026-08-24T11:00:00Z",
          seq: ord,
          ord,
        };
      });
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages(base),
      );
      await expect(page.getByText("anchor runway row 40")).toBeVisible({
        timeout: 15_000,
      });
      const stream = liveChatStage(page).locator(".den-chat-stream");
      await stream.focus();
      await page.keyboard.press("PageUp");
      await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
      await settleTranscriptLayout(page);
      await expect
        .poll(async () => (await transcriptScrollSample(page)).firstVisibleKey, {
          timeout: 10_000,
        })
        .not.toBeNull();
      const before = await transcriptScrollSample(page);
      expect(before.firstVisibleKey).not.toBeNull();
      expect(before.scrollTop).toBeLessThan(before.maxTop);
      await startTranscriptScrollTrace(page);

      const assistantId = crypto.randomUUID();
      const toolCallId = "tc-structural-anchor";
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages([
          {
            id: assistantId,
            role: "assistant",
            content: "",
            tool_calls: [
              {
                id: toolCallId,
                name: "write",
                args: { path: "src/new-item.ts", content: "export const value = 1;\n" },
              },
            ],
            created_at: "2026-08-24T11:01:00Z",
            seq: 41,
            ord: 41,
          },
          {
            id: crypto.randomUUID(),
            role: "tool",
            content: "wrote src/new-item.ts",
            tool_result: {
              content: "wrote src/new-item.ts",
              tool: "write",
              tool_call_id: toolCallId,
              assistant_message_id: assistantId,
              file_edit: {
                path: "src/new-item.ts",
                before: "",
                after: "export const value = 1;\n",
              },
            },
            created_at: "2026-08-24T11:01:01Z",
            seq: 42,
            ord: 42,
          },
        ]),
      );

      await expect
        .poll(async () => (await transcriptScrollSample(page)).maxTop, {
          timeout: 15_000,
        })
        .not.toBe(before.maxTop);
      await settleTranscriptLayout(page);
      const after = await transcriptScrollSample(page);

      const diagnostic = JSON.stringify({ before, after, trace: await stopTranscriptScrollTrace(page) });
      expect(after.firstVisibleKey, diagnostic).toBe(before.firstVisibleKey);
      expect(after.firstVisibleTop, diagnostic).toBeCloseTo(before.firstVisibleTop ?? 0, 0);
    },
  );

  webE2e(
    "native wheel input and bidirectional scrolling remain stable during measurement",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      const messages = Array.from({ length: 160 }, (_, index) => {
        const ord = index + 1;
        const assistant = ord % 2 === 1;
        return {
          id: crypto.randomUUID(),
          role: assistant ? ("assistant" as const) : ("user" as const),
          content: assistant
            ? Array.from(
                { length: 20 },
                (_, paragraph) =>
                  `Long response ${ord}, paragraph ${paragraph + 1}, deliberately wrapping across the narrow transcript column so its first painted measurement is much taller than its estimate.`,
              ).join("\n\n")
            : `Short user row ${ord}`,
          created_at: "2026-01-01T00:00:00Z",
          seq: ord,
          ord,
        };
      });
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages(messages),
      );
      await page.reload();
      await expectShellReady(page);
      await expect(page.getByTestId("message-stream")).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.getByText("Short user row 160")).toBeVisible({
        timeout: 30_000,
      });

      expect(await transcriptWheelHandlingStatus(page)).toBe("native");
      expect(
        await transcriptWheelRoundTripStatus(
          page,
          new Map(messages.map((message) => [message.id, message.ord])),
        ),
      ).toBe("stable");
      expect(await transcriptBottomBoundaryStatus(page)).toBe("aligned");
      expect(await transcriptRowFlowStatus(page)).toBe("separated");
    },
  );

  webE2e(
    "forward wheel input stays monotonic across expanded virtual rows near the tail",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      const ambientRun = await apiGetActiveWorkflowRun(request, sessionId);
      const planRun = await apiStartWorkflowRun(request, sessionId, {
        workflow_id: "plan",
        workflow_version: "1.0.0",
        parameters: { research_depth: "none" },
      });
      const activityAssistantId = crypto.randomUUID();
      const activityToolCallId = "tc-expanded-wheel-tail";
      const messages = [
        ...Array.from({ length: 24 }, (_, index) => {
          const ord = index + 1;
          return {
            id: crypto.randomUUID(),
            role: ord % 2 === 1 ? ("assistant" as const) : ("user" as const),
            content:
              ord % 2 === 1
                ? Array.from(
                    { length: 5 },
                    (_, paragraph) =>
                      `Runway response ${ord}, paragraph ${paragraph + 1}, wraps enough to exercise variable transcript measurement.`,
                  ).join("\n\n")
                : `Runway prompt ${ord}`,
            created_at: "2026-08-24T10:00:00Z",
            workflow_run_id:
              index < 12 ? ambientRun.id : planRun.id,
            seq: ord,
            ord,
          };
        }),
        {
          id: activityAssistantId,
          role: "assistant" as const,
          content: "",
          tool_calls: [
            {
              id: activityToolCallId,
              name: "read",
              args: { path: "large-output.txt" },
            },
          ],
          created_at: "2026-08-24T10:01:00Z",
          workflow_run_id: planRun.id,
          seq: 25,
          ord: 25,
        },
        {
          id: crypto.randomUUID(),
          role: "tool" as const,
          content: Array.from(
            { length: 80 },
            (_, line) => `Expanded tool output line ${line + 1}`,
          ).join("\n"),
          tool_result: {
            content: Array.from(
              { length: 80 },
              (_, line) => `Expanded tool output line ${line + 1}`,
            ).join("\n"),
            tool: "read",
            tool_call_id: activityToolCallId,
            assistant_message_id: activityAssistantId,
          },
          created_at: "2026-08-24T10:01:01Z",
          workflow_run_id: planRun.id,
          seq: 26,
          ord: 26,
        },
        ...Array.from({ length: 6 }, (_, index) => {
          const ord = index + 27;
          return {
            id: crypto.randomUUID(),
            role: ord % 2 === 1 ? ("assistant" as const) : ("user" as const),
            content:
              ord % 2 === 1
                ? Array.from(
                    { length: 8 },
                    (_, paragraph) =>
                      `Tail response ${ord}, paragraph ${paragraph + 1}, leaves the expanded activity close to the scroll boundary.`,
                  ).join("\n\n")
                : `Tail prompt ${ord}`,
            created_at: "2026-08-24T10:02:00Z",
            workflow_run_id: planRun.id,
            seq: ord,
            ord,
          };
        }),
      ];
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages(messages),
      );

      const activity = page.getByTestId("activity-span-card").last();
      await expect(activity).toBeVisible({ timeout: 30_000 });
      await wheelStreamUp(page);
      await settleTranscriptLayout(page);
      if (!await activity.evaluate((el) => el.hasAttribute("open"))) {
        await activity.locator(":scope > summary").click();
      }
      await expect(activity).toHaveAttribute("open", "");
      await expect(activity).not.toHaveAttribute("data-animating");
      const nestedTool = activity.locator("details.den-tool-part").first();
      await expect(nestedTool).toBeVisible();
      if (!await nestedTool.evaluate((el) => el.hasAttribute("open"))) {
        await nestedTool.locator(":scope > summary").click();
      }
      await expect(nestedTool).toHaveAttribute("open", "");
      await expect(nestedTool).not.toHaveAttribute("data-animating");

      await page.evaluate(() => {
        const viewport = document.querySelector<HTMLElement>(
          ".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream",
        );
        if (!viewport) return;
        viewport.scrollTop = Math.max(
          0,
          viewport.scrollHeight - viewport.clientHeight - 1_600,
        );
        viewport.dispatchEvent(new Event("scroll"));
      });
      await settleTranscriptLayout(page);
      const expandedOffset = (await transcriptScrollSample(page)).scrollTop;
      await nestedTool.locator(":scope > summary").evaluate((summary) => {
        (summary as HTMLElement).click();
      });
      await settleTranscriptLayout(page);
      expect((await transcriptScrollSample(page)).scrollTop).toBeCloseTo(
        expandedOffset,
        0,
      );
      await expect(nestedTool).not.toHaveAttribute("open", "");

      await nestedTool.locator(":scope > summary").evaluate((summary) => {
        (summary as HTMLElement).click();
      });
      await settleTranscriptLayout(page);
      expect((await transcriptScrollSample(page)).scrollTop).toBeCloseTo(
        expandedOffset,
        0,
      );
      await expect(nestedTool).toHaveAttribute("open", "");

      const viewport = page
        .locator(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream")
        .first();
      await viewport.hover();
      await startTranscriptScrollTrace(page);
      for (let index = 0; index < 40; index += 1) {
        await page.mouse.wheel(0, 120);
        if (index === 10 || index === 22) {
          await nestedTool.locator(":scope > summary").evaluate((summary) => {
            (summary as HTMLElement).click();
          });
        }
        await page.waitForTimeout(16);
      }
      await page.waitForTimeout(800);
      const samples = await stopTranscriptScrollTrace(page);

      expect(samples.length).toBeGreaterThan(5);
      expect(
        forwardScrollReversal(samples),
        JSON.stringify(samples),
      ).toBeNull();
      expect(await transcriptBottomBoundaryStatus(page)).toBe("aligned");
      expect(await nestedTool).toHaveAttribute("open", "");
      expect(await activity).toHaveAttribute("open", "");
    },
  );

  webE2e(
    "older history lands above the reading row without moving it",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages(synthMessages(260)),
      );
      const isOlderPage = (url: URL) =>
        url.pathname.endsWith("/messages") && url.searchParams.has("before_message_id");
      let olderRequested = false;
      let releaseOlder: (() => void) | undefined;
      await page.route(isOlderPage, async (route) => {
        olderRequested = true;
        await new Promise<void>((resolve) => {
          releaseOlder = resolve;
        });
        await route.continue();
      });
      await page.reload();
      await expectShellReady(page);
      await expect(page.getByText("scale row 260").first()).toBeVisible({
        timeout: 30_000,
      });
      await settleTranscriptLayout(page);

      // Fractional deltas take the trackpad path.
      await page.locator(".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream").first().hover();
      for (let step = 0; step < 200 && !olderRequested; step += 1) {
        await page.mouse.wheel(0, -237.5);
        await settleTranscriptLayout(page);
      }
      expect(olderRequested).toBe(true);
      await settleTranscriptLayout(page);
      const before = await transcriptScrollSample(page);
      // Older history is requested before the reader reaches the top.
      expect(before.scrollTop).toBeGreaterThan(0);
      expect(before.firstVisibleKey).not.toBeNull();

      const loaded = page.waitForResponse((response) =>
        isOlderPage(new URL(response.url())),
      );
      releaseOlder?.();
      await loaded;
      await page.unroute(isOlderPage);
      await expect
        .poll(async () => (await transcriptScrollSample(page)).scrollTop, {
          timeout: 10_000,
        })
        .toBeGreaterThan(before.scrollTop);
      await settleTranscriptLayout(page);
      const after = await transcriptScrollSample(page);

      expect(after.firstVisibleKey).toBe(before.firstVisibleKey);
      expect(after.firstVisibleTop).toBeCloseTo(before.firstVisibleTop ?? 0, 0);

      await page.mouse.wheel(0, -237.5);
      await settleTranscriptLayout(page);
      await expect.poll(async () => {
        const current = await transcriptScrollSample(page);
        return current.index !== null && after.index !== null &&
          (current.index < after.index ||
            (current.firstVisibleKey === after.firstVisibleKey &&
              current.firstVisibleTop !== null && after.firstVisibleTop !== null &&
              current.firstVisibleTop > after.firstVisibleTop));
      }).toBe(true);
    },
  );

  webE2e(
    "tail-only open, load-more, reveal, sticky append stay DOM-bounded",
    async ({ page, request }) => {
      let all = synthMessages(SESSION_LEN);
      const loadMoreRequests: string[] = [];
      page.on("request", (req) => {
        const url = new URL(req.url());
        if (!url.pathname.endsWith("/messages")) return;
        const before = url.searchParams.get("before_message_id");
        if (before) loadMoreRequests.push(before);
      });

      await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      expect(sessionId).toBeTruthy();
      await apiSeedSessionTranscript(request, sessionId, transcriptMessages(all));
      await page.reload();
      await expectShellReady(page);

      await expect(page.getByTestId("message-stream")).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.getByText(`scale row ${SESSION_LEN}`).first()).toBeVisible();
      expect(await page.getByText("scale row 1").count()).toBe(0);

      const initialMounted = await mountedRowCount(page);
      expect(initialMounted).toBeGreaterThan(0);
      expect(initialMounted).toBeLessThanOrEqual(MAX_MOUNTED_TRANSCRIPT_ROWS);
      expect(await scrollSurfaceContract(page)).toEqual({
        activitySettles: true,
        customVerticalScrollbar: true,
        gutter: "auto",
        scrollbarWidth: "none",
      });

      await scrollStreamToMiddle(page);
      await expect
        .poll(() => transcriptRunwayStatus(page), {
          timeout: 5_000,
        })
        .toBe("covered");
      expect(await mountedRowCount(page)).toBeLessThanOrEqual(
        MAX_MOUNTED_TRANSCRIPT_ROWS,
      );

      await expect
        .poll(
          async () => {
            await wheelStreamUp(page);
            return loadMoreRequests.length;
          },
          { timeout: 15_000 },
        )
        .toBeGreaterThan(0);

      const afterLoadMounted = await mountedRowCount(page);
      expect(afterLoadMounted).toBeLessThanOrEqual(
        MAX_MOUNTED_TRANSCRIPT_ROWS,
      );

      // Scrolling mounts a row from newly loaded history.
      const revealId = all[349]!.id;
      const found = await revealMsgByScroll(page, revealId);
      expect(found).toBe(true);
      await expect(page.locator(`[data-msg-id="${revealId}"]`)).toHaveCount(1, {
        timeout: 5_000,
      });
      expect(await mountedRowCount(page)).toBeLessThanOrEqual(
        MAX_MOUNTED_TRANSCRIPT_ROWS,
      );

      // New tail turn after reload — sticky window stays bounded.
      const appended: Msg = {
        id: crypto.randomUUID(),
        role: "assistant",
        content: `scale row ${SESSION_LEN + 1}`,
        created_at: "2026-01-01T00:01:00Z",
        seq: SESSION_LEN + 1,
        ord: SESSION_LEN + 1,
      };
      all = [...all, appended];
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages([appended]),
      );
      await page.reload();
      await expect(page.getByTestId("message-stream")).toBeVisible({
        timeout: 60_000,
      });
      await expect(
        page.getByText(`scale row ${SESSION_LEN + 1}`).first(),
      ).toBeVisible({ timeout: 15_000 });
      expect(await mountedRowCount(page)).toBeLessThanOrEqual(
        MAX_MOUNTED_TRANSCRIPT_ROWS,
      );
    },
  );
});
