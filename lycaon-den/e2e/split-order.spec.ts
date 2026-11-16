import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, openProjectFilesFixture, webE2e } from "./helpers.ts";

async function openSplit(page: Page) {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await openProjectFilesFixture(page, page.request, {
    prefix: "split-order", name: "Split order",
    seed: root => writeFileSync(path.join(root, "example.md"), "# Split order\n\n" + "Keep the editor mounted.\n".repeat(120)),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "example.md" }).click();
  await expect(page.locator(".cm-content:visible")).toBeVisible();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
}

async function layoutAction(page: Page, action: "swap-columns" | "toggle-orientation") {
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId(`layout-${action}`).click();
  await page.getByTestId("layout-dock-btn").click();
}

function visibleRestore(page: Page, testId: string) {
  return page.locator(`[data-testid="${testId}"]:visible`);
}

async function expectSeparateRestoreTargets(page: Page) {
  const controls = page.locator('.den-shell-header-chat .den-pane-toggle--labelled:visible');
  for (const control of await controls.all()) {
    const label = control.locator("span").last();
    const buttonBox = (await control.boundingBox())!;
    const labelBox = (await label.boundingBox())!;
    expect(labelBox.x).toBeGreaterThanOrEqual(buttonBox.x);
    expect(labelBox.x + labelBox.width).toBeLessThanOrEqual(buttonBox.x + buttonBox.width);
    expect(await label.evaluate(el => {
      const box = el.getBoundingClientRect();
      return document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)?.closest("button") === el.closest("button");
    })).toBe(true);
  }
}

