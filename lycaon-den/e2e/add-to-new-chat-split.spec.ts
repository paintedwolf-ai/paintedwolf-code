import { expect } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { liveChatStage, openProjectFilesFixture, settledChatSessionId, webE2e } from "./helpers.ts";

webE2e.describe("add to a new chat in split view", () => {
  for (const collapsed of [false, true]) {
    webE2e(`opens the new destination with the conversation ${collapsed ? "hidden" : "visible"}`, async ({ page, request }) => {
      await page.setViewportSize({ width: 1500, height: 900 });
      await openProjectFilesFixture(page, request, {
        prefix: "new-chat-selection", name: "New chat selection",
        seed: (root) => writeFileSync(path.join(root, "example.ts"), "const value = 1;\n"),
      });
      await page.getByTestId("layout-dock-btn").click();
      await page.getByTestId("layout-tile-split").click();
      await page.getByTestId("layout-dock-btn").click();
      await expect(page.getByTestId("split-divider")).toBeVisible();
      const previousSession = await settledChatSessionId(page);
      if (collapsed) {
        await page.getByTestId("conversation-collapse-btn").click();
        await expect(page.getByTestId("split-col-chat")).toBeHidden();
      }

      await page.getByTestId("files-tree-file").filter({ hasText: "example.ts" }).click();
      const editor = page.getByTestId("files-editor-host").locator(".cm-content").filter({ visible: true });
      await expect(editor).toHaveAttribute("contenteditable", "true");
      await editor.press("ControlOrMeta+a");
      await editor.click({ button: "right" });
      await page.getByRole("menuitem", { name: /^Add to chat/ }).click();
      await page.getByTestId("chat-destination-new").click();

      await expect(page.getByTestId("chat-destination-picker")).toBeHidden();
      await expect.poll(() => settledChatSessionId(page)).not.toBe(previousSession);
      await expect(page.getByTestId("split-col-chat")).toBeVisible();
      await expect(page.getByTestId("split-divider")).toBeVisible();
      await expect(editor).toBeVisible();
      await expect(liveChatStage(page).getByTestId("composer-attachment-chip")).toContainText("example.ts");
    });
  }
});
