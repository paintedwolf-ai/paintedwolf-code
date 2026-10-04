import { expect } from "@playwright/test";
import {
  bootstrapChatSession,
  CONTEXT_NAV_WITH,
  E2E_EXAMPLE_PACK_DIR,
  liveChatStage,
  webE2e,
} from "./helpers.ts";

webE2e.describe("extensions settings", () => {
  webE2e("Settings → Extensions is device-only (How it works first)", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await expect(page.getByTestId("settings-view")).toBeVisible({
      timeout: 15_000,
    });
    const nav = page.getByTestId("settings-nav-extensions");
    await nav.scrollIntoViewIfNeeded();
    await nav.click();
    await expect(page.getByTestId("extensions-settings-panel")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("extensions-panel-model")).toBeVisible();
    await expect(page.getByTestId("extensions-settings-panel")).toHaveAttribute("data-surface", "settings");
    await expect(page.getByTestId("extensions-device-banner")).toBeVisible();
    await expect(page.getByTestId("extensions-tab-units")).toBeVisible();
    await page.getByTestId("extensions-tab-packs").click();
    await expect(page.getByTestId("extensions-pack-list")).toBeVisible();
    await expect(page.getByTestId("extensions-pack-list")).toContainText(
      "Stock",
      { timeout: 15_000 },
    );
  });

  webE2e("Context → Extensions shows Active for the project", async ({
    page,
  }) => {
    await bootstrapChatSession(page, undefined, {
      contextNav: CONTEXT_NAV_WITH("extensions"),
    });
    await page.getByTestId("project-extensions-entry").click();
    await expect(page.getByTestId("project-extensions-view")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("project-extensions-panel")).toBeVisible();
    await expect(page.getByTestId("extensions-panel-units")).toBeVisible();
    const firstUnit = page.locator("[data-testid^=extensions-unit-row-]").first();
    await expect(firstUnit).toBeVisible({ timeout: 15_000 });
    await firstUnit.click();
    await expect(page.getByTestId("extensions-unit-detail")).toBeVisible({
      timeout: 15_000,
    });
  });

  webE2e("installs a pack from a folder and renders its settings", async ({
    page,
  }) => {
    await bootstrapChatSession(page);

    await page.getByRole("button", { name: "Settings", exact: true }).click();
    const nav = page.getByTestId("settings-nav-extensions");
    await nav.scrollIntoViewIfNeeded();
    await nav.click();
    await expect(page.getByTestId("extensions-settings-panel")).toBeVisible({
      timeout: 15_000,
    });
    await page.getByTestId("extensions-tab-packs").click();
    const installed = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/v1/extensions/packs" &&
        response.request().method() === "POST",
    );
    await page.getByTestId("extensions-add-folder").click();
    await page.getByRole("textbox", { name: "Folder path", exact: true }).fill(E2E_EXAMPLE_PACK_DIR);
    await page.getByRole("button", { name: "Choose folder", exact: true }).click();
    const installResponse = await installed;
    expect(installResponse.ok(), await installResponse.text()).toBeTruthy();

    const packList = page.getByTestId("extensions-pack-list");
    await expect(packList).toContainText("Editor Surface", { timeout: 30_000 });
    await expect(page.getByTestId("extensions-panel-packs")).toBeVisible();
    await expect(page.getByTestId("extensions-panel-model")).toBeHidden();

    await page.getByTestId("extensions-tab-settings").click();
    const toggle = page.getByTestId(
      "extensions-configuration-input-example/editor-surface:offer-summarize",
    );
    await expect(toggle).toBeVisible({ timeout: 15_000 });
    await expect(toggle).toBeChecked();

    // Editor selection survives focus changes across command surfaces.
    await page.getByTestId("project-files-entry").click();
    await page.getByTestId("files-tree-file").filter({ hasText: "README.md" }).click();
    const editor = page.getByTestId("files-editor-host").locator(".cm-content").filter({ visible: true });
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await editor.press("ControlOrMeta+a");
    const palette = page.getByRole("dialog", { name: "Crossbar", exact: true });
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await page.keyboard.press("ControlOrMeta+k");
    await palette.getByRole("textbox", { name: "Crossbar…", exact: true }).fill("Summarize selection");
    await expect(palette.getByRole("button", { name: /^Summarize selection / })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Code", exact: true }).click();
    await editor.press("ControlOrMeta+k");
    await palette.getByRole("textbox", { name: "Crossbar…", exact: true }).fill("Summarize selection");
    await palette.getByRole("button", { name: /^Summarize selection / }).click();
    await expect(palette.getByRole("textbox", { name: /Additional instruction/ })).toBeVisible();
    await palette.getByRole("button", { name: "Close", exact: true }).click();

    await editor.press("Shift+F10");
    await page.getByRole("menuitem", { name: "Summarize selection", exact: true }).click();
    await expect(palette.getByRole("textbox", { name: /Additional instruction/ })).toBeVisible();
    await palette.getByRole("button", { name: "Close", exact: true }).click();

    // A keyboard reports the shifted capital; Ctrl with a lowercase y is redo off macOS.
    await editor.press("ControlOrMeta+Shift+Y");
    await expect(palette.getByRole("textbox", { name: /Additional instruction/ })).toBeVisible();
    const invoked = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      decodeURIComponent(new URL(response.url()).pathname).endsWith("/commands/example/editor-surface:summarize-selection/invoke"));
    await palette.getByRole("button", { name: "Run", exact: true }).click();
    const invocation = await invoked;
    expect(invocation.ok(), await invocation.text()).toBeTruthy();
    expect(invocation.request().postDataJSON().context).toMatchObject({
      path: "README.md", root_id: expect.any(String), document_revision: expect.any(Number),
      start_line: 1, end_line: 5,
    });
    await page.getByRole("button", { name: /Selected chat\. Activate to show the conversation\./ }).click();
    await expect(liveChatStage(page).getByRole("button", { name: "Open README.md:1–5", exact: true })).toBeVisible();
    await page.getByTestId("project-files-entry").click();

    await editor.press("ArrowRight");
    await editor.press("Shift+F10");
    await expect(page.getByRole("menuitem", { name: "Summarize selection", exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
  });
});
