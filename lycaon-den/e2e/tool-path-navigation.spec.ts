import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import type { CommandActivity, CommandProgramReceipt, CommandProgramReceiptPage } from "../src/api/types.ts";
import {
  activateProject, apiAttachProjectRoot, apiCreateProjectWithRoot,
  apiSeedSessionTranscript, e2eTempDir, gotoShell, liveChatStage,
  modelIndependentWebE2e, settledChatSessionId, transcriptMessages,
  bootstrapChatSession, webE2e,
} from "./helpers.ts";

for (const target of [".", "src", "@other/src"]) {
  modelIndependentWebE2e(`tool path ${target} selects its folder without opening a file tab`, async ({ page, request }, testInfo) => {
    const primary = e2eTempDir(request, "tool-path-primary");
    const secondary = e2eTempDir(request, "tool-path-secondary");
    for (const root of [primary, secondary]) {
      mkdirSync(path.join(root, "src"));
      writeFileSync(path.join(root, "src", "entry.txt"), root);
    }
    const created = await apiCreateProjectWithRoot(request, primary, "Tool path navigation");
    const project = await apiAttachProjectRoot(request, created.id, secondary, "other");
    await gotoShell(page);
    await activateProject(page, project.id);
    const sessionId = await settledChatSessionId(page);
    const assistantId = crypto.randomUUID();
    const callId = crypto.randomUUID();
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
      { id: assistantId, role: "assistant", content: "", ord: 1, created_at: "2026-09-15T12:00:00Z",
        tool_calls: [{ id: callId, name: "summarize", args: { path: target } }] },
      { id: crypto.randomUUID(), role: "tool", content: "Directory summary", ord: 2, created_at: "2026-09-15T12:00:01Z",
        tool_result: { tool: "summarize", tool_call_id: callId,
          assistant_message_id: assistantId, tool_args: { path: target },
          content: "Directory summary", outcome: "completed" } },
    ]));
    const stream = liveChatStage(page);
    const activity = stream.getByTestId("activity-span-card").first();
    await expect(activity).toBeVisible();
    if (await activity.getAttribute("data-expanded") !== "true") await activity.locator(":scope > .den-transcript-disclosure-header").click();
    const card = stream.locator(`[data-testid="tool-part-card"][data-tool-call-id="${callId}"]`);
    await expect(card).toBeVisible();
    if (await card.getAttribute("data-expanded") !== "true") await card.locator(":scope > .den-transcript-disclosure-header").click();
    const link = card.getByTestId("source-path-link").first();
    await expect(link).toHaveText(target);
    await link.click();
    const rootId = target.startsWith("@")
      ? project.roots.find((root) => root.label === "other")!.id
      : created.roots[0]!.id;
    const relative = target === "." ? "." : "src";
    const folder = page.locator(`.den-files-tree__label--dir[data-root="${rootId}"][data-path="${relative}"][aria-current="true"]`).first();
    await expect(folder).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("files-tab")).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath("folder-navigation.png") });
  });
}

// One command card per program call: plain sections inside the existing opening.
const programId = "6f7d0a8e-1b2c-4d3e-8f90-112233445566";

const program: CommandProgramReceipt = {
  id: programId,
  parent_receipt_id: "0d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6",
  language_version: 1,
  catalog_digest: "catalog",
  source_digest: "source",
  source_ref: "output:exact-program",
  state: "failed",
  status_code: 1,
  stop_code: "HTTP_REQUEST_FAILED",
  detail_ref: "output:program-detail",
  progress_state: "unchanged",
  started_at: "2026-10-20T10:00:00Z",
  settled_at: "2026-10-20T10:00:02Z",
};

