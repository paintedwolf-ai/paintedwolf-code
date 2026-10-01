import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import {
  activateProject, apiAttachProjectRoot, apiCreateProjectWithRoot,
  apiSeedSessionTranscript, e2eTempDir, gotoShell, liveChatStage,
  modelIndependentWebE2e, settledChatSessionId, transcriptMessages,
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
    if (await activity.getAttribute("open") === null) await activity.locator(":scope > summary").click();
    const card = stream.locator(`[data-testid="tool-part-card"][data-tool-call-id="${callId}"]`);
    await expect(card).toBeVisible();
    if (await card.getAttribute("open") === null) await card.locator(":scope > summary").click();
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
