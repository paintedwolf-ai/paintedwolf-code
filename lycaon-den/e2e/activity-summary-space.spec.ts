import { expect, type Page, type Route } from "@playwright/test";
import {
  activateProject,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

const SHORT = { purpose: "Reads the middleware to find the auth check.", change: "Found it in middleware.go." };
const LONG = {
  purpose: "Reads the middleware to find where requests are authenticated before the router sees them.",
  change: "Found the token check in middleware.go, which reads the Authorization header, verifies the signature against the cached key set, " +
    "refreshes that set when a key id is unknown, and rejects expired tokens with a 401 before any handler runs, so every route inherits it. " +
    "The same middleware also records the caller's subject on the request context, which the audit logger and the rate limiter both read, " +
    "and it exempts only the health and metrics endpoints, which are registered before the middleware is installed on the router.",
};

async function seedReadCard(page: Page, request: Parameters<typeof bootstrapChatSession>[1]) {
  const project = await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const carrier = crypto.randomUUID();
  await apiSeedSessionTranscript(request ?? page.request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "user", content: "Where is auth?", created_at: "2026-07-23T20:00:00Z", seq: 1, ord: 1 },
    { id: carrier, role: "assistant", content: "", status: "complete", created_at: "2026-07-23T20:00:01Z", seq: 2, ord: 2,
      tool_calls: [{ id: "call_read_1", name: "read", args: { path: "middleware.go" } }] },
    { id: crypto.randomUUID(), role: "tool", content: "package auth", created_at: "2026-07-23T20:00:02Z", seq: 3, ord: 3,
      tool_result: { tool_call_id: "call_read_1", assistant_message_id: carrier, tool: "read", content: "package auth", outcome: "completed" } },
  ]));
  // Reopen the seeded session so it is activated once, before any card opens.
  await page.reload();
  await expectShellReady(page);
  await activateProject(page, project.id);
  await openSessionRow(page, sessionId);
  return sessionId;
}

/** Answers the card's summary request after a delay, as a slow summarizer would. */
async function answerSummaryAfter(page: Page, text: { purpose: string; change: string }, delayMs: number) {
  await page.route("**/v1/projects/*/briefings", async (route: Route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await new Promise((resolve) => setTimeout(resolve, delayMs));
    const section = (section_id: string, value: string) => ({
      section_id, text: value, manifest_hash: section_id.padEnd(64, "0"), source: "summarizer", reused: false,
      provider_id: "", model: "", evidence_quality: [], created_at: "2026-07-23T20:00:03Z",
    });
    await route.fulfill({ json: {
      profile_id: "tool_activity", profile_version: "2", target_key: "card", scope_fingerprint: "card", target: {},
      cache_epoch: "1", revision: "1", status: "complete", pending_sections: [], outcome: { state: "unknown" },
      error_code: "", error_message: "",
      result: { evidence_ref: { project_id: "p", record_id: "r".repeat(64) }, summary: [section("purpose", text.purpose), section("change", text.change)] },
    } });
  });
}

/** Offset of the card body below the summary from the card's top. */
async function bodyOffset(page: Page): Promise<number> {
  return page.evaluate(() => {
    const card = document.querySelector<HTMLElement>('[data-testid="activity-summary"]')?.closest<HTMLElement>('[data-testid="tool-part-card"]');
    const below = card?.querySelector<HTMLElement>(".den-tool-part-card-structured");
    if (!card || !below) return Number.NaN;
    return below.getBoundingClientRect().top - card.getBoundingClientRect().top;
  });
}

async function openCard(page: Page, sessionId: string) {
  const stream = liveChatStage(page).locator(`[data-testid="chat-stream"][data-session-id="${sessionId}"]`);
  await stream.getByTestId("activity-span-card").locator(":scope > .den-transcript-disclosure-header").click();
  // The row settles once its result joins the call; opening earlier would open a row that is about to be replaced.
  const card = stream.locator('[data-testid="tool-part-card"][data-tool-call-id="call_read_1"][data-status="done"]');
  await expect(card).toBeVisible({ timeout: 30_000 });
  // A span with a single action opens it along with the span.
  if ((await card.getAttribute("data-expanded")) !== "true") await card.locator(":scope > .den-transcript-disclosure-header").click();
  await expect(card).toHaveAttribute("data-expanded", "true");
  // Measure only once the opening motion has finished.
  await card.evaluate((element) => Promise.all(element.getAnimations({ subtree: true }).map((animation) => animation.finished)));
  await expect(page.getByTestId("activity-summary")).toHaveAttribute("data-state", "pending");
}

webE2e.describe("activity summary space", () => {
  for (const [name, text] of [["short", SHORT], ["long", LONG]] as const) {
    webE2e(`a ${name} summary arriving after the card opens moves nothing below it`, async ({ page, request }) => {
      await answerSummaryAfter(page, text, 1500);
      const sessionId = await seedReadCard(page, request);
      await openCard(page, sessionId);
      const before = await bodyOffset(page);
      await expect(page.getByTestId("activity-summary")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
      await expect(page.getByTestId("activity-summary")).toContainText(text.purpose.slice(0, 30));
      expect(await bodyOffset(page)).toBeCloseTo(before, 0);
      if (name === "long") {
        await page.getByTestId("activity-summary-show-all").click();
        expect(await bodyOffset(page)).toBeGreaterThan(before);
      }
    });
  }
});
