import { expect, type Page, type Route } from "@playwright/test";
import type { SessionTranscriptPage, WorkerTask, WorkerListResponse } from "../src/api/types.ts";
import { bootstrapChatSession, openBottomTab, webE2e } from "./helpers.ts";

const WORKER_ID = "5b0c2f9e-3a7d-4c61-9f0e-7d2b8a4e1c90";
const CHILD_SESSION_ID = "8e1d4c3b-2f6a-4b9e-a7d5-0c9f1e2b3a47";
const ROWS = 1000;

type Message = SessionTranscriptPage["messages"][number];

const transcript: Message[] = Array.from({ length: ROWS }, (_, index) => ({
  id: `worker-row-${index + 1}`,
  worker_id: WORKER_ID,
  ord: index + 1,
  seq: index + 1,
  role: "assistant",
  origin: "model",
  authority: "none",
  trust_tier: "trusted",
  status: "complete",
  content: `Step ${index + 1}.`,
  created_at: "2026-09-01T12:00:00Z",
}));

/** Serves the host's window positions: newest page, the oldest page, or beside a held message. */
function workerPage(url: URL): SessionTranscriptPage {
  const limit = Number(url.searchParams.get("limit") ?? 100);
  const ordOf = (id: string) => Number(id.slice("worker-row-".length));
  const before = url.searchParams.get("before_message_id");
  const after = url.searchParams.get("after_message_id");
  let messages: Message[];
  if (after !== null) messages = transcript.filter((row) => (row.ord ?? 0) > ordOf(after)).slice(0, limit);
  else if (url.searchParams.get("from") === "oldest") messages = transcript.slice(0, limit);
  else if (before !== null) messages = transcript.filter((row) => (row.ord ?? 0) < ordOf(before)).slice(-limit);
  else messages = transcript.slice(-limit);
  const oldest = messages[0]?.ord ?? 0;
  const newest = messages.at(-1)?.ord ?? 0;
  return {
    messages,
    watermark: ROWS,
    turn_clocks: {},
    turn_loads: {},
    before_cursor: oldest > 1 ? `older-than-${oldest}` : undefined,
    after_cursor: newest < ROWS ? `newer-than-${newest}` : undefined,
  };
}

type PageGate = { edge: "before" | "after"; release: () => void };

async function installWorkerRoutes(page: Page): Promise<{ hold: (edge: "before" | "after") => Promise<PageGate> }> {
  let sessionId = "";
  let pendingHold: { edge: "before" | "after"; held: (gate: PageGate) => void } | undefined;
  await page.route("**/v1/sessions", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    const response = await route.fetch();
    const body = (await response.json()) as { id?: string };
    if (body.id) sessionId = body.id;
    await route.fulfill({ response });
  });
  const worker = (): WorkerTask => ({
    id: WORKER_ID,
    parent_session_id: sessionId,
    child_session_id: CHILD_SESSION_ID,
    agent_type: "implementer",
    status: "complete",
    brief: "Walk a long transcript",
    created_at: "2026-09-01T11:00:00Z",
    tool_loops_used: ROWS,
  });
  // The session bootstrap and later roster refreshes both carry the worker.
  await page.route("**/v1/sessions/*/bootstrap", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as { session?: { id?: string }; workers?: WorkerTask[] };
    if (body.session?.id) sessionId = body.session.id;
    await route.fulfill({ response, json: { ...body, workers: [worker()] } });
  });
  await page.route("**/v1/workers**", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    await route.fulfill({ json: { workers: [worker()] } satisfies WorkerListResponse });
  });
  await page.route(`**/v1/sessions/${CHILD_SESSION_ID}/messages**`, async (route: Route) => {
    const url = new URL(route.request().url());
    const edge = url.searchParams.has("before_message_id") ? "before"
      : url.searchParams.has("after_message_id") || url.searchParams.has("from") ? "after" : undefined;
    if (edge && pendingHold?.edge === edge) {
      const hold = pendingHold;
      pendingHold = undefined;
      await new Promise<void>((release) => hold.held({ edge, release }));
    }
    await route.fulfill({ json: workerPage(url) });
  });
  return {
    hold: (edge) => new Promise<PageGate>((held) => { pendingHold = { edge, held }; }),
  };
}

const drawerViewport = (page: Page) => page.getByTestId("worker-transcript");

/** The first activity row crossing the top of the drawer, and where it sits. */
async function readingRow(page: Page): Promise<{ key: string; top: number }> {
  return drawerViewport(page).evaluate((viewport) => {
    const line = viewport.getBoundingClientRect().top;
    const rows = [...viewport.querySelectorAll<HTMLElement>("[data-worker-item-key]")]
      .map((row) => ({ row, box: row.getBoundingClientRect() }))
      .filter(({ box }) => box.bottom > line)
      .sort((a, b) => a.box.top - b.box.top);
    const first = rows[0];
    if (!first) throw new Error("No activity row crosses the drawer's top edge.");
    return { key: first.row.dataset.workerItemKey ?? "", top: first.box.top - line };
  });
}

