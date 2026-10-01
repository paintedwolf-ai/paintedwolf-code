import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { activateProject, gotoShell, modelIndependentWebE2e, openProjectFilesFixture, seedAppState } from "./helpers.ts";

modelIndependentWebE2e.use({ trace: "retain-on-failure" });

const initial = "First line\nMiddle line\nLast line\n";
const editor = (page: Page) => page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");

async function openFile(page: Page) {
  await page.getByTestId("files-tree-file").filter({ hasText: "shared.txt" }).click();
  await expect(editor(page)).toBeEditable({ timeout: 15_000 });
}

async function insert(page: Page, position: "start" | "end", text: string) {
  await editor(page).click();
  await page.keyboard.press(position === "start" ? "ControlOrMeta+Home" : "ControlOrMeta+End");
  await page.keyboard.insertText(text);
}

modelIndependentWebE2e("collaborating windows converge, undo independently, save and reopen", async ({ page, context, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-collaboration", name: "Editor collaboration",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  const peer = await context.newPage();
  try {
    await seedAppState(peer);
    await gotoShell(peer);
    await activateProject(peer, project.id);
    await peer.getByTestId("project-files-entry").click();
    await openFile(peer);
    await insert(page, "start", "Person A 🐺\n");
    await expect(editor(peer)).toContainText("Person A 🐺");
    await insert(peer, "end", "Person B 世界\n");
    await expect(editor(page)).toContainText("Person B 世界");
    await editor(page).press("ControlOrMeta+z");
    await expect(editor(page)).not.toContainText("Person A");
    await expect(editor(peer)).not.toContainText("Person A");
    await expect(editor(page)).toContainText("Person B 世界");
    await editor(page).press("ControlOrMeta+Shift+z");
    await expect(editor(peer)).toContainText("Person A 🐺");
    await page.getByTestId("files-editor-save").click();
    const saved = "Person A 🐺\n" + initial + "Person B 世界\n";
    await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8")).toBe(saved);
    await expect(peer.getByTestId("files-editor-save")).toHaveCount(0);
    await page.reload();
    await page.getByTestId("project-files-entry").click();
    await openFile(page);
    await expect(editor(page)).toContainText("Person A 🐺");
    await expect(editor(page)).toContainText("Person B 世界");
    await editor(page).press("ControlOrMeta+z");
    await expect(editor(page)).not.toContainText("Person A 🐺");
    await expect(editor(peer)).not.toContainText("Person A 🐺");
    await expect(editor(page)).toContainText("Person B 世界");
    await editor(page).press("ControlOrMeta+Shift+z");
    await expect(editor(peer)).toContainText("Person A 🐺");
  } finally { await peer.close(); }
});

modelIndependentWebE2e("outside writes merge with unsaved typing and survive local undo", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-disk-merge", name: "Editor disk merge",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  await insert(page, "start", "Unsaved person 🐺\n");
  writeFileSync(path.join(root, "shared.txt"), initial.replace("Middle line", "Outside 世界"));
  await expect(editor(page)).toContainText("Outside 世界", { timeout: 15_000 });
  await expect(editor(page)).toContainText("Unsaved person 🐺");
  await editor(page).press("ControlOrMeta+z");
  await expect(editor(page)).not.toContainText("Unsaved person");
  await expect(editor(page)).toContainText("Outside 世界");
  await editor(page).press("ControlOrMeta+Shift+z");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
    .toBe("Unsaved person 🐺\n" + initial.replace("Middle line", "Outside 世界"));
});

modelIndependentWebE2e("preserved updates survive reload after a lost host acknowledgement", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-lost-reply", name: "Editor lost reply",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  let accepted = 0;
  await page.route("**/editor-documents/*/updates", async (route) => {
    const response = await route.fetch();
    expect(response.ok(), await response.text()).toBe(true);
    accepted++;
    await route.abort("failed");
  });
  await insert(page, "end", "Exactly once 🐺\n");
  await expect.poll(() => accepted).toBeGreaterThan(0);
  await page.reload();
  await page.unroute("**/editor-documents/*/updates");
  await page.getByTestId("project-files-entry").click();
  await openFile(page);
  await expect(editor(page)).toContainText("Exactly once 🐺");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
    .toBe(initial + "Exactly once 🐺\n");
  await editor(page).press("ControlOrMeta+z");
  await expect(editor(page)).not.toContainText("Exactly once 🐺");
  await editor(page).press("ControlOrMeta+Shift+z");
  await expect(editor(page)).toContainText("Exactly once 🐺");
});

