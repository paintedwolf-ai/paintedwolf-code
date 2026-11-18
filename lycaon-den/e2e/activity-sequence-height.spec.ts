import { expect } from "@playwright/test";
import type { CommandActivity, CommandProgramReceipt } from "../src/api/types.ts";
import {
  apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e,
  settledChatSessionId, transcriptMessages,
} from "./helpers.ts";

modelIndependentWebE2e("the first activity arrow preserves the collapsed card height", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const session = await settledChatSessionId(page);
  const assistant = crypto.randomUUID();
  const call = crypto.randomUUID();
  const created = "2026-10-30T10:00:00Z";
  const activities: CommandActivity[] = ["Running programs", "Reading files"].map((group, index) => ({
    id: String(index), group, label: index ? "Read file" : "Run process",
    subject: index ? "result.txt" : "make", directory: ".", state: "completed",
    outcome: "Completed", execution_ids: [String(index)], spans: [], steps: [],
  }));
  const program: CommandProgramReceipt = {
    id: crypto.randomUUID(), parent_receipt_id: crypto.randomUUID(), language_version: 1,
    catalog_digest: "catalog", source_digest: "source", source_ref: "source",
    state: "running", detail_ref: "detail", progress_state: "unchanged",
    started_at: created, activities: activities.slice(0, 1),
  };
  let release = () => {};
  const nextReceipt = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/program-receipts/**", async route => {
    await nextReceipt;
    await route.fulfill({ json: {
      program: { ...program, activities, activity_revision: 2 }, activities,
      executions: [], nodes: [], executions_omitted: 0, nodes_omitted: 0,
    } });
  });
  try {
    await apiSeedSessionTranscript(request, session, transcriptMessages([
      { id: assistant, role: "assistant", content: "", ord: 1, created_at: created,
        tool_calls: [{ id: call, name: "command", args: { command: "make && cat result.txt" } }] },
      { id: crypto.randomUUID(), role: "tool", content: "", ord: 2, created_at: created,
        tool_result: { tool: "command", tool_call_id: call, assistant_message_id: assistant,
          content: "", outcome: "completed", command_program: program } },
    ]));
    const card = liveChatStage(page).getByTestId("activity-span-card").first();
    await expect(card).toHaveAttribute("data-count", "1");
    const header = card.getByRole("button", { expanded: false }).first();
    await expect(header).toContainText("Running programs");
    await page.evaluate(() => document.fonts.ready);
    const before = await card.boundingBox();
    await expect(header.locator(".den-activity-span-hint")).toHaveText("");
    release();
    await expect(card).toHaveAttribute("data-count", "2");
    await expect(header).toContainText("Running programs → Reading files");
    const after = await card.boundingBox();
    expect(before).not.toBeNull();
    expect(after).not.toBeNull();
    expect(after!.height).toBe(before!.height);
  } finally {
    release();
  }
});
