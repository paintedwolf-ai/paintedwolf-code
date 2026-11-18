import { expect, type Locator, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e as test,
  settledChatSessionId,
  transcriptMessages,
} from "../helpers.ts";
import { scrollSettled } from "../fixtures/settle.ts";

type Box = { top: number; right: number; bottom: number; left: number; width: number; height: number };

function boxesOverlap(a: Box, b: Box): boolean {
  const eps = 0.5;
  return a.left < b.right - eps && a.right > b.left + eps && a.top < b.bottom - eps && a.bottom > b.top + eps;
}

async function setMessageTimes(page: Page, value: "hover" | "always") {
  await page.evaluate(async (next) => {
    const url = "/src/settings/chat/chat-prefs.ts";
    const { saveMessageTimes } = await import(/* @vite-ignore */ url);
    await saveMessageTimes(next);
  }, value);
}

async function setTextScale(page: Page, scale: number) {
  await page.evaluate((next) => {
    (window as unknown as { __harness: { setTextScale: (value: number) => void } }).__harness.setTextScale(next);
  }, scale);
}

async function rectOf(locator: Locator): Promise<Box> {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { top: r.top, right: r.right, bottom: r.bottom, left: r.left, width: r.width, height: r.height };
  });
}

async function sampleRowScroll(stream: Locator, msgId: string) {
  return stream.evaluate((host, id) => ({
    scrollTop: host.scrollTop,
    scrollHeight: host.scrollHeight,
    height: host.querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${id}"]`)!.getBoundingClientRect().height,
  }), msgId);
}

