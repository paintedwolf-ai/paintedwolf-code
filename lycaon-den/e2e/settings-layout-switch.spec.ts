import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import {
  bootstrapChatSession,
  liveChatStage,
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

async function switchSettingsAndLayout(page: Page) {
  const settings = page.getByTestId("nav-settings");
  const layout = page.getByTestId("layout-dock-btn");
  const tray = page.getByTestId("layout-tray");

  await settings.click();
  await expect(page.getByTestId("general-settings-panel")).toBeVisible();
  await layout.click();
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
  await expect(layout).toHaveAttribute("aria-expanded", "true");
  await expect(tray).toBeVisible();
  await expect(settings).toHaveAttribute("aria-expanded", "false");
  const dismissedFrames = await page.evaluate(async () => {
    const dismissed: string[] = [];
    const button = document.querySelector('[data-testid="layout-dock-btn"]')!;
    for (let frame = 0; frame < 30; frame++) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      if (button.getAttribute("aria-expanded") !== "true") {
        dismissed.push(document.activeElement?.outerHTML ?? "no focus");
      }
    }
    return dismissed;
  });
  expect(dismissedFrames).toEqual([]);

  await settings.click();
  await expect(page.getByTestId("general-settings-panel")).toBeVisible();
  await expect(settings).toHaveAttribute("aria-expanded", "true");
  await expect(layout).toHaveAttribute("aria-expanded", "false");
  await expect(tray.locator("xpath=../..")).toHaveAttribute("aria-hidden", "true");
}

webE2e("switches between Settings and Layout without dismissing the new tray", async ({ page }) => {
  await bootstrapChatSession(page);
  await switchSettingsAndLayout(page);
});

webE2e("keeps Layout open when leaving Settings restores a split editor", async ({ page, request }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await openProjectFilesFixture(page, request, {
    prefix: "settings-layout",
    name: "Settings and Layout",
    seed: (root) => writeFileSync(path.join(root, "example.md"), "# Example\n"),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "example.md" }).click();
  await expect(page.locator(".cm-content:visible")).toBeVisible();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await expect(page.locator(".cm-content:visible")).toBeVisible();
  await switchSettingsAndLayout(page);
});
