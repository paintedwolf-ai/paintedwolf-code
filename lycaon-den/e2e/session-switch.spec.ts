import { expect } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiCreateProjectWithRoot,
  bootstrapChatSession,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  openNewChatSession,
    webE2e,
} from "./helpers.ts";

webE2e.describe("session switch", () => {
  webE2e("new session shows chat composer", async ({ page }) => {
    await bootstrapChatSession(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
    await expect(liveChatStage(page).getByTestId("chat-stream")).toBeVisible();
  });

  webE2e("launcher cross-project switch keeps composer reachable", async ({ page, request }) => {
    const first = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Session-A"),
    );
    const second = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Session-B"),
    );
    await gotoShell(page);
    await activateProject(page, first.id);
    await openNewChatSession(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 90_000 });

    await page.getByTestId("project-switcher").click();
    await page.getByTestId(`project-launcher-row-${second.id}`).click();
    await expect(page.getByTestId("project-switcher")).toContainText(second.name ?? "Untitled");
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 90_000 });
  });
});
