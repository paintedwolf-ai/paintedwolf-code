import { expect, type Page } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

const stageTabs = (page: Page) => page.getByTestId("stage-tabs");
const stageTab = (page: Page, id: string) => page.getByTestId(`stage-tab-${id}`);
const stagePanel = (page: Page) => page.getByTestId("stage-tabs-panel");
const conversationTabs = (page: Page) => page.getByTestId("tab-progress");

async function openFiles(page: Page) {
  await page
    .getByTestId("focused-project-nav")
    .getByRole("button", { name: "Files", exact: true })
    .click();
  await expect(page.getByTestId("files-tree")).toBeVisible();
}

async function switchToSplit(page: Page) {
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();
}

webE2e.describe("session tabs in the stage title bar", () => {
  webE2e("single view carries the tabs on another stage and returns them with the chat", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await bootstrapChatSession(page);
    await expect(conversationTabs(page)).toBeVisible();
    await expect(stageTabs(page)).toHaveCount(0);

    await openFiles(page);
    await expect(conversationTabs(page)).toBeHidden();
    for (const id of ["progress", "workflows", "git", "workers"]) {
      await expect(stageTab(page, id)).toBeVisible();
    }

    // The icons end the title bar, at the stage's right edge.
    const stage = await page.getByTestId("split-col-stage").boundingBox();
    const last = await stageTab(page, "workers").boundingBox();
    expect(stage!.x + stage!.width - (last!.x + last!.width)).toBeLessThan(40);

    await page.getByTestId("focused-session-list").getByRole("button").first().click();
    await expect(conversationTabs(page)).toBeVisible();
    await expect(stageTabs(page)).toHaveCount(0);
  });

  webE2e("a tab opens its panel over the stage and Escape closes it", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await bootstrapChatSession(page);
    await openFiles(page);

    await stageTab(page, "workers").click();
    await expect(stagePanel(page)).toBeVisible();
    await expect(stagePanel(page)).toContainText("No workers");
    await expect(stageTab(page, "workers")).toHaveAttribute("aria-expanded", "true");
    // The stage keeps its place under the panel.
    await expect(page.getByTestId("files-tree")).toBeVisible();

    await stageTab(page, "progress").click();
    await expect(stagePanel(page)).toHaveAttribute("data-tab", "progress");

    await page.keyboard.press("Escape");
    await expect(stagePanel(page)).toHaveCount(0);
    await expect(stageTab(page, "progress")).toHaveAttribute("aria-expanded", "false");
  });

  webE2e("the worklog opens in the conversation it belongs to", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await bootstrapChatSession(page);
    await openFiles(page);

    await stageTab(page, "progress").click();
    await stagePanel(page).getByTestId("worklog-open").click();
    await expect(page.getByTestId("chat-view")).toBeVisible();
    await expect(page.getByTestId("worklog-panel")).toBeVisible();
    await expect(stageTabs(page)).toHaveCount(0);
  });

  webE2e("split view carries the tabs while the conversation is closed", async ({ page }) => {
    await page.setViewportSize({ width: 1500, height: 900 });
    await bootstrapChatSession(page);
    await openFiles(page);
    await switchToSplit(page);
    await expect(conversationTabs(page)).toBeVisible();
    await expect(stageTabs(page)).toHaveCount(0);

    await page.getByTestId("conversation-collapse-btn").click();
    await expect(stageTab(page, "git")).toBeVisible();
    // The icons sit beside the control that brings the conversation back.
    const reopen = await page.getByTestId("conversation-expand-btn").boundingBox();
    const last = await stageTab(page, "workers").boundingBox();
    expect(reopen!.x - (last!.x + last!.width)).toBeGreaterThanOrEqual(0);
    expect(reopen!.x - (last!.x + last!.width)).toBeLessThan(16);

    await stageTab(page, "git").click();
    await expect(stagePanel(page)).toBeVisible();

    await page.getByTestId("conversation-expand-btn").click();
    await expect(conversationTabs(page)).toBeVisible();
    await expect(stageTabs(page)).toHaveCount(0);

    // A panel opened over the stage does not come back with the next close.
    await page.getByTestId("conversation-collapse-btn").click();
    await expect(stageTab(page, "git")).toBeVisible();
    await expect(stagePanel(page)).toHaveCount(0);
  });
});
