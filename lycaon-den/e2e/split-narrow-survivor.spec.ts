import { expect, type Page } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

/** Files beside the conversation, at a width that fits both columns. */
async function openFilesSplit(page: Page) {
  await page.setViewportSize({ width: 1500, height: 900 });
  await bootstrapChatSession(page);
  await page
    .getByTestId("focused-project-nav")
    .getByRole("button", { name: "Files", exact: true })
    .click();
  await expect(page.getByTestId("files-tree")).toBeVisible();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();
}

async function pinSurvivor(page: Page, value: "conversation" | "stage") {
  await page.evaluate(async () => {
    await (
      window as unknown as { __harness: { openSettings(): Promise<unknown> } }
    ).__harness.openSettings();
  });
  await expect(page.getByTestId("settings-view")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("settings-nav-general").click();
  await page.getByTestId("general-tab-display").click();
  await page.getByTestId("display-narrow-survivor").click();
  await page.locator(`[role="option"][data-value="${value}"]`).click();
  await page.getByTestId("settings-stage-close").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();
}

const stageCol = (page: Page) => page.getByTestId("split-col-stage");
const chatCol = (page: Page) => page.getByTestId("split-col-chat");

webE2e.describe("narrow split survivor", () => {
  webE2e("keeps the conversation by default, and offers the width back", async ({ page }) => {
    await openFilesSplit(page);
    await expect(stageCol(page)).toBeVisible();

    await page.setViewportSize({ width: 1000, height: 900 });
    await expect(stageCol(page)).toBeHidden();
    await expect(chatCol(page)).toBeVisible();
    await expect(page.getByTestId("split-divider")).toBeHidden();

    // The seam swaps its control: nothing left to hide, a width to claim.
    await expect(page.getByTestId("conversation-collapse-btn")).toBeHidden();
    const widen = page.getByTestId("split-fit-restore-stage");
    await expect(widen).toBeVisible();
    await expect(widen).toHaveAttribute("aria-label", "Widen window to show Files");
  });

  webE2e("keeps the stage when that is the declared survivor", async ({ page }) => {
    await openFilesSplit(page);
    await pinSurvivor(page, "stage");

    await page.setViewportSize({ width: 1000, height: 900 });
    await expect(chatCol(page)).toBeHidden();
    await expect(stageCol(page)).toBeVisible();
    await expect(page.getByTestId("files-tree")).toBeVisible();

    const widen = page.getByTestId("split-fit-restore-conversation");
    await expect(widen).toBeVisible();
    await expect(widen).toHaveAttribute(
      "aria-label",
      "Widen window to show conversation",
    );

    // Widening puts both columns back without a preference changing.
    await page.setViewportSize({ width: 1500, height: 900 });
    await expect(chatCol(page)).toBeVisible();
    await expect(stageCol(page)).toBeVisible();
    await expect(widen).toBeHidden();
  });

  webE2e("never folds the Files tree only to give it back", async ({ page }) => {
    await openFilesSplit(page);
    await pinSurvivor(page, "stage");

    // The tree holds across the seam: the surviving stage never inherits
    // width the split had denied it.
    for (const width of [1500, 1300, 1200, 1120, 1080, 1000, 900, 800]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(page.getByTestId("files-tree")).toBeVisible();
    }
  });
});
