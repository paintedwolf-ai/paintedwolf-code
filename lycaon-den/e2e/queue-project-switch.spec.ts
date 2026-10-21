import { expect, type Page } from "@playwright/test";
import type { HarnessApi } from "../src/platform/harness/harness-driver.ts";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiCreateProjectWithRoot,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  openNewChatSession,
  waitForChatComposerReady,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";

const harness = (page: Page) => ({
  manual: () => page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.manual()),
  pending: () => page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.pending(30_000)),
  auto: (text: string) => page.evaluate((reply) => (window as unknown as { __harness: HarnessApi }).__harness.llm.auto(reply), text),
});

async function switchProject(page: Page, projectId: string, name: string | undefined): Promise<void> {
  await page.getByTestId("project-switcher").click();
  await page.getByTestId(`project-launcher-row-${projectId}`).click();
  await expect(page.getByTestId("project-switcher")).toContainText(name ?? "Untitled");
}

webE2e.describe("queue across a project switch", () => {
  webE2e("queued prompts stay visible after switching to another project and back", async ({ page, request }) => {
    const first = await apiCreateProjectWithRoot(request, E2E_FIXTURE_PROJECT, e2eUniqueLabel("Queue-A"));
    const second = await apiCreateProjectWithRoot(request, E2E_FIXTURE_PROJECT, e2eUniqueLabel("Queue-B"));
    await gotoShell(page);
    await activateProject(page, first.id);
    const sessionA = await openNewChatSession(page);
    await waitForSessionReady(page.request, sessionA);
    const composer = await waitForChatComposerReady(page);
    const llm = harness(page);
    await llm.manual();
    try {
      // The held completion keeps the turn busy so the next prompt queues.
      await composer.fill("Start the first turn.");
      await composer.press("Enter");
      await expect(composer).toHaveValue("", { timeout: 30_000 });
      await llm.pending();

      const queuedPrompt = "Then summarize the README.";
      await composer.fill(queuedPrompt);
      await composer.press("Enter");
      const stage = liveChatStage(page);
      await expect(stage.getByTestId("queue-popover-card")).toContainText(queuedPrompt, { timeout: 30_000 });

      await switchProject(page, second.id, second.name);
      await switchProject(page, first.id, first.name);
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 60_000 });
      await expect(liveChatStage(page).getByTestId("queue-popover-card")).toContainText(queuedPrompt, { timeout: 10_000 });
    } finally {
      await llm.auto("Queue fixture complete.");
    }
  });
});