const receiptPage: CommandProgramReceiptPage = {
  program,
  executions: [
    { id: "e1", program_id: programId, node_id: "n1", parent_execution_id: "root", owner: "files", lifecycle: "synchronous", definition_digest: "d", arguments_digest: "a", arguments_ref: "output:a1", native_command: "cat", effect_classes: ["read_only"], command: "cat input.json", state: "completed", owner_started: true, owner_invoked: true, status_code: 0, complete: true, publication_eligible: true, output_publishable: true, effect_certainty: "none", detail_ref: "output:d1" },
    { id: "e2", program_id: programId, node_id: "n2", parent_execution_id: "root", owner: "files", lifecycle: "synchronous", definition_digest: "d", arguments_digest: "a", arguments_ref: "output:a2", native_command: "write", effect_classes: ["mutation"], command: "cat > report.txt <<EOF ...", state: "completed", owner_started: true, owner_invoked: true, status_code: 0, complete: true, publication_eligible: true, output_publishable: true, effect_certainty: "completed", accepted_revision: "r7", published_revision: "r7", detail_ref: "output:d2" },
    { id: "e3", program_id: programId, node_id: "n3", parent_execution_id: "root", owner: "http", lifecycle: "synchronous", definition_digest: "d", arguments_digest: "a", arguments_ref: "output:a3", native_command: "curl", effect_classes: ["network_egress"], command: "curl -X POST https://example.test/upload", state: "failed", owner_started: true, owner_invoked: true, status_code: 1, stop_code: "HTTP_REQUEST_FAILED", complete: false, publication_eligible: false, output_publishable: false, effect_certainty: "unknown", detail_ref: "output:d3" },
  ],
  nodes: [
    { id: "n1", source_unit_id: "submitted", start_byte: 0, end_byte: 14, line: 1, column: 1, state: "reached" },
    { id: "n2", source_unit_id: "submitted", start_byte: 15, end_byte: 40, line: 2, column: 1, state: "reached" },
    { id: "n3", source_unit_id: "submitted", start_byte: 60, end_byte: 99, line: 5, column: 1, state: "reached" },
    { id: "n4", source_unit_id: "submitted", start_byte: 100, end_byte: 120, line: 6, column: 1, state: "not_started" },
  ],
  executions_omitted: 0,
  nodes_omitted: 0,
};

webE2e.describe("command activity details", () => {
  webE2e("opens retained receipts through compact activity details", async ({ page, request }) => {
    await page.route(`**/v1/sessions/*/program-receipts/${programId}*`, (route) =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(receiptPage) }));
    await bootstrapChatSession(page, request);
    const sessionId = ((await liveChatStage(page).getByTestId("chat-stream").getAttribute("data-session-id")) ?? "").trim();
    expect(sessionId).toBeTruthy();
    const assistantMessageId = crypto.randomUUID();
    const source = "cat input.json\ncat > report.txt <<EOF\nquarterly totals\nEOF\ncurl -X POST https://example.test/upload\necho done";
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
      { id: crypto.randomUUID(), role: "user", content: "Write the report and upload it.", created_at: "2026-10-20T10:00:00Z", seq: 1, ord: 1 },
      { id: assistantMessageId, role: "assistant", content: "", status: "complete", created_at: "2026-10-20T10:00:01Z", seq: 2, ord: 2, tool_calls: [{ id: "call_command_1", name: "command", args: { command: source } }] },
      {
        id: crypto.randomUUID(), role: "tool", content: "Code: HTTP_REQUEST_FAILED", created_at: "2026-10-20T10:00:02Z", seq: 3, ord: 3,
        tool_result: {
          tool_call_id: "call_command_1", assistant_message_id: assistantMessageId, tool: "command", tool_args: { command: source }, content: "Code: HTTP_REQUEST_FAILED", outcome: "rejected",
          command_program: program,
          feedback: [{ code: "HTTP_REQUEST_FAILED", details: { native_operation: "http_request", operation_source_line: 5, operation_source_column: 1 } }],
        },
      },
    ]));
    const stream = liveChatStage(page).locator(`[data-testid="chat-stream"][data-session-id="${sessionId}"]`);
    await stream.getByTestId("activity-span-card").locator(":scope > .den-transcript-disclosure-header").click();
    const card = stream.getByTestId("tool-part-card");
    await expect(card).toHaveCount(1);
    if (await card.getAttribute("data-expanded") !== "true") await card.locator(":scope > .den-transcript-disclosure-header").click();
    const body = card.getByTestId("command-activity-details");
    await expect(body.locator(".den-command-activity-step")).toHaveCount(3);
    await expect(body).toContainText("Partial effects; unknown outcome");
    await expect(body).toContainText("HTTP_REQUEST_FAILED");
    await expect(body).toContainText("Full original command");
    await expect(body).toContainText("Not started");
    await expect(body.locator("details")).toHaveCount(0);
  });
});

modelIndependentWebE2e("command activities contain long text at narrow card widths", async ({ page, request }, testInfo) => {
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
