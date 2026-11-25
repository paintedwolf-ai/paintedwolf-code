import { expect } from "@playwright/test";
import type { CommandActivity, CommandProgramReceipt } from "../../src/api/types.ts";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e,
  settledChatSessionId,
  transcriptMessages,
} from "../helpers.ts";

modelIndependentWebE2e(
  "stacks activities, approvals, and file diffs in first-arrival order without moving earlier headers or overflowing narrow splits",
  async ({ page, request }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await bootstrapChatSession(page, request);
    const session = await settledChatSessionId(page);
    const stage = liveChatStage(page);
    const created = "2026-10-30T10:00:00Z";

    const userId = crypto.randomUUID();
    const assistantId = crypto.randomUUID();
    const callRun = "call-stack-run";
    const callAllow = "call-stack-allow";
    const callWrite = "call-stack-write";
    const resultRunId = crypto.randomUUID();
    const resultAllowId = crypto.randomUUID();
    const resultWriteId = crypto.randomUUID();

    const activities: CommandActivity[] = [
      {
        id: "act-run",
        group: "Running programs",
        label: "Run build",
        subject: "make build",
        directory: ".",
        state: "completed",
        outcome: "Completed",
        execution_ids: ["e-1"],
        spans: [],
        steps: [],
      },
      {
        id: "act-edit",
        group: "Editing files",
        label: "Update report",
        subject: "/external/report.txt",
        directory: ".",
        state: "completed",
        outcome: "Completed",
        execution_ids: ["e-2"],
        spans: [],
        steps: [],
      },
    ];
    const program: CommandProgramReceipt = {
      id: crypto.randomUUID(),
      parent_receipt_id: crypto.randomUUID(),
      language_version: 1,
      catalog_digest: "catalog",
      source_digest: "source",
      source_ref: "source",
      state: "completed",
      detail_ref: "detail",
      progress_state: "unchanged",
      started_at: created,
      activities,
    };

    // Phase 1: Activities section arrives first.
    await apiSeedSessionTranscript(
      request,
      session,
      transcriptMessages([
        { id: userId, role: "user", content: "Build and update report.", ord: 1, seq: 1, created_at: created },
        {
          id: assistantId,
          role: "assistant",
          content: "",
          ord: 2,
          seq: 2,
          created_at: created,
          tool_calls: [
            { id: callRun, name: "command", args: { command: "make build" } },
            { id: callAllow, name: "command", args: { command: "inspect-env" } },
            { id: callWrite, name: "command", args: { command: "write-report" } },
          ],
        },
        {
          id: resultRunId,
          role: "tool",
          content: "",
          ord: 3,
          seq: 3,
          created_at: created,
          tool_result: {
            tool: "command",
            tool_call_id: callRun,
            assistant_message_id: assistantId,
            content: "",
            outcome: "completed",
            command_program: { ...program, activities: activities.slice(0, 1) },
          },
        },
      ]),
    );

    const card = stage.getByTestId("activity-group-card").first();
    await expect(card).toHaveAttribute("data-sections", "activities");
    await expect(card).not.toHaveClass(/den-enter-fade/);
    await page.evaluate(() => document.fonts.ready);

    const actHeader = card.getByTestId("activity-span-card").locator(":scope > .den-transcript-disclosure-header");
    const boxPhase1 = await actHeader.boundingBox();
    expect(boxPhase1).not.toBeNull();

    // Phase 2: Approvals section appends below Activities without moving Activities.
    await apiSeedSessionTranscript(
      request,
      session,
      transcriptMessages([
        {
          id: resultAllowId,
          role: "tool",
          content: "ok",
          ord: 4,
          seq: 4,
          created_at: created,
          tool_result: {
            tool: "command",
            tool_call_id: callAllow,
            assistant_message_id: assistantId,
            content: "ok",
            outcome: "completed",
            checkpoint_decision: {
              checkpoint_id: "cp-allow-1",
              kind: "tool_approval",
              status: "approved",
              tool: "command",
              subject: "inspect-env",
              grant_title: "Allow once",
            },
          },
        },
      ]),
    );

    await expect(card).toHaveAttribute("data-sections", "activities,approvals");
    await expect(stage.getByTestId("chat-stream").getByTestId("tool-approval-card")).toHaveCount(0);
    const boxPhase2 = await actHeader.boundingBox();
    expect(boxPhase2!.y).toBe(boxPhase1!.y);
    expect(boxPhase2!.height).toBe(boxPhase1!.height);

    // Phase 3: Second approval + Files edited section append below Approvals without moving earlier headers.
    await apiSeedSessionTranscript(
      request,
      session,
      transcriptMessages([
        {
          id: resultWriteId,
          role: "tool",
          content: "updated report",
          ord: 5,
          seq: 5,
          created_at: created,
          tool_result: {
            tool: "command",
            tool_call_id: callWrite,
            assistant_message_id: assistantId,
            content: "updated report",
            outcome: "completed",
            command_program: program,
            checkpoint_decision: {
              checkpoint_id: "cp-allow-2",
              kind: "tool_approval",
              status: "approved",
              tool: "command",
              subject: "/external/report.txt",
              grant_title: "Allow once",
            },
            file_edits: [
              { path: "/external/report.txt", before: "old report\n", after: "new report\n" },
              { path: "/external/notes.txt", before: "old notes\n", after: "new notes\n" },
            ],
          },
        },
      ]),
    );

    await expect(card).toHaveAttribute("data-sections", "activities,approvals,files");
    const approvalsHeader = card.getByTestId("activity-group-approvals").locator(":scope > .den-transcript-disclosure-header");
    const approvalsToggle = card.getByTestId("activity-group-approvals-toggle");
    const filesToggle = card.getByTestId("diff-group").locator(":scope > .den-transcript-disclosure-header");
    const boxPhase3 = await actHeader.boundingBox();
    const appBox = await approvalsHeader.boundingBox();
    const filesBox = await filesToggle.boundingBox();

    expect(boxPhase3!.y).toBe(boxPhase1!.y);
    expect(boxPhase3!.height).toBe(boxPhase1!.height);
    expect(Math.abs(appBox!.height - boxPhase3!.height)).toBeLessThanOrEqual(1);
    expect(Math.abs(filesBox!.height - boxPhase3!.height)).toBeLessThanOrEqual(1);

    // Keyboard Enter/Space toggles Approvals disclosure.
    await approvalsToggle.focus();
    await page.keyboard.press("Enter");
    await expect(approvalsToggle).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Space");
    await expect(approvalsToggle).toHaveAttribute("aria-expanded", "false");

    // Files edited -> diff -> Show activity opens Activities and mounts the originating tool row.
    // Opening a section widens the card and every section's chevron follows its right edge.
    const restingCard = (await card.boundingBox())!;
    await filesToggle.click();
    await expect(card.getByTestId("diff-group")).not.toHaveAttribute("data-animating", "true");
    const openCard = (await card.boundingBox())!;
    const openApprovalsHeader = (await approvalsHeader.boundingBox())!;
    expect(openCard.width).toBeGreaterThan(restingCard.width);
    expect(openCard.x + openCard.width - (openApprovalsHeader.x + openApprovalsHeader.width)).toBeLessThanOrEqual(2);
    const reportDiff = card.getByTestId("file-edit-diff").filter({ hasText: "/external/report.txt" });
    await reportDiff.getByText("/external/report.txt", { exact: true }).click();
    await expect(reportDiff).toHaveAttribute("data-expanded", "true");
    await reportDiff.getByTestId("file-edit-show-activity").click();
    await expect(card.getByTestId("activity-span-card")).toHaveAttribute("data-expanded", "true");
    await expect(card.locator(`[data-tool-call-id="${callWrite}"]`)).toBeVisible();

    // Collapse back to single-line headers and verify zero horizontal overflow at wide and <=341px split widths.
    await actHeader.click();
    await filesToggle.click();
    await expect(card.getByTestId("activity-span-card")).toHaveAttribute("data-expanded", "false");
    await expect(card.getByTestId("activity-span-card")).not.toHaveAttribute("data-animating", "true");
    await expect(card.getByTestId("diff-group")).toHaveAttribute("data-expanded", "false");
    await expect(card.getByTestId("diff-group")).not.toHaveAttribute("data-animating", "true");

    const wideFit = await card.evaluate((el) => ({ client: el.clientWidth, scroll: el.scrollWidth }));
    expect(wideFit.scroll).toBe(wideFit.client);

    await page.getByTestId("focused-project-nav").getByRole("button", { name: "Files", exact: true }).click();
    await page.getByTestId("layout-dock-btn").click();
    await page.getByTestId("layout-tile-split").click();
    const divider = page.getByTestId("split-divider");
    await expect(divider).toBeVisible();
    await divider.press("End");
    await expect
      .poll(async () => (await page.getByTestId("split-col-chat").boundingBox())?.width ?? 0)
      .toBeLessThanOrEqual(341);

    const narrowFit = await card.evaluate((el) => ({ client: el.clientWidth, scroll: el.scrollWidth }));
    expect(narrowFit.scroll).toBe(narrowFit.client);
    const narrowActH = (await actHeader.boundingBox())!.height;
    const narrowAppH = (await approvalsHeader.boundingBox())!.height;
    const narrowFilesH = (await filesToggle.boundingBox())!.height;
    expect(Math.abs(narrowAppH - narrowActH)).toBeLessThanOrEqual(1);
    expect(Math.abs(narrowFilesH - narrowActH)).toBeLessThanOrEqual(1);
  },
);
