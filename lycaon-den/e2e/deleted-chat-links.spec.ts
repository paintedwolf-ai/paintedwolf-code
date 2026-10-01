import { randomUUID } from "node:crypto";
import { expect } from "@playwright/test";
import {
  apiConfig, apiSeedSessionTranscript, bootstrapChatSession,
  modelIndependentWebE2e, settledChatSessionId, transcriptMessages,
} from "./helpers.ts";
import type { ProjectSourceReadResponse } from "../src/api/types.ts";

modelIndependentWebE2e.use({ browserName: "webkit" });

modelIndependentWebE2e("chat links open known deletions and follow recreated paths", async ({ page, request }, testInfo) => {
  const project = await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const base = `${apiUrl}/v1/projects/${project.id}/source`;
  const rootId = project.roots[0]!.id;
  const path = `retained-${randomUUID()}.ts`;
  const unseenPath = `unseen-${randomUUID()}.ts`;
  const create = async (path: string, content: string) => {
    const created = await request.post(base, { headers, data: {
      operation_id: randomUUID(), root_id: rootId, path, kind: "file",
    } });
    expect(created.ok(), await created.text()).toBeTruthy();
    const read = await request.get(base, { headers, params: { root_id: rootId, path } });
    expect(read.ok(), await read.text()).toBeTruthy();
    const source = await read.json() as ProjectSourceReadResponse;
    const saved = await request.put(base, { headers, data: {
      operation_id: randomUUID(), root_id: rootId, path,
      encoding: source.encoding, base_sha256: source.sha256, content,
    } });
    expect(saved.ok(), await saved.text()).toBeTruthy();
  };
  await create(path, "// Retained before deletion\nexport const original = true;\n");
  await create(unseenPath, "// Existing file outside the agent's source context\n");
  const deleted = await request.delete(base, { headers, params: {
    operation_id: randomUUID(), root_id: rootId, path,
  } });
  expect(deleted.ok(), await deleted.text()).toBeTruthy();
  const messageId = randomUUID();
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
    id: messageId, role: "assistant", content: `See \`${path}:2\`. The unrelated \`${unseenPath}\` stays plain.`,
    source_context: { locations: [{ project_id: project.id, root_id: rootId, path, entry_kind: "file" }], truncated: false },
    created_at: new Date().toISOString(), seq: 1, ord: 1,
  }]));
  const prose = page.locator(`[data-msg-id="${messageId}"] .assistant-prose`);
  const link = prose.getByRole("button", { name: `${path}:2`, exact: true });
  await expect(link).toBeVisible();
  await expect(prose.getByRole("button", { name: unseenPath, exact: true })).toHaveCount(0);
  await link.click();
  const document = page.getByTestId("file-deletion-document").filter({ visible: true });
  await expect(document).toContainText("Retained before deletion");
  await expect(page.getByTestId("files-editor-restore-deleted").filter({ visible: true })).toBeVisible();
  // The retained contents read as the deletion they are, as on every other reader surface.
  await expect(document.locator(".cm-den-reader-delete")).toHaveCount(2);
  await expect(document.locator(".cm-content")).toHaveAttribute("contenteditable", "false");
  const tab = page.getByTestId("files-tab").filter({ hasText: path.replace(/\.ts$/, "") });
  await expect(tab.locator('.den-files-tab__name[data-file-change="deleted"]')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("deleted-chat-link.png") });

  await create(path, "// Replacement at the same path\nexport const replacement = true;\n");
  await page.locator(`[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`).click();
  await expect(link).toBeVisible();
  await link.click();
  await expect(page.getByTestId("file-deletion-document").filter({ visible: true })).toHaveCount(0);
  await expect(page.locator('.project-files-view .cm-content[contenteditable="true"]').filter({ visible: true })).toContainText("Replacement at the same path");
  await expect(tab.locator('.den-files-tab__name[data-file-change="deleted"]')).toHaveCount(0);
});
