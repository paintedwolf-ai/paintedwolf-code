import { expect, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e as test,
  settledChatSessionId,
  transcriptMessages,
} from "../helpers.ts";

type Box = { top: number; right: number; bottom: number; left: number; width: number; height: number };

function boxesOverlap(a: Box, b: Box): boolean {
  const eps = 0.5;
  return (
    a.left < b.right - eps &&
    a.right > b.left + eps &&
    a.top < b.bottom - eps &&
    a.bottom > b.top + eps
  );
}

async function setMessageTimes(page: Page, value: "hover" | "always") {
  await page.evaluate(async (next) => {
    const url = "/src/settings/chat/chat-prefs.ts";
    const { saveMessageTimes } = await import(/* @vite-ignore */ url);
    await saveMessageTimes(next);
  }, value);
}

async function rectOf(locator: ReturnType<Page["locator"]>): Promise<Box> {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { top: r.top, right: r.right, bottom: r.bottom, left: r.left, width: r.width, height: r.height };
  });
}

test.describe("message actions layout", () => {
  test.afterEach(async ({ page }) => {
    if (!page.isClosed()) {
      await setMessageTimes(page, "hover").catch(() => {});
    }
  });

  test("keeps user and assistant action toolbars clear of sibling chrome, adjacent rows, and the composer", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const u1 = crypto.randomUUID();
    const a1 = crypto.randomUUID();
    const u2 = crypto.randomUUID();
    const a2 = crypto.randomUUID();

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
              editors: [
                {
                  root_id: "root-1",
                  path: "cmd/fixture/main.go",
                  display_path: "cmd/fixture/main.go",
                  unsaved: false,
                },
              ],
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
            cited_evidence: [
              {
                handle: "read#1",
                path: "cmd/fixture/main.go",
                line: 9,
                excerpt: "func main()",
                verdict: "matched",
              },
            ],
          },
        },
        {
          id: u2,
          role: "user",
          content: "Confirm the final line.",
          created_at: "2026-07-23T20:01:00Z",
          seq: 3,
          ord: 3,
        },
        {
          id: a2,
          role: "assistant",
          content: "Confirmed at the bottom of the stream.",
          status: "complete",
          created_at: "2026-07-23T20:01:04Z",
          seq: 4,
          ord: 4,
        },
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
    await expect.poll(() => u1Toolbar.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
    await expect(u1Toolbar).toHaveAttribute("data-align", "end");
    const u1BubbleBox = await rectOf(u1Row.getByTestId("transcript-article-user"));
    const u1ViewBox = await rectOf(u1Row.getByTestId("transcript-view-context"));
    const u1ToolbarBox = await rectOf(u1Toolbar);
    const a1RowBox = await rectOf(a1Row);
    expect(boxesOverlap(u1ToolbarBox, u1BubbleBox)).toBe(false);
    expect(boxesOverlap(u1ToolbarBox, u1ViewBox)).toBe(false);
    expect(u1ToolbarBox.top).toBeGreaterThanOrEqual(u1ViewBox.bottom - 0.5);
    expect(boxesOverlap(u1ToolbarBox, a1RowBox)).toBe(false);

    // 2. User row in "always" mode: toolbar sits beside below-time without covering it.
    await setMessageTimes(page, "always");
    const u1BelowTime = u1Row.locator(".den-msg-time-below");
    await expect(u1BelowTime).toBeVisible();
    await u1Row.getByTestId("transcript-article-user").hover();
    await expect.poll(() => u1Toolbar.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
    const u1ToolbarAlwaysBox = await rectOf(u1Toolbar);
    const u1BelowTimeBox = await rectOf(u1BelowTime);
    expect(boxesOverlap(u1ToolbarAlwaysBox, u1BelowTimeBox)).toBe(false);
    expect(boxesOverlap(u1ToolbarAlwaysBox, await rectOf(u1Row.getByTestId("transcript-view-context")))).toBe(false);

    // 3. Assistant row: toolbar clears prose, CitationEvidenceChicklet, and next row; chicklet stays clickable.
    await a1Row.locator(".bubble--assistant").hover();
    const a1Toolbar = a1Row.getByTestId("message-actions");
    const a1Chicklet = a1Row.getByTestId("citation-evidence-chicklet");
    await expect.poll(() => a1Toolbar.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
    await expect(a1Toolbar).toHaveAttribute("data-align", "start");
    const a1BubbleBox = await rectOf(a1Row.locator(".bubble--assistant"));
    const a1ChickletBox = await rectOf(a1Chicklet);
    const a1ToolbarBox = await rectOf(a1Toolbar);
    const afterA1RowBox = await rectOf(a1Row.locator("xpath=following-sibling::*[1]"));
    expect(boxesOverlap(a1ToolbarBox, a1BubbleBox)).toBe(false);
    expect(boxesOverlap(a1ToolbarBox, a1ChickletBox)).toBe(false);
    expect(a1ToolbarBox.top).toBeGreaterThanOrEqual(a1ChickletBox.bottom - 0.5);
    expect(boxesOverlap(a1ToolbarBox, afterA1RowBox)).toBe(false);
    expect(boxesOverlap(a1ToolbarBox, await rectOf(u2Row))).toBe(false);

    await a1Chicklet.locator(".den-transcript-disclosure-header").click();
    await expect(page.getByTestId("citation-evidence-chicklet-panel")).toBeVisible();

    // 4. Final message above the composer: toolbar stays inside the stream and above the composer.
    const stream = stage.locator(".den-chat-stream");
    await stream.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await a2Row.locator(".bubble--assistant").hover();
    const a2Toolbar = a2Row.getByTestId("message-actions");
    await expect.poll(() => a2Toolbar.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
    const a2ToolbarBox = await rectOf(a2Toolbar);
    const streamBox = await rectOf(stream);
    const composerBox = await rectOf(stage.locator(".den-composer"));
    expect(a2ToolbarBox.bottom).toBeLessThanOrEqual(streamBox.bottom + 0.5);
    expect(a2ToolbarBox.bottom).toBeLessThanOrEqual(composerBox.top + 0.5);
  });

  test("preserves virtualized row heights and scroll position across hover, time-mode toggles, and narrow columns", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);

    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages(
        Array.from({ length: 120 }, (_, index) => ({
          id: crypto.randomUUID(),
          role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
          content: `Turn ${index}: ${"A steady paragraph for virtualized scroll verification. ".repeat(5)}`,
          status: index % 2 === 1 ? ("complete" as const) : undefined,
          created_at: new Date(Date.parse("2026-07-23T18:00:00Z") + index * 30_000).toISOString(),
          seq: index + 1,
          ord: index + 1,
        })),
      ),
    );

    const stage = liveChatStage(page);
    const stream = stage.locator(".den-chat-stream");
    await expect(
      stream.locator(".transcript-viewport-row").getByText("Turn 119:", { exact: false }),
    ).toBeAttached({ timeout: 30_000 });

    await stream.evaluate(async (host) => {
      host.scrollTop = Math.floor(host.scrollHeight * 0.45);
      for (let frame = 0; frame < 4; frame += 1) {
        await new Promise(requestAnimationFrame);
      }
    });

    const baseline = await stream.evaluate((host) => {
      const hostRect = host.getBoundingClientRect();
      const rows = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")];
      const inView = rows.filter((row) => {
        const r = row.getBoundingClientRect();
        return r.top >= hostRect.top + 8 && r.bottom <= hostRect.bottom - 8;
      });
      const userRow = inView.find((row) => row.querySelector(".user-turn"));
      const assistantRow = inView.find((row) => row.querySelector(".assistant-turn"));
      return {
        mountedCount: rows.length,
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        userId: userRow?.dataset.msgId ?? null,
        userHeight: userRow?.getBoundingClientRect().height ?? 0,
        assistantId: assistantRow?.dataset.msgId ?? null,
        assistantHeight: assistantRow?.getBoundingClientRect().height ?? 0,
      };
    });

    expect(baseline.mountedCount).toBeLessThan(30);
    expect(baseline.userId).not.toBeNull();
    expect(baseline.assistantId).not.toBeNull();

    const userRow = stream.locator(`.transcript-viewport-row[data-msg-id="${baseline.userId}"]`);
    const assistantRow = stream.locator(
      `.transcript-viewport-row[data-msg-id="${baseline.assistantId}"]`,
    );

    // Hovering user and assistant toolbars must not alter row height or scroll offset.
    await userRow.getByTestId("transcript-article-user").hover();
    await expect
      .poll(() => userRow.getByTestId("message-actions").evaluate((el) => getComputedStyle(el).opacity))
      .toBe("1");
    const afterUserHover = await stream.evaluate(
      (host, id) => ({
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        height: host
          .querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${id}"]`)!
          .getBoundingClientRect().height,
      }),
      baseline.userId,
    );
    expect(afterUserHover.height).toBeCloseTo(baseline.userHeight, 2);
    expect(afterUserHover.scrollTop).toBe(baseline.scrollTop);
    expect(afterUserHover.scrollHeight).toBe(baseline.scrollHeight);

    await assistantRow.locator(".bubble--assistant").hover();
    await expect
      .poll(() =>
        assistantRow.getByTestId("message-actions").evaluate((el) => getComputedStyle(el).opacity),
      )
      .toBe("1");
    const afterAssistantHover = await stream.evaluate(
      (host, id) => ({
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        height: host
          .querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${id}"]`)!
          .getBoundingClientRect().height,
      }),
      baseline.assistantId,
    );
    expect(afterAssistantHover.height).toBeCloseTo(baseline.assistantHeight, 2);
    expect(afterAssistantHover.scrollTop).toBe(baseline.scrollTop);
    expect(afterAssistantHover.scrollHeight).toBe(baseline.scrollHeight);

    // Switching message times to "always" keeps the single-line user footer height unchanged.
    await setMessageTimes(page, "always");
    await expect(userRow.locator(".den-msg-time-below")).toBeVisible();
    const afterAlwaysToggle = await stream.evaluate(
      (host, id) => ({
        scrollTop: host.scrollTop,
        height: host
          .querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${id}"]`)!
          .getBoundingClientRect().height,
      }),
      baseline.userId,
    );
    expect(afterAlwaysToggle.height).toBeCloseTo(baseline.userHeight, 2);
    expect(afterAlwaysToggle.scrollTop).toBe(baseline.scrollTop);

    // Narrowing to CHAT_COL_MIN (328px) keeps every mounted action strip on one fixed-height line.
    const narrowHeights = await stream.evaluate(async (host) => {
      const inner = host.querySelector<HTMLElement>(".den-chat-stream-inner")!;
      inner.style.width = "328px";
      for (let frame = 0; frame < 3; frame += 1) {
        await new Promise(requestAnimationFrame);
      }
      const remPx = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
      const expected = 1.5 * remPx + 4;
      const deltas = [
        ...inner.querySelectorAll<HTMLElement>(".den-msg-actions, .den-user-msg-footer--actions"),
      ].map((el) => Math.abs(el.getBoundingClientRect().height - expected));
      inner.style.width = "";
      return Math.max(0, ...deltas);
    });
    expect(narrowHeights).toBeLessThan(1);
  });
});