modelIndependentWebE2e("undelivered typing merges with outside changes after reload", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-reconnect", name: "Editor reconnect",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  let attempts = 0;
  await page.route("**/editor-documents/*/updates", async (route) => { attempts++; await route.abort("failed"); });
  await insert(page, "start", "Offline person\n");
  await expect.poll(() => attempts).toBeGreaterThan(0);
  writeFileSync(path.join(root, "shared.txt"), initial.replace("Last line", "Outside while offline"));
  await expect(editor(page)).toContainText("Outside while offline", { timeout: 15_000 });
  await expect(editor(page)).toContainText("Offline person");
  await page.reload();
  await page.unroute("**/editor-documents/*/updates");
  await page.getByTestId("project-files-entry").click();
  await openFile(page);
  await expect(editor(page)).toContainText("Offline person");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
    .toBe("Offline person\n" + initial.replace("Last line", "Outside while offline"));
  await editor(page).press("ControlOrMeta+z");
  await expect(editor(page)).not.toContainText("Offline person");
  await expect(editor(page)).toContainText("Outside while offline");
  await editor(page).press("ControlOrMeta+Shift+z");
  await expect(editor(page)).toContainText("Offline person");
  await expect(editor(page)).toContainText("Outside while offline");
});

for (const boundary of [{ label: "requested", route: "snapshots" }, { label: "pinned", route: "save*" }]) {
  modelIndependentWebE2e(`typing after a save is ${boundary.label} stays editable and dirty`, async ({ page, request }) => {
    const { root } = await openProjectFilesFixture(page, request, {
      prefix: "editor-save-race", name: "Editor save race",
      seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
    });
    await openFile(page);
    await insert(page, "start", "Pinned change\n");
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    let saving = false;
    const routePattern = `**/editor-documents/*/${boundary.route}`;
    await page.route(routePattern, async (route) => {
      saving = true;
      await held;
      await route.continue();
    });
    try {
      await page.getByTestId("files-editor-save").click();
      await expect.poll(() => saving).toBe(true);
      await expect(editor(page)).toBeEditable();
      await insert(page, "end", "Later typing\n");
      await editor(page).evaluate((element) => {
        element.setAttribute("data-save-blurs", "0");
        element.addEventListener("blur", () => element.setAttribute("data-save-blurs",
          String(Number(element.getAttribute("data-save-blurs")) + 1)));
      });
    } finally { release(); }
    await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8")).toBe("Pinned change\n" + initial);
    await expect(editor(page)).toContainText("Later typing");
    await expect(editor(page)).toBeFocused();
    await expect(editor(page)).toHaveAttribute("data-save-blurs", "0");
    await expect(page.getByTestId("files-editor-save")).toBeEnabled();
    await page.unroute(routePattern);
    await page.getByTestId("files-editor-save").click();
    await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
      .toBe("Pinned change\n" + initial + "Later typing\n");
  });
}

modelIndependentWebE2e("discard returns focus to the editor and can be undone and redone", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-discard", name: "Editor discard",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  await insert(page, "end", "Unsaved typing\n");
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await page.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(editor(page).locator(".cm-line")).toHaveText(initial.split("\n"));
  await expect(editor(page)).toBeFocused();
  await page.keyboard.press("ControlOrMeta+z");
  await expect(editor(page)).toContainText("Unsaved typing");
  await page.keyboard.press("ControlOrMeta+Shift+z");
  await expect(editor(page).locator(".cm-line")).toHaveText(initial.split("\n"));
});

modelIndependentWebE2e("reload restores the caret with the unsaved collaborative draft", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-caret", name: "Editor caret",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), initial),
  });
  await openFile(page);
  await editor(page).press("ControlOrMeta+Home");
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  const delivered = page.waitForResponse((response) => response.url().endsWith("/updates") && response.ok());
  await page.keyboard.insertText("middle");
  await delivered;
  await page.reload();
  await page.getByTestId("project-files-entry").click();
  await openFile(page);
  await editor(page).focus();
  await page.keyboard.insertText("|");
  await expect(editor(page).locator(".cm-line")).toHaveText(initial.replace("First", "Fimiddle|rst").split("\n"));
});
