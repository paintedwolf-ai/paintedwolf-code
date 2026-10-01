import { expect } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiCreateProjectWithRoot,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  openNewChatSession,
    sendChatPrompt,
  webE2e,
} from "./helpers.ts";

webE2e.describe("project switch", () => {
  webE2e("launcher switch does not show stale transcript from prior project", async ({
    page,
    request,
  }) => {
    const marker = e2eUniqueLabel("sw");
    const small = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Small"),
    );
    const large = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Large"),
    );
    await gotoShell(page);
    await activateProject(page, small.id);
    await openNewChatSession(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 90_000 });
    await sendChatPrompt(page, marker);
    await expect(page.locator(".bubble--user")).toContainText(marker, { timeout: 30_000 });

    await page.getByTestId("project-switcher").click();
    await expect(page.getByTestId("project-launcher")).toBeVisible();
    await page.getByTestId(`project-launcher-row-${large.id}`).click();
    await expect(page.getByTestId("project-switcher")).toContainText(large.name ?? "Untitled");

    await expect(liveChatStage(page).getByTestId("chat-stream")).not.toContainText(marker, {
      timeout: 30_000,
    });
  });
});
