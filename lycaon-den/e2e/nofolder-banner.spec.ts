import path from "node:path";
import { expect } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  bootstrapActiveProject,
  liveChatStage,
  openNewChatSession,
  webE2e,
} from "./helpers.ts";

// Adding a root restores file access after the last root is detached.
webE2e.describe("nofolder banner", () => {
  webE2e("den:harness — NoFolderBanner → Add folder → rooted project", async ({
    page,
    request,
  }) => {
    const project = await bootstrapActiveProject(page, request);
    const rootId = project.roots?.[0]?.id;
    expect(rootId).toBeTruthy();

    await openNewChatSession(page);
    const chat = liveChatStage(page);
    await expect(chat.getByTestId("nofolder-banner")).toHaveCount(0);

    // Detach lives in the expanded rows; the facepile is the collapsed summary.
    await page.getByTestId("project-folder-summary").click();
    await page.getByTestId(`project-folder-remove-${rootId}`).click();
    await page.getByRole("button", { name: "Remove folder", exact: true }).click();
    await expect(page.locator(".project-folders--empty")).toBeVisible({
      timeout: 30_000,
    });
    await expect(chat.getByTestId("nofolder-banner")).toBeVisible({ timeout: 30_000 });
    await expect(chat.getByTestId("nofolder-add")).toBeVisible();

    await chat.getByTestId("nofolder-add").click();
    await page.getByRole("textbox", { name: "Folder path", exact: true }).fill(E2E_FIXTURE_PROJECT);
    await page.getByRole("button", { name: "Choose folder", exact: true }).click();

    await expect(chat.getByTestId("nofolder-banner")).toHaveCount(0, { timeout: 30_000 });
    await expect(page.locator(".project-folders--empty")).toHaveCount(0);
    await expect(page.getByTestId("project-folder-zone")).toContainText(
      path.basename(E2E_FIXTURE_PROJECT),
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("project-files-entry")).toBeEnabled({
      timeout: 30_000,
    });
  });
});
