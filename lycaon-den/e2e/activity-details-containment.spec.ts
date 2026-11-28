import { expect, type Route } from "@playwright/test";
import { buildTestFilmstripZip } from "../src/chat/visual/filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "../src/chat/visual/filmstrip-zip.ts";
import {
  activateProject,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  modelIndependentWebE2e,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
} from "./helpers.ts";

const SUMMARY = {
  purpose: "The action intends to capture a filmstrip of moves played on the tic-tac-toe game against the AI and verify board updates.",
  change: "The activity resulted in 2 total changes being made to the target `div` with the ID \"board\" on the tic-tac-toe page.",
};

modelIndependentWebE2e("activity details with a wide filmstrip stay inside their card box", async ({ page, request }) => {
  const project = await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const artifactId = crypto.randomUUID();
  const carrier = crypto.randomUUID();

  await page.route(`**/v1/sessions/${sessionId}/artifacts/${artifactId}`, (route) => route.fulfill({
    contentType: FILMSTRIP_MIME,
    body: Buffer.from(buildTestFilmstripZip(false, 12)),
  }));
  await page.route("**/v1/projects/*/briefings", async (route: Route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const section = (section_id: string, text: string) => ({
      section_id, text, manifest_hash: section_id.padEnd(64, "0"), source: "summarizer", reused: false,
      provider_id: "", model: "", evidence_quality: [], created_at: "2026-07-23T20:00:03Z",
    });
    await route.fulfill({
      json: {
        profile_id: "tool_activity", profile_version: "2", target_key: "card", scope_fingerprint: "card", target: {},
        cache_epoch: "1", revision: "1", status: "complete", pending_sections: [], outcome: { state: "unknown" },
        error_code: "", error_message: "",
        result: {
          evidence_ref: { project_id: project.id, record_id: "r".repeat(64) },
          summary: [section("purpose", SUMMARY.purpose), section("change", SUMMARY.change)],
        },
      },
    });
  });

  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "user", content: "Play a few moves and capture.", created_at: "2026-07-23T20:00:00Z", seq: 1, ord: 1 },
    {
      id: carrier, role: "assistant", content: "", status: "complete", created_at: "2026-07-23T20:00:01Z", seq: 2, ord: 2,
      tool_calls: [{ id: "call_capture_1", name: "capture_page", args: { label: "Tic-tac-toe: played a few moves against the AI" } }],
    },
    {
      id: crypto.randomUUID(), role: "tool", content: "Captured 12 steps", created_at: "2026-07-23T20:00:02Z", seq: 3, ord: 3,
      tool_result: {
        tool_call_id: "call_capture_1",
        assistant_message_id: carrier,
        tool: "capture_page",
        content: "Captured 12 steps",
        outcome: "completed",
        visual: { id: artifactId, mime: FILMSTRIP_MIME, source: "capture", caption: "Tic-tac-toe: played a few moves against the AI" },
      },
    },
  ]));

  await page.reload();
  await expectShellReady(page);
  await activateProject(page, project.id);
  await openSessionRow(page, sessionId);

  const stream = liveChatStage(page).locator(`[data-testid="chat-stream"][data-session-id="${sessionId}"]`);
  const span = stream.getByTestId("activity-span-card");
  await span.locator(":scope > .den-transcript-disclosure-header").click();
  const card = stream.locator('[data-testid="action-card"][data-tool-call-id="call_capture_1"][data-status="done"]');
  await expect(card).toBeVisible({ timeout: 30_000 });
  if ((await card.getAttribute("data-expanded")) !== "true") {
    await card.locator(":scope > .den-transcript-disclosure-header").click();
  }
  await expect(card).toHaveAttribute("data-expanded", "true");
  await expect(card.getByTestId("activity-summary")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
  const rail = card.getByTestId("transcript-visual-filmstrip-rail");
  await expect(rail).toBeVisible({ timeout: 15_000 });
  const viewport = rail.locator(":scope > .den-scrollport__viewport");
  await expect.poll(() => viewport.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);

  const assertContained = async () => {
    const violations = await card.evaluate((cardEl) => {
      const spanEl = cardEl.closest<HTMLElement>('[data-testid="activity-span-card"]');
      const cardRect = cardEl.getBoundingClientRect();
      const limit = Math.min(cardRect.right, spanEl?.getBoundingClientRect().right ?? Infinity);
      const selectors = [
        '[data-testid="action-card-body"]',
        '[data-testid="action-ran"]',
        '[data-testid="action-ran-copy"]',
        '[data-testid="activity-summary"]',
        '[data-testid="activity-summary"] [data-section="intent"] .den-activity-summary__text',
        '[data-testid="activity-summary"] [data-section="outcome"] .den-activity-summary__text',
        '[data-testid="action-facts"]',
        '[data-testid="transcript-visual-filmstrip"]',
        '[data-testid="transcript-visual-filmstrip-reading"]',
        '[data-testid="transcript-visual-filmstrip-rail"]',
        '[data-testid="action-links"]',
      ];
      const bad: string[] = [];
      for (const selector of selectors) {
        const el = cardEl.querySelector<HTMLElement>(selector);
        if (!el) continue;
        const rect = el.getBoundingClientRect();
        if (rect.right > limit + 1) {
          bad.push(`${selector}: right=${Math.round(rect.right)} > limit=${Math.round(limit)} (width=${Math.round(rect.width)})`);
        }
      }
      return bad;
    });
    expect(violations).toEqual([]);
  };

  const stageGeometry = () => card.evaluate((cardEl) => {
    const hero = cardEl.querySelector<HTMLElement>(".den-transcript-visual-filmstrip__hero")!.getBoundingClientRect();
    const reading = cardEl.querySelector<HTMLElement>('[data-testid="transcript-visual-filmstrip-reading"]')!.getBoundingClientRect();
    return { hero, reading };
  });

  await assertContained();
  const wideStage = await stageGeometry();
  expect(wideStage.reading.left).toBeGreaterThanOrEqual(wideStage.hero.right - 1);

  // Narrow the viewport so the filmstrip falls below its 24rem stack threshold.
  await page.setViewportSize({ width: 360, height: 900 });
  await assertContained();
  await expect.poll(async () => {
    const { hero, reading } = await stageGeometry();
    return reading.top >= hero.bottom - 1;
  }).toBe(true);
  await expect.poll(() => viewport.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);
});
