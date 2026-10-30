import { expect } from "@playwright/test";
import type { CommandActivity, CommandProgramReceipt } from "../src/api/types.ts";
import { apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e as test, settledChatSessionId, transcriptMessages } from "./helpers.ts";

test("command activities contain long text at narrow card widths", async ({ page, request }, testInfo) => {
 await bootstrapChatSession(page, request);
 const session = await settledChatSessionId(page);
 const assistant = crypto.randomUUID();
 const call = crypto.randomUUID();
 const source = `cd /${"long-directory/".repeat(30)} && cargo build && cargo test`;
 const facts: CommandActivity[] = ["build", "test"].map(id => ({ id, group: "Running programs", label: "Run process", subject: `${id}-${"x".repeat(256)}`, directory: `/${"directory/".repeat(24)}`, state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: [id], spans: [] }));
 const program: CommandProgramReceipt = { id: crypto.randomUUID(), parent_receipt_id: crypto.randomUUID(), language_version: 1, catalog_digest: "catalog", source_digest: "source", source_ref: "source", state: "completed", detail_ref: "detail", progress_state: "unchanged", started_at: "2026-10-30T10:00:00Z", activities: facts };
 await page.route("**/program-receipts/**", route => route.fulfill({ json: { program, executions: [], nodes: [], executions_omitted: 0, nodes_omitted: 0 } }));
 await apiSeedSessionTranscript(request, session, transcriptMessages([
  { id: assistant, role: "assistant", content: "", ord: 1, tool_calls: [{ id: call, name: "command", args: { command: source } }], created_at: "2026-10-30T10:00:00Z" },
  { id: crypto.randomUUID(), role: "tool", content: "", ord: 2, tool_result: { tool: "command", tool_call_id: call, assistant_message_id: assistant, tool_args: { command: source }, content: "", outcome: "completed", command_program: program }, created_at: "2026-10-30T10:00:01Z" },
 ]));
 const group = liveChatStage(page).getByTestId("activity-span-card").first();
 await expect(group).toBeVisible();
 if (await group.getAttribute("data-expanded") !== "true") await group.locator(":scope > .den-transcript-disclosure-header").click();
 const rows = group.locator("[data-command-activity]");
 await expect(rows).toHaveCount(2);
 const row = rows.last();
 for (const width of [320,420,680]) {
  await group.evaluate((element,width) => { element.style.width = `${width}px`; element.style.maxWidth = `${width}px`; },width);
  if (await row.getAttribute("data-expanded") !== "true") await row.locator(":scope > .den-transcript-disclosure-header").click();
  await expect(row.getByTestId("command-activity-details")).toBeVisible();
  await expect.poll(() => group.evaluate(element => {
   const boundary = element.getBoundingClientRect();
   const selectors = [".den-tool-part-chicklet-main", ".den-command-activity-outcome", ".den-row-mark", ".den-command-activity-details", ".den-command-activity-command"];
   return selectors.flatMap(selector => [...element.querySelectorAll<HTMLElement>(selector)]).every(child => {
    const rect = child.getBoundingClientRect();
    return rect.left >= boundary.left-1 && rect.right <= boundary.right+1;
   });
  })).toBe(true);
  expect(await row.locator(".den-tool-part-title").first().evaluate(element => getComputedStyle(element).whiteSpace)).toBe("nowrap");
  expect(await row.locator(".den-command-activity-command").evaluate(element => element.scrollWidth <= element.clientWidth+1)).toBe(true);
  for (const selector of [".den-tool-part-name", ".den-command-activity-outcome"]) {
   expect(await row.locator(selector).evaluate(element => element.scrollWidth <= element.clientWidth+1)).toBe(true);
  }
  await page.mouse.move(0,0);
  await page.getByTestId("den-tooltip").waitFor({state:"hidden"});
  await group.screenshot({ path: testInfo.outputPath(`activity-${width}.png`) });
 }
});
