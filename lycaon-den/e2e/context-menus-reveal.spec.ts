import { expect, type Locator, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { bootstrapChatSession, openProjectFilesFixture, webE2e } from "./helpers.ts";

async function openContextMenu(page: Page, target: Locator) {
  await expect(target).toBeVisible({ timeout: 30_000 });
  if ((await page.getByTestId("context-menu").count()) > 0) {
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("context-menu")).toHaveCount(0);
  }
  const box = await target.boundingBox();
  expect(box).toBeTruthy();
  await page.mouse.click(box!.x + Math.min(20, box!.width / 2), box!.y + 8, {
    button: "right",
  });
  await expect(page.getByTestId("context-menu")).toBeVisible({
    timeout: 10_000,
  });
}

/**
 * Right-click opens the shared ContextMenu on both surfaces; FocusedSessionList unit tests
 * cover the full session item list, and file manager reveal needs the desktop app.
 */
webE2e.describe("context menus + reveal", () => {
  webE2e("file surfaces share destinations without adding an editor toolbar control", async ({ page, request }) => {
    await openProjectFilesFixture(page, request, {
      prefix: "context-destinations", name: "Context destinations",
      seed(root) { writeFileSync(path.join(root, "notes.txt"), "First line\nSecond line\n"); },
    });
    const row = page.locator('[data-files-ctx="tree-row"][data-path="notes.txt"]').first();
    await row.click();
    const editor = page.getByTestId("files-editor");
    await expect(editor.locator(".cm-content")).toBeVisible();
    await expect(editor.locator(".den-files-toolbar").getByTestId("open-in-button")).toHaveCount(0);
    const crumb = page.locator('[data-files-ctx="breadcrumb"][data-path="notes.txt"]');
    for (const target of [row, page.locator('[data-files-ctx="tab"][data-path="notes.txt"]'), crumb, editor.locator(".cm-content")]) {
      await openContextMenu(page, target);
      await page.getByTestId("open-in-menu").click();
      for (const destination of ["file-manager", "editor", "browser", "default-application"]) {
        const item = page.getByTestId(`open-in-${destination}`);
        await expect(item).toBeVisible();
        await expect(item).toBeDisabled();
        await expect(item).toHaveAccessibleDescription("Open this path from the desktop app.");
      }
      await page.keyboard.press("Escape");
      if (await page.getByTestId("context-menu").count()) await page.keyboard.press("Escape");
      await expect(page.getByTestId("context-menu")).toHaveCount(0);
    }
    await crumb.focus();
    await page.keyboard.press("Shift+F10");
    await expect(page.getByTestId("open-in-menu")).toBeVisible();
  });

  webE2e("folder root Reveal/Copy menu", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    await expect(page.getByTestId("project-folder-zone")).toBeVisible({
      timeout: 30_000,
    });
    await page.getByTestId("project-folder-summary").click();
    const folderRow = page
      .getByTestId("project-folder-zone")
      .locator("[data-testid^=project-folder-row-]")
      .first();
    await openContextMenu(page, folderRow);
    const menu = page.getByTestId("context-menu");
    await expect(menu.getByTestId("open-in-menu")).toBeVisible();
    await expect(menu.getByTestId("path-menu-copy-path")).toBeVisible();
    await menu.getByTestId("open-in-menu").click();
    await expect(page.getByTestId("open-in-file-manager")).toBeVisible();
    await expect(page.getByTestId("open-in-editor")).toBeVisible();
    await expect(page.getByTestId("open-in-browser")).toHaveCount(0);
  });

  webE2e("session list opens shared ContextMenu", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    const sessionList = page.getByTestId("focused-session-list");
    await expect(sessionList).toBeVisible({ timeout: 30_000 });
    await expect(sessionList.locator(".focused-session-list__row")).not.toHaveCount(
      0,
      { timeout: 60_000 },
    );
    const sessionRow = sessionList.locator(".focused-session-list__row").first();
    await openContextMenu(page, sessionRow);
    const menu = page.getByTestId("context-menu");
    // Snapshot item labels in one read so dismiss-on-scroll cannot flake mid-assert.
    await expect(menu).toContainText("Open");
    await expect(menu).toContainText("Rename");
    await expect(menu).toContainText("Copy title");
    await expect(menu).toContainText("Copy session ID");
    await expect(menu).toContainText("Archive chat");
    await expect(menu).toContainText("Delete chat…");
  });
});
