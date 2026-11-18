import { expect, type Locator } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "../helpers.ts";

type Box = { left: number; top: number; width: number; height: number };

async function box(locator: Locator): Promise<Box> {
  return locator.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { left: rect.left, top: rect.top, width: rect.width, height: rect.height };
  });
}

/** The colour the box's border paints; transparent while the update rests. */
async function borderAlpha(locator: Locator): Promise<number> {
  return locator.evaluate((element) => {
    const color = getComputedStyle(element).borderTopColor;
    const parts = color.match(/[\d.]+/g) ?? [];
    return color.startsWith("rgba") || parts.length === 4 ? Number(parts[3]) : 1;
  });
}

async function toggleAndSettle(update: Locator, expanded: boolean) {
  await update.getByRole("button").click();
  await expect(update).toHaveAttribute("data-expanded", String(expanded));
  await expect(update).not.toHaveAttribute("data-animating", /.*/);
}

webE2e(
  "a progress update rests as one line and opens into the same box as the plan",
  async ({ page, request }) => {
    await page.setViewportSize({ width: 1600, height: 1000 });
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const done = (label: string) => ({ label, state: "done" as const });
    const pending = (label: string) => ({ label, state: "pending" as const });
    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages([
        { id: crypto.randomUUID(), role: "user", content: "add the greeting", seq: 1, ord: 1 },
        {
          id: crypto.randomUUID(),
          role: "system",
          content: "",
          kind: "progress_update",
          progress_update: {
            initial: true,
            steps: [pending("Read the app package"), pending("Add a greeting helper"), pending("Run the tests")],
          },
          seq: 2,
          ord: 2,
        },
        {
          id: crypto.randomUUID(),
          role: "system",
          content: "",
          kind: "progress_update",
          progress_update: {
            changes: [
              { kind: "done", label: "Read the app package", state: "done" },
              { kind: "done", label: "Add a greeting helper", state: "done" },
            ],
            summary: { change_count: 2, total_steps: 3, pending: 1, done: 2, na: 0 },
          },
          seq: 3,
          ord: 3,
        },
        {
          id: crypto.randomUUID(),
          role: "system",
          content: "",
          kind: "progress_complete",
          progress_complete: {
            steps: [done("Read the app package"), done("Add a greeting helper"), done("Run the tests")],
          },
          seq: 4,
          ord: 4,
        },
        { id: crypto.randomUUID(), role: "assistant", content: "The greeting is in.", status: "complete", seq: 5, ord: 5 },
      ]),
    );

    const chat = liveChatStage(page);
    const plan = chat.getByTestId("progress-strip-created");
    const update = chat.getByTestId("progress-strip-delta");
    const finished = chat.getByTestId("progress-strip-completed");
    await expect(plan).toBeVisible();
    await expect(update.getByTestId("progress-strip-rest")).toHaveText("✓Read the app package·✓Add a greeting helper");
    await expect(finished.getByTestId("progress-strip-rest")).toHaveText("✓All 3 steps done");
    await expect(update.getByTestId("progress-strip-standing")).toHaveText("2 of 3");

    // At rest: one line each, no box drawn, and the plan lists every step.
    const planBox = await box(plan);
    const planHeader = await box(plan.locator(".progress-strip__header"));
    const restingUpdate = await box(update);
    const restingFinished = await box(finished);
    await expect(plan.getByTestId("progress-strip-item")).toHaveCount(3);
    await expect(update.getByTestId("progress-strip-item").first()).toBeHidden();
    expect(restingUpdate.height).toBeLessThan(planBox.height / 2);
    expect(restingFinished.height).toBeCloseTo(restingUpdate.height, 0);
    expect(await borderAlpha(update)).toBe(0);
    // The resting text starts on the plan box's left edge.
    const restText = await box(update.getByTestId("progress-strip-rest"));
    expect(restText.left).toBeCloseTo(planBox.left, 0);

    // Open: the plan's box, in the plan's place.
    await toggleAndSettle(update, true);
    const openUpdate = await box(update);
    const openHeader = await box(update.locator(".progress-strip__summary"));
    await expect(update.getByTestId("progress-strip-item")).toHaveCount(2);
    await expect(update.getByTestId("progress-strip-item").first()).toBeVisible();
    await expect(update.getByTestId("progress-strip-rest")).toBeHidden();
    await expect(update.getByTestId("progress-strip-standing")).toBeHidden();
    await expect(update.locator(".progress-strip__title")).toHaveText("Updated");
    expect(openUpdate.left).toBeCloseTo(planBox.left, 0);
    expect(openUpdate.width).toBeCloseTo(planBox.width, 0);
    expect(openHeader.height).toBeCloseTo(planHeader.height, 0);
    expect(openUpdate.top).toBeCloseTo(restingUpdate.top, 0);
    await expect.poll(() => borderAlpha(update)).toBeGreaterThan(0);

    // Closed again: the line it started as.
    await toggleAndSettle(update, false);
    const closedUpdate = await box(update);
    expect(closedUpdate.height).toBeCloseTo(restingUpdate.height, 0);
    await expect.poll(() => borderAlpha(update)).toBe(0);
    await expect(update.getByTestId("progress-strip-rest")).toBeVisible();
  },
);
