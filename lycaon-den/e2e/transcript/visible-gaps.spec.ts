import { expect, type Locator } from "@playwright/test";
import {
  activateProject, apiSeedSessionTranscript, bootstrapChatSession, expectShellReady, liveChatStage, openSessionRow,
  modelIndependentWebE2e as test, settledChatSessionId, transcriptMessages,
} from "../helpers.ts";

async function bounds(element: Locator) {
  return element.evaluate((node) => {
    const { top, bottom } = node.getBoundingClientRect();
    return { top, bottom };
  });
}

for (const scale of [1, 53 / 17]) {
  test(`visible transcript edges at text scale ${scale}`, async ({ page, request }, info) => {
    await page.setViewportSize({ width: 1600, height: 1200 });
    const project = await bootstrapChatSession(page, request);
    const session = await settledChatSessionId(page);
    const opening = crypto.randomUUID();
    const call = crypto.randomUUID();
    const reply = crypto.randomUUID();
    const next = crypto.randomUUID();
    const after = "export const greeting = 'hello';\n";
    await page.route("**/source/walk-summary?*", (route) => route.fulfill({
      json: { turns: [{ message_id: opening, turn: 1, steps: 1, items: 1 }] },
    }));
    await apiSeedSessionTranscript(request, session, transcriptMessages([
      { id: opening, role: "user", content: "Add a greeting.", ord: 1, seq: 1 },
      { id: crypto.randomUUID(), role: "system", content: "", ord: 2, seq: 2, kind: "progress_update",
        progress_update: { initial: true, seq: 1, steps: [{ label: "Add the greeting", state: "pending" }] } },
      { id: call, role: "assistant", content: "", ord: 3, seq: 3,
        tool_calls: [{ id: "write-greeting", name: "write", args: { path: "greeting.ts", content: after } }] },
      { id: crypto.randomUUID(), role: "tool", content: "Saved.", ord: 4, seq: 4,
        tool_result: { tool: "write", tool_call_id: "write-greeting", assistant_message_id: call,
          content: "Saved.", file_edits: [{ path: "greeting.ts", before: "", after }] } },
      { id: crypto.randomUUID(), role: "system", content: "", ord: 5, seq: 5, kind: "progress_complete",
        progress_complete: { seq: 2, steps: [{ label: "Add the greeting", state: "done" }] } },
      { id: reply, role: "assistant", content: "Added the greeting.", ord: 6, seq: 6, status: "complete",
        grounding: { traced: true, cited_evidence: [{ handle: "read#1", path: "greeting.ts", line: 1,
          excerpt: after, verdict: "matched" }] } },
      { id: next, role: "user", content: "Show me how to use it.", ord: 7, seq: 7 },
    ]));
    await page.route(`**/sessions/${session}/messages?*`, async (route) => {
      const response = await route.fetch();
      const data = await response.json();
      data.turn_clocks[opening] = { session_id: session, opening_message_id: opening,
        running: false, active_ms: 3000, work_ms: 3000, settled_at: "1970-01-01T00:00:03Z" };
      await route.fulfill({ response, json: data });
    });
    await page.reload();
    await expectShellReady(page);
    await activateProject(page, project.id);
    await openSessionRow(page, session);
    const stage = liveChatStage(page);
    const review = stage.getByTestId("turn-walk-card");
    await expect(review).toBeVisible();
    await page.evaluate((value) => {
      (window as unknown as { __harness: { setTextScale(scale: number): void } }).__harness.setTextScale(value);
    }, scale);
    const user = stage.locator(`[data-msg-id="${opening}"] .bubble--user`);
    const second = stage.locator(`[data-msg-id="${next}"] .bubble--user`);
    const prose = stage.locator(`[data-msg-id="${reply}"] .bubble--assistant`);
    const grounded = stage.getByTestId("citation-evidence-chicklet");
    const delta = stage.getByTestId("progress-strip-completed");
    const diff = stage.getByTestId("diff-group");
    const activity = diff.locator("xpath=ancestor::*[@data-index][1]/preceding-sibling::*[@data-index][1]/*[1]");
    const footer = stage.getByTestId("turn-tail").first();
    const label = (link: Locator) => link.locator(".den-transcript-disclosure-header > :first-child");
    const distances: [Locator, Locator, number][] = [
      [user, stage.getByTestId("progress-strip-created"), 48],
      [stage.getByTestId("progress-strip-created"), activity, 16],
      [activity, label(diff), 16],
      [label(diff), delta.getByTestId("progress-strip-rest"), 16],
      [delta.getByTestId("progress-strip-rest"), prose, 48],
      [label(grounded), review, 48],
      [review, footer, 16],
      [footer, second, 48],
    ];
    for (const [above, below, expected] of distances) {
      await expect.poll(async () => {
        const upper = await bounds(above);
        const lower = await bounds(below);
        return Math.abs(lower.top - upper.bottom - expected * scale);
      }).toBeLessThanOrEqual(0.5);
    }
    for (const chip of [diff.locator("button").first(), grounded.locator("button").first(), delta.locator("button").first()]) {
      await chip.scrollIntoViewIfNeeded();
      expect(await chip.evaluate((button) => {
        const rect = button.getBoundingClientRect();
        return [rect.top + 0.5, rect.bottom - 0.5].every((y) =>
          button.contains(document.elementFromPoint(rect.left + rect.width / 2, y)));
      })).toBe(true);
    }
    for (const [message, lastEdge, following] of [[user, user, distances[0][1]], [prose, label(grounded), review]]) {
      const actions = message.locator("xpath=ancestor::*[contains(@class,'transcript-viewport-row')][1]").locator(".den-msg-actions");
      await actions.locator("button").first().focus();
      await expect(actions).toHaveCSS("opacity", "1");
      const bar = await bounds(actions);
      expect(bar.top).toBeGreaterThanOrEqual((await bounds(lastEdge)).bottom);
      expect(bar.bottom).toBeLessThanOrEqual((await bounds(following)).top);
      if (scale === 1) await info.attach(message === user ? "first-turn" : "turn-boundary", {
        body: await page.screenshot(), contentType: "image/png",
      });
    }
  });
}