webE2e.describe("independent split order", () => {
  webE2e("swaps from the composer shortcut without changing the draft", async ({ page }) => {
    await openSplit(page);
    const composer = liveChatStage(page).getByTestId("chat-composer");
    await composer.fill("Keep this draft.");
    const mac = await page.evaluate(() => /Mac/.test(navigator.platform));
    const shortcut = mac ? "Meta+Alt+x" : "Control+Shift+x";
    await composer.press(shortcut);
    await expect(page.getByTestId("shell-stage-host")).toHaveAttribute("data-stage-leading", "false");
    await expect(composer).toBeFocused();
    await expect(composer).toHaveValue("Keep this draft.");
    await composer.press(shortcut);
    await expect(page.getByTestId("shell-stage-host")).toHaveAttribute("data-stage-leading", "true");
  });

  webE2e("composes swapping and mirroring without remounting editor or chat", async ({ page }) => {
    await openSplit(page);
    const composer = liveChatStage(page).getByTestId("chat-composer");
    await composer.fill("Keep this draft when the panes move.");
    const input = await composer.elementHandle();
    const editor = await page.locator(".cm-content:visible").elementHandle();
    const chat = page.getByTestId("split-col-chat");
    const stage = page.getByTestId("split-col-stage");
    const originalWidth = (await chat.boundingBox())!.width;
    const scrollTop = await page.locator(".cm-scroller:visible").evaluate(el => {
      el.scrollTop = 300;
      return el.scrollTop;
    });

    for (const [action, stageOnLeft, treeOnLeft] of [
      ["swap-columns", false, true],
      ["toggle-orientation", true, false],
      ["swap-columns", false, false],
      ["toggle-orientation", true, true],
    ] as const) {
      await layoutAction(page, action);
      await expect(page.getByTestId("shell-stage-host")).toHaveAttribute("data-stage-leading", String(stageOnLeft));
      const chatBox = (await chat.boundingBox())!;
      const stageBox = (await stage.boundingBox())!;
      expect(stageBox.x < chatBox.x).toBe(stageOnLeft);
      expect(chatBox.width).toBeCloseTo(originalWidth, 0);
      const treeBox = (await page.getByTestId("files-tree").boundingBox())!;
      const editorBox = (await page.locator(".cm-content:visible").boundingBox())!;
      expect(treeBox.x < editorBox.x).toBe(treeOnLeft);
      expect(await input!.evaluate(el => el.isConnected)).toBe(true);
      expect(await editor!.evaluate(el => el.isConnected)).toBe(true);
      await expect(composer).toHaveValue("Keep this draft when the panes move.");
      expect(await page.locator(".cm-scroller:visible").evaluate(el => el.scrollTop)).toBeCloseTo(scrollTop, 0);
    }
    await layoutAction(page, "swap-columns");
    await page.reload();
    await expect(page.getByTestId("shell-stage-host")).toHaveAttribute("data-stage-leading", "false");
    await expect(page.getByTestId("split-divider")).toBeVisible();
  });

  for (const swapped of [false, true]) {
    for (const mirrored of [false, true]) {
      webE2e(`hides and restores context without moving Files (swapped: ${swapped}, mirrored: ${mirrored})`, async ({ page }) => {
        await openSplit(page);
        await page.evaluate(() => document.documentElement.classList.add("den-custom-chrome", "den-tauri-macos"));
        if (swapped) await layoutAction(page, "swap-columns");
        if (mirrored) await layoutAction(page, "toggle-orientation");
        const stage = page.getByTestId("split-col-stage");
        const chat = page.getByTestId("split-col-chat");
        const editor = page.locator(".cm-content:visible");
        const node = await editor.elementHandle();
        const grip = page.getByTestId("files-tree-resize");
        const tabStrip = page.locator(".den-files-tab-strip:visible");
        await expect.poll(async () => {
          const ruleY = await tabStrip.evaluate(el => el.getBoundingClientRect().top
            + parseFloat(getComputedStyle(el).paddingTop)
            + parseFloat(getComputedStyle(el.querySelector(".den-files-tab-strip__tree-toggle")!).marginTop));
          return Math.abs((await grip.boundingBox())!.y - ruleY);
        }).toBeLessThan(1);
        const gripBox = (await grip.boundingBox())!;
        expect(gripBox.width).toBe(4);
        for (const x of [gripBox.x - 3, gripBox.x + gripBox.width + 3]) {
          expect(await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.closest('[data-testid="files-tree-resize"]') !== null,
            { x, y: gripBox.y + 4 })).toBe(true);
        }
        const belowChrome = (await page.getByTestId("files-tree").boundingBox())!.y + 20;
        expect(await page.evaluate(({ x, y }) => Boolean(document.elementFromPoint(x, y)?.closest('[data-testid="files-tree-resize"]')),
          { x: gripBox.x - 3, y: belowChrome })).toBe(false);
        const y = (await page.getByTestId("files-tree").boundingBox())!.y;
        const hideChat = (await chat.getByTestId("conversation-collapse-btn").boundingBox())!;
        const hideContext = (await stage.getByTestId("context-collapse-btn").boundingBox())!;
        const menu = (await chat.getByTestId("session-export-menu-trigger").boundingBox())!;
        const chip = (await chat.getByTestId("tab-progress").boundingBox())!;
        const center = hideContext.y + hideContext.height / 2;
        expect(Math.abs(hideChat.y + hideChat.height / 2 - center)).toBeLessThan(1.5);
        expect(Math.abs(chip.y + chip.height / 2 - center)).toBeLessThan(1.5);
        expect(Math.abs(menu.y + menu.height / 2 - center)).toBeLessThan(1.5);
        expect(menu.x < hideChat.x).toBe(hideChat.x > chip.x);
        await chat.getByTestId("conversation-collapse-btn").click();
        await expect(chat).toBeHidden();
        expect((await page.getByTestId("files-tree").boundingBox())!.y).toBeCloseTo(y, 0);
        await stage.getByTestId("conversation-expand-btn").click();
        await expect(chat).toBeVisible();
        expect((await page.getByTestId("files-tree").boundingBox())!.y).toBeCloseTo(y, 0);
        await stage.getByTestId("context-collapse-btn").click();
        await expect(stage).toBeHidden();
        await expect(chat).toBeVisible();
        await expect(stage).toHaveAttribute("inert", "");
        expect(await node!.evaluate(el => el.isConnected)).toBe(true);
        await page.getByTestId("nav-collapse-btn").click();
        await expect(chat.getByTestId("nav-expand-btn")).toBeVisible();
        await expect(chat.getByTestId("context-expand-btn")).toBeVisible();
        await expect(chat.locator(".den-pane-toggle--labelled:visible")).toHaveCount(swapped ? 0 : 2);
        await expectSeparateRestoreTargets(page);
        await page.setViewportSize({ width: 800, height: 900 });
        await expect(chat).toBeVisible();
        await expect(stage).toBeHidden();
        await expectSeparateRestoreTargets(page);
        await page.setViewportSize({ width: 1600, height: 1000 });
        await chat.getByTestId("context-expand-btn").click();
        await expect(stage).toBeVisible();
        expect(await node!.evaluate(el => el.isConnected)).toBe(true);
        await visibleRestore(page, "nav-expand-btn").click();
        await stage.getByTestId("context-collapse-btn").click();
        await page.getByTestId("project-files-entry").click();
        await expect(stage).toBeVisible();
        await page.screenshot({ path: `test-results/split-${swapped}-${mirrored}.png` });
      });
    }
  }

  for (const mirrored of [false, true]) {
    webE2e(`routes sidebar and chat restores to their seams (mirrored: ${mirrored})`, async ({ page }) => {
      await openSplit(page);
      await layoutAction(page, "swap-columns");
      if (mirrored) await layoutAction(page, "toggle-orientation");
      const chat = page.getByTestId("split-col-chat");
      const stage = page.getByTestId("split-col-stage");
      await page.getByTestId("nav-collapse-btn").click();
      const navRestore = visibleRestore(page, "nav-expand-btn");
      await expect(navRestore).toHaveCount(1);
      await expect(chat.getByTestId("nav-expand-btn")).toBeVisible();
      await expect(stage.getByTestId("nav-expand-btn")).toBeHidden();
      await chat.getByTestId("conversation-collapse-btn").click();
      await expect(chat).toBeHidden();
      await expect(stage.getByTestId("nav-expand-btn")).toBeVisible();
      const sidebar = stage.getByTestId("nav-expand-btn");
      const conversation = stage.getByTestId("conversation-expand-btn");
      await expect(sidebar).toHaveText("Sidebar");
      await expect(conversation).toHaveText("Chat");
      expect((await sidebar.boundingBox())!.x < (await conversation.boundingBox())!.x).toBe(!mirrored);
      await conversation.click();
      await expect(chat).toBeVisible();
      await expect(chat.getByTestId("nav-expand-btn")).toBeVisible();
      await chat.getByTestId("nav-expand-btn").click();
      await expect(page.getByTestId("nav-collapse-btn")).toBeVisible();

      // Automatic collapse keeps double chevrons and only one sidebar restore.
      await page.setViewportSize({ width: 800, height: 900 });
      await expect(chat).toBeVisible();
      await expect(stage).toBeHidden();
      await expect(page.getByTestId("split-fit-restore-stage")).toBeVisible();
      await expect(visibleRestore(page, "nav-auto-restore-btn")).toHaveCount(1);
      await page.setViewportSize({ width: 1600, height: 1000 });
      await expect(stage).toBeVisible();

      await page.getByTestId("nav-settings").click();
      await page.getByTestId("settings-nav-general").click();
      await page.getByTestId("general-tab-display").click();
      await page.getByTestId("display-narrow-survivor").click();
      await page.locator('[role="option"][data-value="stage"]').click();
      await page.getByTestId("settings-stage-close").click();
      await page.setViewportSize({ width: 800, height: 900 });
      await expect(chat).toBeHidden();
      await expect(stage).toBeVisible();
      await expect(stage.getByTestId("nav-auto-restore-btn")).toBeVisible();
      await expect(stage.getByTestId("split-fit-restore-conversation")).toBeVisible();
      await expect(stage.getByTestId("split-fit-restore-conversation")).toHaveText("Chat");
      await page.setViewportSize({ width: 1600, height: 1000 });
      await expect(chat).toBeVisible();
      await expect(stage.getByTestId("split-fit-restore-conversation")).toBeHidden();
    });
  }
});

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

    await page
      .locator('[data-testid="focused-session-list"] [data-session-id][aria-current="true"]')
      .click();
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

  webE2e("the worklog drawer opens over the stage with the conversation closed", async ({ page }) => {
    await page.setViewportSize({ width: 1500, height: 900 });
    await bootstrapChatSession(page);
    await openFiles(page);

    await stageTab(page, "progress").click();
    await stagePanel(page).getByTestId("worklog-open").click();
    const drawer = page.getByTestId("worklog-panel");
    await expect(drawer).toBeVisible();
    await expect(page.getByTestId("chat-view")).toBeHidden();
    await expect(page.getByTestId("files-tree")).toBeVisible();
    await expect(stageTab(page, "progress")).toBeVisible();

    // The drawer lies over the stage's trailing edge and the stage keeps its width.
    const stage = await page.getByTestId("split-col-stage").boundingBox();
    const box = await drawer.boundingBox();
    expect(Math.abs(stage!.x + stage!.width - (box!.x + box!.width))).toBeLessThanOrEqual(1);
    expect(box!.y).toBe(stage!.y);

    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(page.getByTestId("files-tree")).toBeVisible();
  });

  webE2e("arming a workflow shows it on the composer it arms", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await bootstrapChatSession(page);
    await openFiles(page);

    await stageTab(page, "workflows").click();
    await stagePanel(page).getByTestId("pick-workflow-button").click();
    await stagePanel(page).locator('[data-testid^="workflow-picker-row-"]').first().click();
    await expect(liveChatStage(page).getByTestId("workflow-arm-chip")).toBeVisible();
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
    // The control that brings the conversation back leads the icons.
    const reopen = await page.getByTestId("conversation-expand-btn").boundingBox();
    const first = await stageTab(page, "progress").boundingBox();
    expect(first!.x - (reopen!.x + reopen!.width)).toBeGreaterThanOrEqual(0);
    expect(first!.x - (reopen!.x + reopen!.width)).toBeLessThan(16);

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