async function waitToolbarVisible(row: Locator) {
  await expect.poll(() => row.getByTestId("message-actions").evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
}

test.describe("message actions layout", () => {
  test.afterEach(async ({ page }) => {
    if (!page.isClosed() && page.url().startsWith("http")) {
      await setMessageTimes(page, "hover");
      await setTextScale(page, 1);
    }
  });

  test("keeps user and assistant action toolbars clear of sibling chrome, adjacent rows, and the composer", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const [u1, a1, u2, a2] = [crypto.randomUUID(), crypto.randomUUID(), crypto.randomUUID(), crypto.randomUUID()];

    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages([
        {
          id: u1,
          role: "user",
          content: "Inspect the entrypoint and summarize what is in view.",
          created_at: "2026-07-23T20:00:00Z",
          seq: 1,
          ord: 1,
          source_context: {
            locations: [],
            truncated: false,
            view: {
              editors: [{ root_id: "root-1", path: "cmd/fixture/main.go", display_path: "cmd/fixture/main.go", unsaved: false }],
              diffs: [],
              findings: [],
            },
          },
        },
        {
          id: a1,
          role: "assistant",
          content: "The entrypoint lives in `cmd/fixture/main.go`.",
          status: "complete",
          created_at: "2026-07-23T20:00:05Z",
          seq: 2,
          ord: 2,
          grounding: {
            traced: true,
            cited_evidence: [{ handle: "read#1", path: "cmd/fixture/main.go", line: 9, excerpt: "func main()", verdict: "matched" }],
          },
        },
        { id: u2, role: "user", content: "Confirm the final line.", created_at: "2026-07-23T20:01:00Z", seq: 3, ord: 3 },
        { id: a2, role: "assistant", content: "Confirmed at the bottom of the stream.", status: "complete", created_at: "2026-07-23T20:01:04Z", seq: 4, ord: 4 },
      ]),
    );

    const stage = liveChatStage(page);
    const u1Row = stage.locator(`.transcript-viewport-row[data-msg-id="${u1}"]`);
    const a1Row = stage.locator(`.transcript-viewport-row[data-msg-id="${a1}"]`);
    const u2Row = stage.locator(`.transcript-viewport-row[data-msg-id="${u2}"]`);
    const a2Row = stage.locator(`.transcript-viewport-row[data-msg-id="${a2}"]`);

    await expect(u1Row.getByTestId("transcript-view-context")).toBeVisible({ timeout: 30_000 });
    await expect(a1Row.getByTestId("citation-evidence-chicklet")).toBeVisible({ timeout: 30_000 });
    await expect(a2Row.getByTestId("transcript-article-assistant")).toBeVisible({ timeout: 30_000 });

    // 1. User row in "hover" mode: toolbar clears bubble, ViewContextLine, and following row.
    await u1Row.getByTestId("transcript-article-user").hover();
    const u1Toolbar = u1Row.getByTestId("message-actions");
    await waitToolbarVisible(u1Row);
    await expect(u1Toolbar).toHaveAttribute("data-align", "end");
    const [u1BubbleBox, u1ViewBox, u1ToolbarBox, a1RowBox] = await Promise.all([
      rectOf(u1Row.getByTestId("transcript-article-user")),
      rectOf(u1Row.getByTestId("transcript-view-context")),
      rectOf(u1Toolbar),
      rectOf(a1Row),
    ]);
    expect(boxesOverlap(u1ToolbarBox, u1BubbleBox)).toBe(false);
    expect(boxesOverlap(u1ToolbarBox, u1ViewBox)).toBe(false);
    expect(u1ToolbarBox.top).toBeGreaterThanOrEqual(u1ViewBox.bottom - 0.5);
    expect(boxesOverlap(u1ToolbarBox, a1RowBox)).toBe(false);

    // 2. User row in "always" mode: toolbar sits beside below-time without covering it.
    await setMessageTimes(page, "always");
    const u1BelowTime = u1Row.locator(".den-msg-time-below");
    await expect(u1BelowTime).toBeVisible();
    await u1Row.getByTestId("transcript-article-user").hover();
    await waitToolbarVisible(u1Row);
    const [u1ToolbarAlwaysBox, u1BelowTimeBox, u1ViewAlwaysBox] = await Promise.all([
      rectOf(u1Toolbar),
      rectOf(u1BelowTime),
      rectOf(u1Row.getByTestId("transcript-view-context")),
    ]);
    expect(boxesOverlap(u1ToolbarAlwaysBox, u1BelowTimeBox)).toBe(false);
    expect(boxesOverlap(u1ToolbarAlwaysBox, u1ViewAlwaysBox)).toBe(false);

    // 3. Assistant row: toolbar clears prose, CitationEvidenceChicklet, and next row; chicklet stays clickable.
    await a1Row.locator(".bubble--assistant").hover();
    const a1Toolbar = a1Row.getByTestId("message-actions");
    const a1Chicklet = a1Row.getByTestId("citation-evidence-chicklet");
    await waitToolbarVisible(a1Row);
    await expect(a1Toolbar).toHaveAttribute("data-align", "start");
    const [a1BubbleBox, a1ChickletBox, a1ToolbarBox, afterA1RowBox, u2RowBox] = await Promise.all([
      rectOf(a1Row.locator(".bubble--assistant")),
      rectOf(a1Chicklet),
      rectOf(a1Toolbar),
      rectOf(a1Row.locator("xpath=following-sibling::*[1]")),
      rectOf(u2Row),
    ]);
    expect(boxesOverlap(a1ToolbarBox, a1BubbleBox)).toBe(false);
    expect(boxesOverlap(a1ToolbarBox, a1ChickletBox)).toBe(false);
    expect(a1ToolbarBox.top).toBeGreaterThanOrEqual(a1ChickletBox.bottom - 0.5);
    expect(boxesOverlap(a1ToolbarBox, afterA1RowBox)).toBe(false);
    expect(boxesOverlap(a1ToolbarBox, u2RowBox)).toBe(false);

    await a1Chicklet.locator(".den-transcript-disclosure-header").click();
    await expect(page.getByTestId("citation-evidence-chicklet-panel")).toBeVisible();

    // 4. Final message above the composer: toolbar stays inside the stream and above the composer.
    const stream = stage.locator(".den-chat-stream");
    await stream.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await a2Row.locator(".bubble--assistant").hover();
    await waitToolbarVisible(a2Row);
    const [a2ToolbarBox, streamBox, composerBox] = await Promise.all([
      rectOf(a2Row.getByTestId("message-actions")),
      rectOf(stream),
      rectOf(stage.locator(".den-composer")),
    ]);
    expect(a2ToolbarBox.bottom).toBeLessThanOrEqual(streamBox.bottom + 0.5);
    expect(a2ToolbarBox.bottom).toBeLessThanOrEqual(composerBox.top + 0.5);
  });

  test("matches unmounted row-height estimates and preserves scroll and layout at narrow widths", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const paragraph = "A steady paragraph for virtualized scroll verification. ".repeat(3);

    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages(
        Array.from({ length: 120 }, (_, index) => ({
          id: crypto.randomUUID(),
          role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
          content: `Turn ${index}: ${paragraph}`,
          status: index % 2 === 1 ? ("complete" as const) : undefined,
          created_at: new Date(Date.parse("2026-07-23T18:00:00Z") + index * 30_000).toISOString(),
          seq: index + 1,
          ord: index + 1,
        })),
      ),
    );

    const stage = liveChatStage(page);
    const stream = stage.locator(".den-chat-stream");
    await expect(stream.locator(".transcript-viewport-row").getByText("Turn 119:", { exact: false })).toBeAttached({
      timeout: 30_000,
    });
    await stream.focus();
    await page.keyboard.press("End");
    await stream.evaluate(async () => {
      for (let frame = 0; frame < 4; frame += 1) await new Promise(requestAnimationFrame);
    });

    // 1. Unmounted-row estimate accuracy: before scrolling up, rows 0..anchorIndex-1 have never
    // mounted, so anchor's data-virtual-start is purely the sum of their unmounted estimates.
    const initialTailAnchor = await stream.evaluate((host) => {
      const firstMounted = host.querySelector<HTMLElement>(".transcript-viewport-row[data-msg-id][data-index]")!;
      return {
        msgId: firstMounted.dataset.msgId!,
        index: Number(firstMounted.dataset.index),
        estimatedStart: Number(firstMounted.dataset.virtualStart),
      };
    });
    expect(initialTailAnchor.index).toBeGreaterThanOrEqual(60);

    // Sweep from top to bottom so every previously unmounted row mounts and is measured.
    const measuredTailStart = await stream.evaluate(async (host, anchorId) => {
      host.scrollTop = 0;
      for (let frame = 0; frame < 3; frame += 1) await new Promise(requestAnimationFrame);
      const step = Math.max(200, Math.floor(host.clientHeight * 0.65));
      while (host.scrollTop + host.clientHeight < host.scrollHeight - 4) {
        host.scrollTop = Math.min(host.scrollHeight, host.scrollTop + step);
        for (let frame = 0; frame < 2; frame += 1) await new Promise(requestAnimationFrame);
      }
      host.scrollTop = host.scrollHeight;
      for (let frame = 0; frame < 4; frame += 1) await new Promise(requestAnimationFrame);
      return Number(host.querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${anchorId}"]`)!.dataset.virtualStart);
    }, initialTailAnchor.msgId);

    // Omitting the reserved ~29px action strip from estimates would drift by ~29px per row.
    const meanMissPerUnmountedRow = Math.abs(measuredTailStart - initialTailAnchor.estimatedStart) / initialTailAnchor.index;
    expect(meanMissPerUnmountedRow).toBeLessThan(16);

    // 2. Disengage live tail-follow with PageUp so mid-stream reading position stays anchored.
    await stream.press("PageUp");
    await scrollSettled(stream);
    await stream.evaluate(async (host) => {
      host.scrollTop = Math.floor(host.scrollHeight * 0.45);
      for (let frame = 0; frame < 4; frame += 1) await new Promise(requestAnimationFrame);
    });

    const baseline = await stream.evaluate(async (host) => {
      const estimateModUrl = "/src/chat/transcript/layout/transcript-row-content-estimate.ts";
      const spacingModUrl = "/src/chat/transcript/layout/transcript-spacing.ts";
      const { transcriptRowContentEstimate } = (await import(
        /* @vite-ignore */ estimateModUrl
      )) as typeof import("../../src/chat/transcript/layout/transcript-row-content-estimate.ts");
      const { transcriptRootRemPx, transcriptSpacing } = (await import(
        /* @vite-ignore */ spacingModUrl
      )) as typeof import("../../src/chat/transcript/layout/transcript-spacing.ts");

      const hostRect = host.getBoundingClientRect();
      const rows = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")];
      const inView = rows.filter((row) => {
        const r = row.getBoundingClientRect();
        return r.top >= hostRect.top + 8 && r.bottom <= hostRect.bottom - 8;
      });
      const userRow = inView.find((row) => row.querySelector(".user-turn"))!;
      const assistantRow = inView.find((row) => row.querySelector(".assistant-turn"))!;

      const remPx = transcriptRootRemPx();
      const spacing = transcriptSpacing();
      const bodySample = assistantRow.querySelector<HTMLElement>(".bubble--assistant") ?? assistantRow;
      const bodyPx = Number.parseFloat(getComputedStyle(bodySample).fontSize);
      const metrics = { widthPx: userRow.getBoundingClientRect().width, remPx, bodyPx, spacing };
      const actionChromePx = 4 + 1.5 * remPx + spacing.messageTimeGap * remPx;
      const userText = userRow.querySelector(".bubble--user")?.textContent ?? "";
      const assistantText = assistantRow.querySelector(".bubble--assistant")?.textContent ?? "";

      const userEstimated =
        transcriptRowContentEstimate({ kind: "user", key: userRow.dataset.msgId!, text: userText }, metrics, [])! + actionChromePx;
      const assistantEstimated =
        transcriptRowContentEstimate({ kind: "assistant", key: assistantRow.dataset.msgId!, text: assistantText }, metrics, [])! +
        actionChromePx;
      const contentHeight = (row: HTMLElement) =>
        row.getBoundingClientRect().height - Number.parseFloat(getComputedStyle(row).paddingTop);

      return {
        mountedCount: rows.length,
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        userId: userRow.dataset.msgId!,
        userHeight: userRow.getBoundingClientRect().height,
        userEstimateMiss: Math.abs(contentHeight(userRow) - userEstimated),
        assistantId: assistantRow.dataset.msgId!,
        assistantHeight: assistantRow.getBoundingClientRect().height,
        assistantEstimateMiss: Math.abs(contentHeight(assistantRow) - assistantEstimated),
      };
    });

    expect(baseline.mountedCount).toBeLessThan(30);
    expect(baseline.userEstimateMiss).toBeLessThan(24);
    expect(baseline.assistantEstimateMiss).toBeLessThan(24);

    const userRow = stream.locator(`.transcript-viewport-row[data-msg-id="${baseline.userId}"]`);
    const assistantRow = stream.locator(`.transcript-viewport-row[data-msg-id="${baseline.assistantId}"]`);

    // Hovering user and assistant toolbars must not alter row height or scroll offset.
    await userRow.getByTestId("transcript-article-user").hover();
    await waitToolbarVisible(userRow);
    const afterUserHover = await sampleRowScroll(stream, baseline.userId);
    expect(afterUserHover.height).toBeCloseTo(baseline.userHeight, 2);
    expect(afterUserHover.scrollTop).toBe(baseline.scrollTop);
    expect(afterUserHover.scrollHeight).toBe(baseline.scrollHeight);

    await assistantRow.locator(".bubble--assistant").hover();
    await waitToolbarVisible(assistantRow);
    const afterAssistantHover = await sampleRowScroll(stream, baseline.assistantId);
    expect(afterAssistantHover.height).toBeCloseTo(baseline.assistantHeight, 2);
    expect(afterAssistantHover.scrollTop).toBe(baseline.scrollTop);
    expect(afterAssistantHover.scrollHeight).toBe(baseline.scrollHeight);

    // Switching message times to "always" keeps the single-line user footer height unchanged.
    await setMessageTimes(page, "always");
    await expect(userRow.locator(".den-msg-time-below")).toBeVisible();
    const afterAlwaysToggle = await sampleRowScroll(stream, baseline.userId);
    expect(afterAlwaysToggle.height).toBeCloseTo(baseline.userHeight, 2);
    expect(afterAlwaysToggle.scrollTop).toBe(baseline.scrollTop);
    await setMessageTimes(page, "hover");

    // 3. Narrow column (CHAT_COL_MIN = 328px) under max text scale (53/17): timestamp truncates
    // with ellipsis instead of overflowing horizontally, clipping buttons, or resizing on hover.
    await setTextScale(page, 53 / 17);
    const narrowBeforeHover = await stream.evaluate(async (host) => {
      const inner = host.querySelector<HTMLElement>(".den-chat-stream-inner")!;
      inner.style.width = "328px";
      for (let frame = 0; frame < 6; frame += 1) await new Promise(requestAnimationFrame);
      const hostRect = host.getBoundingClientRect();
      const remPx = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
      const rows = [...inner.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")];
      const uRow =
        rows.find((r) => r.querySelector(".user-turn") && r.getBoundingClientRect().bottom > hostRect.top + 48) ??
        rows.find((r) => r.querySelector(".user-turn"))!;
      host.scrollTop += uRow.getBoundingClientRect().top - hostRect.top - 12;
      for (let frame = 0; frame < 3; frame += 1) await new Promise(requestAnimationFrame);
      const aRow = inner.querySelector<HTMLElement>(".transcript-viewport-row:has(.assistant-turn)")!;
      const uToolbar = uRow.querySelector<HTMLElement>('[data-testid="message-actions"]')!;
      const aToolbar = aRow.querySelector<HTMLElement>('[data-testid="message-actions"]')!;
      const uTime = uToolbar.querySelector<HTMLElement>(".den-msg-actions__time")!;
      const inspectToolbar = (row: HTMLElement, toolbar: HTMLElement) => {
        const rowRect = row.getBoundingClientRect();
        const barRect = toolbar.getBoundingClientRect();
        const btns = [...toolbar.querySelectorAll<HTMLElement>("button")].map((btn) => btn.getBoundingClientRect());
        const firstTop = btns[0]?.top ?? 0;
        return {
          withinRowHorizontally: barRect.left >= rowRect.left - 0.5 && barRect.right <= rowRect.right + 0.5,
          minBtnWidth: Math.min(...btns.map((b) => b.width)),
          allBtnsUnclipped: btns.every(
            (b) =>
              b.left >= barRect.left - 0.5 &&
              b.right <= barRect.right + 0.5 &&
              b.top >= barRect.top - 0.5 &&
              b.bottom <= barRect.bottom + 0.5 &&
              Math.abs(b.top - firstTop) < 1,
          ),
        };
      };
      const bubbleRect = uRow.querySelector<HTMLElement>('[data-testid="transcript-article-user"]')!.getBoundingClientRect();
      return {
        remPx,
        innerOverflowPx: inner.scrollWidth - inner.clientWidth,
        userId: uRow.dataset.msgId!,
        userHeightAtRest: uRow.getBoundingClientRect().height,
        hoverX: bubbleRect.left + Math.min(40, bubbleRect.width / 2),
        hoverY: Math.max(hostRect.top + 32, bubbleRect.top + 32),
        userTimeTruncated: uTime.clientWidth < uTime.scrollWidth,
        userBar: inspectToolbar(uRow, uToolbar),
        assistantBar: inspectToolbar(aRow, aToolbar),
      };
    });

    expect(narrowBeforeHover.innerOverflowPx).toBeLessThanOrEqual(1);
    expect(narrowBeforeHover.userBar.withinRowHorizontally).toBe(true);
    expect(narrowBeforeHover.userBar.allBtnsUnclipped).toBe(true);
    expect(narrowBeforeHover.userBar.minBtnWidth).toBeGreaterThanOrEqual(1.5 * narrowBeforeHover.remPx);
    expect(narrowBeforeHover.userTimeTruncated).toBe(true);
    expect(narrowBeforeHover.assistantBar.withinRowHorizontally).toBe(true);
    expect(narrowBeforeHover.assistantBar.allBtnsUnclipped).toBe(true);

    const narrowUserRow = stream.locator(`.transcript-viewport-row[data-msg-id="${narrowBeforeHover.userId}"]`);
    await page.mouse.move(narrowBeforeHover.hoverX, narrowBeforeHover.hoverY);
    await waitToolbarVisible(narrowUserRow);
    const narrowHeightHovered = await narrowUserRow.evaluate((el) => el.getBoundingClientRect().height);
    expect(narrowHeightHovered).toBeCloseTo(narrowBeforeHover.userHeightAtRest, 2);

    await stream.evaluate((host) => {
      host.querySelector<HTMLElement>(".den-chat-stream-inner")!.style.width = "";
    });
  });
});
