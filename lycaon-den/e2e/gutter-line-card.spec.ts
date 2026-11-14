import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiJson, apiPostPrompt, modelIndependentWebE2e as test, openProjectFilesFixture, waitForSessionReady, webE2e } from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

test("line facts show a hint on rest and open the card only on activation", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, { prefix: "line-facts", name: "Line facts",
    seed: root => writeFileSync(path.join(root, "load.go"), ["package config", "", "func load() {", "  read()", "  parse()", "}", ...Array.from({ length: 80 }, (_, i) => `// line ${i + 7}`)].join("\n")) });
  await page.getByTestId("files-tree-file").filter({ hasText: "load.go" }).click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true });
  const content = editor.locator(".cm-content");
  await expect(content).toBeEditable({ timeout: 15000 });
  await page.evaluate(async ({ projectId, rootId }) => {
    const modulePath = "/src/files/components/agent-presence-store.ts";
    const { applyAgentPresenceSnapshot } = await import(/* @vite-ignore */ modulePath);
    const target = { root_id: rootId, path: "load.go" };
    const read = (id: string, sequence: number, whole = false) => ({ ...target, id, sequence, tool_call_id: id, tool: "read", stale: false,
      extent: whole ? "whole_file" : "range", ranges: whole ? [] : [{ start_line: 3, end_line: 5 }] });
    applyAgentPresenceSnapshot({ project_id: projectId, revision: 100000, sessions: [
      { session_id: "reader", title: "Inspect config", turn: 1, activities: [], intents: [], worker_drafts: [], reads: [read("whole", 1, true), read("range", 2)] },
      { session_id: "editor", title: "Config defaults", turn: 2, activities: [], reads: [read("earlier", 1)], worker_drafts: [],
        intents: [{ ...target, id: "pending", tool_call_id: "pending", tool: "replace_lines", operation: "edit", state: "awaiting_approval", extent: "range", ranges: [{ start_line: 4, end_line: 5 }] }] },
    ] });
  }, { projectId: project.id, rootId: project.roots[0]!.id });

  const handle = editor.locator('.files-line-gutter__facts[data-line="4"]');
  const next = editor.locator('.files-line-gutter__facts[data-line="5"]');
  const foldableHandle = editor.locator('.files-line-gutter__facts[data-line="3"]');
  const foldNumber = editor.locator('[data-testid="files-line-gutter-fold"][data-line="3"]');
  const card = page.getByTestId("files-line-facts");
  const tooltip = page.getByTestId("den-tooltip");

  await expect(handle).toHaveAttribute("aria-label", "Line 4: 4 facts");

  await content.click({ position: { x: 24, y: 8 } });
  await page.keyboard.press("Shift+ArrowRight");
  const readFocusState = () => content.evaluate(node => ({
    focused: document.activeElement === node,
    selectedText: document.getSelection()?.toString() ?? "",
    scroll: node.closest(".cm-scroller")?.scrollTop ?? 0,
  }));
  const before = await readFocusState();
  expect(before.focused).toBe(true);
  expect(before.selectedText.length).toBeGreaterThan(0);

  await handle.hover();
  await expect(tooltip).toBeVisible();
  await expect(card).toHaveCount(0);

  await handle.click();
  await expect(card).toBeVisible();
  await expect(tooltip).toHaveCount(0);
  await expect(card).toHaveAttribute("data-side", "right");
  await expect(handle).toHaveCSS("box-shadow", "none");
  await expect(card.getByRole("button")).toHaveCount(4);
  expect(await readFocusState()).toEqual(before);

  await next.hover();
  await expect(tooltip).toBeVisible();
  await expect(card).toBeVisible();
  await expect(card).toHaveAccessibleName("Line 4 · load.go");

  await page.keyboard.press("Escape");
  await expect(card).toHaveCount(0);
  expect(await readFocusState()).toEqual(before);

  await handle.click();
  await expect(card).toBeVisible();
  await handle.click();
  await expect(card).toHaveCount(0);

  await foldableHandle.click({ position: { x: 6, y: 8 } });
  await expect(card).toBeVisible();
  await expect(card).toHaveAccessibleName("Line 3 · load.go");
  await foldNumber.click();
  await expect(card).toHaveCount(0);
  await expect(editor.locator(".cm-foldPlaceholder")).toHaveCount(1);
  await foldNumber.click();
  await expect(editor.locator(".cm-foldPlaceholder")).toHaveCount(0);

  await content.hover({ position: { x: 80, y: 40 } });
  await handle.focus();
  await expect(handle).toBeFocused();
  await expect(tooltip).toBeVisible();
  await expect(card).toHaveCount(0);

  await page.keyboard.press("ArrowRight");
  await expect(card).toBeVisible();
  await expect(card.getByRole("button").first()).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(card.getByRole("button").nth(1)).toBeFocused();
  await page.screenshot({ path: testInfo.outputPath("line-facts.png") });
  await page.keyboard.press("Escape");
  await expect(card).toHaveCount(0);
  await expect(handle).toBeFocused();

  await page.keyboard.press("ArrowDown");
  await expect(next).toBeFocused();
  await expect(card).toHaveCount(0);

  await handle.click();
  await expect(card).toBeVisible();
  await editor.locator(".cm-scroller").evaluate(node => { node.scrollTop = 200; });
  await expect(card).toHaveCount(0);
});