async function rowTop(page: Page, key: string): Promise<number | undefined> {
  return drawerViewport(page).evaluate((viewport, rowKey) => {
    const row = viewport.querySelector<HTMLElement>(`[data-worker-item-key="${CSS.escape(rowKey)}"]`);
    return row ? row.getBoundingClientRect().top - viewport.getBoundingClientRect().top : undefined;
  }, key);
}

async function scrollDrawerBy(page: Page, viewports: number) {
  await drawerViewport(page).evaluate((viewport, by) => {
    viewport.scrollTop += viewport.clientHeight * by;
  }, viewports);
}

/** Holds the next page at `edge`, then proves the row being read stays put when it lands. */
async function expectReadingRowHeldAcross(page: Page, gates: { hold: (edge: "before" | "after") => Promise<PageGate> }, edge: "before" | "after", step: number) {
  const held = gates.hold(edge);
  let gate: PageGate | undefined;
  await expect(async () => {
    await scrollDrawerBy(page, step);
    gate = await Promise.race([held, new Promise<undefined>((resolve) => setTimeout(resolve, 150))]);
    expect(gate).toBeDefined();
  }).toPass({ timeout: 60_000 });
  const before = await readingRow(page);
  const landed = page.waitForResponse((response) => response.url().includes(`/v1/sessions/${CHILD_SESSION_ID}/messages`) && response.url().includes(`${edge}_message_id=`));
  gate!.release();
  await landed;
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const after = await rowTop(page, before.key);
  expect(after, `row ${before.key} stays mounted`).toBeDefined();
  expect(Math.abs((after ?? 0) - before.top)).toBeLessThanOrEqual(2);
}

webE2e.describe("worker activity paging", () => {
  webE2e("reads a long worker transcript from its first row to its live end in one scroll surface", async ({ page }) => {
    webE2e.setTimeout(180_000);
    const gates = await installWorkerRoutes(page);
    await bootstrapChatSession(page);
    await openBottomTab(page, "workers");
    await page.getByTestId("workers-tab-row").first().getByRole("button").first().click();
    const activity = page.getByTestId("worker-activity");
    await expect(activity).toBeVisible();

    // Meeting the activity from above reads it from the first row.
    await expect(activity.locator("[data-worker-item-key]").first()).toContainText("Step 1.", { timeout: 30_000 });
    await expect(page.getByRole("button", { name: /^(Earlier|Later|Latest) activity$/ })).toHaveCount(0);
    // Rows scroll with the drawer rather than inside a box of their own.
    await expect(activity.locator("[data-den-scrollport]")).toHaveCount(0);

    // Reading forward loads pages below, and past the page budget evicts the oldest ones
    // above, without moving the reader.
    for (let pageIndex = 0; pageIndex < 7; pageIndex++) {
      await expectReadingRowHeldAcross(page, gates, "after", 1);
    }
    await expect(async () => {
      await scrollDrawerBy(page, 3);
      await expect(activity.getByText(`Step ${ROWS}.`, { exact: true })).toBeVisible({ timeout: 500 });
    }).toPass({ timeout: 90_000 });

    // The drawer reads through to its end, each section header stacking at the top as it passes.
    await expect(async () => {
      await scrollDrawerBy(page, 3);
      expect(await drawerViewport(page).evaluate((viewport) =>
        viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 1)).toBe(true);
    }).toPass({ timeout: 30_000 });
    const stack = await drawerViewport(page).evaluate((viewport) => {
      const top = viewport.getBoundingClientRect().top;
      return [...viewport.querySelectorAll<HTMLElement>(".den-worker-transcript-section-summary")].map((header) => {
        const box = header.getBoundingClientRect();
        return { title: header.textContent?.trim(), top: box.top - top, height: box.height };
      });
    });
    expect(stack.map((header) => header.title)).toEqual(["Task", "Activity", "Coordination", "Evidence"]);
    stack.forEach((header, index) => {
      // Adjacent sticky headers share a one-pixel rule.
      const stacked = stack.slice(0, index).reduce((sum, above) => sum + above.height - 1, 0);
      expect(Math.abs(header.top - stacked), `${header.title} sticks under the headers above it`).toBeLessThanOrEqual(3);
    });

    // Returning upward reloads evicted history above the reader without moving them.
    await expectReadingRowHeldAcross(page, gates, "before", -2);
  });
});
