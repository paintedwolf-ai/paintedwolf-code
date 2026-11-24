import { expect, type Page } from "@playwright/test";
import { apiJson, apiPostPrompt, bootstrapChatSession, liveChatStage, settledChatSessionId, webE2e } from "../helpers.ts";
import { manualHarnessTools } from "../manual-harness-tools.ts";

const CARDS = '[data-testid="progress-strip-created"], [data-testid="activity-group-card"]';

type CardWidth = { card: string; open: boolean; width: number; row: number; cap: number };

/** Measures each card on screen with its open state, row width, and width cap. */
async function cardWidths(page: Page): Promise<CardWidth[]> {
  return page.evaluate((selector) => {
    const cap = 32 * Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
    return [...document.querySelectorAll<HTMLElement>(selector)]
      .filter((card) => card.getBoundingClientRect().width > 0)
      .map((card) => ({
        card: card.dataset.testid!,
        open: card.querySelector('[data-expanded="true"]') !== null,
        width: card.getBoundingClientRect().width,
        row: card.closest<HTMLElement>(".transcript-viewport-row")!.getBoundingClientRect().width,
        cap,
      }));
  }, CARDS);
}

function expectBothCards(widths: CardWidth[]) {
  expect([...new Set(widths.map((entry) => entry.card))].sort()).toEqual(["activity-group-card", "progress-strip-created"]);
}

webE2e("a progress box is as wide as an activity group in single and split view", async ({ page, request }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const prompt = await apiPostPrompt(request, sessionId, { text: "List the project files." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    await driver.completed(await driver.invoke("update_progress", { content: "## Progress\n- [ ] List the files\n" }));
    await driver.completed(await driver.invoke("command", { command: "ls" }));

    const chat = liveChatStage(page);
    await expect(chat.getByTestId("progress-strip-created")).toBeVisible();
    const group = chat.getByTestId("activity-span-card").last();
    await expect(group).toBeVisible();

    // A wide chat column: closed cards stop at the cap.
    const single = await cardWidths(page);
    expectBothCards(single);
    for (const entry of single) {
      expect(entry.row, entry.card).toBeGreaterThan(entry.cap);
      expect(entry.width, entry.card).toBeCloseTo(entry.cap, 0);
    }

    // An open group takes the full row; closed cards beside it stay capped.
    await group.locator("> .den-transcript-disclosure-toggle").click();
    await expect(group).toHaveAttribute("data-expanded", "true");
    const open = await cardWidths(page);
    for (const entry of open) {
      const expected = entry.open ? entry.row : entry.cap;
      expect(entry.width, entry.card).toBeCloseTo(expected, 0);
    }
    await group.locator("> .den-transcript-disclosure-toggle").click();
    await expect(group).toHaveAttribute("data-expanded", "false");

    await page.getByTestId("project-files-entry").click();
    await page.getByTestId("layout-dock-btn").click();
    await page.getByTestId("layout-tile-split").click();
    await page.getByTestId("layout-dock-btn").click();
    await expect(page.getByTestId("split-divider")).toBeVisible();
    const splitChat = page.getByTestId("split-col-chat");
    await expect(splitChat.getByTestId("progress-strip-created")).toBeVisible();

    // A chat column narrower than the cap: both cards fill their row.
    const split = await cardWidths(page);
    expectBothCards(split);
    for (const entry of split) {
      expect(entry.row, entry.card).toBeLessThan(entry.cap);
      expect(entry.width, entry.card).toBeCloseTo(entry.row, 0);
    }
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Listed." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Listed." });
  }
});