webE2e("a live read opens its exact tool and returns to the originating file line", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, { prefix: "line-facts-return", name: "Line facts return",
    seed: root => writeFileSync(path.join(root, "shared.txt"), "First line\nRead this line\nAnd this line\nLast line\n") });
  const session = page.locator('[data-testid="focused-session-list"] [data-session-id][aria-current="true"]');
  await expect(session).toHaveCount(1);
  const sessionId = (await session.getAttribute("data-session-id"))!;
  await page.getByTestId("files-tree-file").filter({ hasText: "shared.txt" }).click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(editor.locator(".cm-content")).toBeEditable();
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const prompt = await apiPostPrompt(request, sessionId, { text: "Read lines 2 and 3 of shared.txt." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    const readId = await driver.invoke("command", { command: "sed -n '2,3p' shared.txt" });
    await driver.completed(readId);
    const handle = editor.locator('.files-line-gutter__facts[data-line="3"]');
    await expect(handle).toBeVisible();
    await handle.click();
    const card = page.getByTestId("files-line-facts");
    await card.getByRole("button").first().click();
    await expect(page.locator(`[data-tool-call-id="${readId}"].den-reveal-flash`)).toBeVisible();
    const back = page.getByTestId("line-facts-back").filter({ visible: true }).first();
    await expect(back).toContainText("shared.txt:3");
    await back.click();
    await expect(editor).toBeVisible();
    await expect(page.getByText("Ln 3, Col 1", { exact: true })).toBeVisible();
    await expect(page.getByTestId("line-facts-back")).toHaveCount(0);
    await expect(editor.locator(".cm-content")).toContainText("Read this line");
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Read verification complete." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Read verification complete." });
  }
});

webE2e("Review expands and focuses the exact pending approval", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, { prefix: "line-facts-approval", name: "Line facts approval",
    seed: root => writeFileSync(path.join(root, "AGENTS.md"), "Use clear names.\nKeep examples short.\n") });
  const session = page.locator('[data-testid="focused-session-list"] [data-session-id][aria-current="true"]');
  await expect(session).toHaveCount(1);
  const sessionId = (await session.getAttribute("data-session-id"))!;
  await waitForSessionReady(request, sessionId);
  await page.getByTestId("files-tree-file").filter({ hasText: "AGENTS.md" }).click();
  await page.setViewportSize({ width: 1800, height: 1000 });
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();
  await page.getByTestId("layout-dock-btn").click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(editor.locator(".cm-content")).toBeEditable();
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const prompt = await apiPostPrompt(request, sessionId, { text: "Replace short with clear in AGENTS.md." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    await driver.completed(await driver.invoke("update_progress", { content: "## Progress\n- [ ] Update the example instruction\n" }));
    await driver.completed(await driver.invoke("command", { command: "cat AGENTS.md" }));
    await driver.invoke("command", { command: "edit --path AGENTS.md --old-string 'Keep examples short.' --new-string 'Keep examples clear.'" });
    const approval = page.getByTestId("tool-approval-card").filter({ visible: true });
    await expect(approval).toBeVisible();
    const checkpointId = await approval.getAttribute("data-checkpoint-id");
    await approval.getByTestId("approval-card-minimize").click();
    await expect(approval).toHaveAttribute("data-minimized", "");
    const hideConversation = page.getByRole("button", { name: "Hide conversation", exact: true });
    await expect(hideConversation).toBeVisible();
    await hideConversation.click();
    await editor.locator('.files-line-gutter__facts[data-line="2"]').click();
    await page.getByTestId("files-line-facts").getByRole("button").first().click();
    await expect(approval).toHaveAttribute("data-checkpoint-id", checkpointId!);
    await expect(approval).not.toHaveAttribute("data-minimized");
    await expect(approval).toBeFocused();
    await page.getByTestId("line-facts-back").filter({ visible: true }).first().click();
    await expect(page.getByText("Ln 2, Col 1", { exact: true }).filter({ visible: true })).toBeVisible();
    await expect(editor.locator(".cm-content")).toContainText("Keep examples short.");
    await approval.getByTestId("approval-no").click();
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Approval verification complete." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Approval verification complete." });
  }
});
