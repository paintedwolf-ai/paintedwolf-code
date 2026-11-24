import { existsSync, mkdtempSync, readFileSync, realpathSync, rmSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect, type APIRequestContext } from "@playwright/test";
import type { CheckpointListResponse, SessionTranscriptPage, SourceWalkResponse, ToolResult } from "../../src/api/types.ts";
import {
  apiJson, apiPostPrompt, bootstrapActiveProject, e2eTempDir, e2eUniqueLabel,
  liveChatStage, settledChatSessionId, waitForChatComposerReady, webE2e,
} from "../helpers.ts";
import { manualHarnessTools } from "../manual-harness-tools.ts";

function quotePath(value: string): string {
  return "'" + value.replaceAll("'", "'\\''") + "'";
}

/** Settles one manual command, answering each of its approval cards with the recommended option. */
async function settle(request: APIRequestContext, sessionId: string, callId: string): Promise<ToolResult> {
  const approved = new Set<string>();
  let result: ToolResult | undefined;
  await expect.poll(async () => {
    const transcript = await apiJson<SessionTranscriptPage>(request, "GET", `/v1/sessions/${sessionId}/messages`);
    result = transcript.messages.find((message) => message.tool_result?.tool_call_id === callId)?.tool_result;
    if (result) return true;
    const pending = await apiJson<CheckpointListResponse>(request, "GET", `/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`);
    for (const checkpoint of pending.checkpoints) {
      const plan = checkpoint.tool_approval?.plan;
      if (checkpoint.status !== "pending" || approved.has(checkpoint.id) || checkpoint.tool_approval?.tool_call_id !== callId || !plan?.recommended_option_id) continue;
      await apiJson(request, "POST", `/v1/sessions/${sessionId}/checkpoints/${checkpoint.id}`, {
        kind: "tool_approval", action: "approve", option_id: plan.recommended_option_id,
      });
      approved.add(checkpoint.id);
    }
    return false;
  }, { timeout: 60_000 }).toBe(true);
  expect(result?.outcome, result?.content).toBe("completed");
  return result!;
}

async function manualSession(page: Parameters<typeof bootstrapActiveProject>[0], request: APIRequestContext, label: string, prompt: string) {
  const root = e2eTempDir(request, `${label}-project`);
  const project = await bootstrapActiveProject(page, request, root, e2eUniqueLabel(label));
  await waitForChatComposerReady(page);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  const sent = await apiPostPrompt(request, sessionId, { text: prompt });
  expect(sent.ok(), await sent.text()).toBe(true);
  const driver = manualHarnessTools(request, sessionId);
  await driver.loadTools("command");
  return { project, sessionId, driver };
}

webE2e("home and shared temp redirects reach Files and Review", async ({ page, request }) => {
  const folder = realpathSync(process.env.LYCAON_E2E_DOCKER_STACK === "1"
    ? e2eTempDir(request, "bugbash-home")
    : mkdtempSync(path.join(process.env.PW_TEST_HOST_HOME ?? homedir(), "pw-e2e-outside-")));
  const home = path.join(folder, "a.txt");
  const shared = path.join(realpathSync("/tmp"), `pw-e2e-${path.basename(folder)}.txt`);
  try {
    const { project, sessionId, driver } = await manualSession(page, request, "Bug bash redirects", "Create a test file in my home dir, then one in /tmp.");
    try {
      const write = await driver.invoke("command", {
        command: `printf 'first\\n' > ${quotePath(home)} && cat -n ${quotePath(home)}`,
        capability_request: { write_root: home },
      });
      const written = await settle(request, sessionId, write);
      expect(readFileSync(home, "utf8")).toBe("first\n");
      expect(written.content).toContain("first");
      const temp = await driver.invoke("command", { command: `printf 'second\\n' > ${quotePath(shared)}` });
      await settle(request, sessionId, temp);
      expect(readFileSync(shared, "utf8")).toBe("second\n");
      await settle(request, sessionId, await driver.invoke("command", { command: `rm ${quotePath(shared)}` }));
      expect(existsSync(shared)).toBe(false);
      const transcript = await apiJson<SessionTranscriptPage>(request, "GET", `/v1/sessions/${sessionId}/messages`);
      expect(transcript.messages.find((message) => message.tool_result?.tool_call_id === write)?.source_context?.locations)
        .toContainEqual(expect.objectContaining({ path: home.slice(1), file_id: expect.any(String) }));
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Both files are done." });
    }

    const review = await apiJson<SourceWalkResponse>(request, "GET",
      `/v1/projects/${project.id}/source/walk?baseline=${encodeURIComponent(`session:${sessionId}`)}&limit=100`);
    const paths = review.files.map((entry) => entry.path);
    expect(paths).toContain(home.slice(1));
    expect(paths).toContain(shared.slice(1));

    await page.getByTestId("project-files-entry").click();
    const tree = page.getByTestId("files-tree");
    await expect(tree.getByTestId("files-tree-outside-section")).toHaveText("Outside project");
    await expect(tree.getByTestId("files-tree-dir").filter({ hasText: path.basename(folder) })).toBeVisible();

    await liveChatStage(page).getByTestId("diff-group").first().getByTestId("diff-group-files").click();
    const card = liveChatStage(page).getByTestId("file-edit-diff").filter({ hasText: home }).first();
    const opened = page.waitForResponse((response) => new URL(response.url()).pathname === `/v1/projects/${project.id}/editor-documents` &&
      response.request().method() === "POST" && Boolean(response.request().postDataJSON().file_id));
    await card.getByTestId("source-path-link").click();
    expect((await opened).ok()).toBe(true);
    await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toContainText("first");
  } finally {
    rmSync(folder, { recursive: true, force: true });
    rmSync(shared, { force: true });
  }
});

webE2e("@scratch work is one editable location without program temp", async ({ page, request }) => {
  const { sessionId, driver } = await manualSession(page, request, "Bug bash scratch", "Draft a note in scratch.");
  let draft = "";
  try {
    const write = await driver.invoke("command", {
      command: "printf 'draft\\n' > @scratch/draft.md && external -- sh -c 'for i in 1 2 3; do : > \"$TMPDIR/junk$i\"; done'",
    });
    await settle(request, sessionId, write);
    const transcript = await apiJson<SessionTranscriptPage>(request, "GET", `/v1/sessions/${sessionId}/messages`);
    const location = transcript.messages.find((message) => message.tool_result?.tool_call_id === write)?.source_context?.locations
      ?.find((entry) => entry.path.endsWith("/draft.md"));
    expect(location?.file_id).toBeTruthy();
    draft = `/${location!.path}`;
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Drafted." });
  }

  await page.getByTestId("project-files-entry").click();
  const tree = page.getByTestId("files-tree");
  await expect(tree.getByTestId("files-tree-outside-section")).toBeVisible();
  const scratch = tree.getByTestId("files-tree-dir").filter({ hasText: "@scratch" });
  await expect(scratch).toBeVisible();
  const toggle = scratch.getByTestId("files-tree-dir-toggle");
  if (await toggle.getAttribute("aria-expanded") !== "true") await toggle.click();
  const file = tree.getByTestId("files-tree-file").filter({ hasText: "draft.md" });
  await expect(file).toBeVisible();
  await expect(tree.getByTestId("files-tree-dir").filter({ hasText: /^\.tmp$/ })).toHaveCount(0);

  await file.dblclick();
  const content = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(content).toContainText("draft");
  await content.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("edited draft");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(draft, "utf8")).toBe("edited draft");
});
