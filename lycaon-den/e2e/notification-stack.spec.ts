import { expect } from "@playwright/test";
import type { NoticeInput } from "../src/notices/notice-model.ts";
import type { NoticeScope } from "../src/notices/notice-scope.ts";
import {
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

type HarnessWindow = {
  __harness?: {
    goHome(): Promise<{ ok?: boolean; error?: string }>;
    publishNotice(
      input: NoticeInput,
      scope?: NoticeScope,
    ): { ok?: boolean; error?: string };
  };
};

webE2e.describe("shared notification stack", () => {
  webE2e(
    "shows app then attributed project notices on chat and Home",
    async ({ page, request }) => {
      const project = await bootstrapChatSession(page, request);
      const sessionId = await settledChatSessionId(page);
      expect(project.name).toBeTruthy();

      await page.waitForFunction(
        () => (window as unknown as HarnessWindow).__harness != null,
        undefined,
        { timeout: 30_000 },
      );
      const published = await page.evaluate(
        ({ projectId }) => {
          const harness = (window as unknown as HarnessWindow).__harness;
          return [
            harness?.publishNotice(
              {
                code: "harness_app_notice",
                title: "App condition",
                message: "This installation needs attention.",
              },
              { kind: "app" },
            ),
            harness?.publishNotice(
              {
                code: "harness_project_notice",
                title: "Project condition",
                message: "This project needs attention.",
                suggestedAction: "Review the project configuration.",
              },
              { kind: "project", projectId },
            ),
          ];
        },
        { projectId: project.id },
      );
      expect(published).toEqual([
        expect.objectContaining({ ok: true }),
        expect.objectContaining({ ok: true }),
      ]);

      const chatStack = liveChatStage(page).getByTestId("notification-stack");
      await expect(chatStack).toBeVisible({ timeout: 15_000 });
      await expect(chatStack.getByText("App condition")).toBeVisible();
      await expect(chatStack.getByText("Project condition")).toBeVisible();
      await expect(
        chatStack.getByRole("button", { name: `Open project ${project.name}` }),
      ).toBeVisible();
      expect(
        await chatStack.locator(":scope > *").evaluateAll((nodes) =>
          nodes.map((node) => node.className),
        ),
      ).toEqual([
        expect.stringContaining("den-notice-rail"),
        expect.stringContaining("den-project-notices"),
      ]);

      const home = await page.evaluate(() =>
        (window as unknown as HarnessWindow).__harness?.goHome(),
      );
      expect(home?.ok).toBe(true);
      await expect(page.getByTestId("home-view")).toBeVisible({ timeout: 15_000 });

      const homeStack = page.getByTestId("notification-stack");
      await expect(homeStack.getByText("App condition")).toBeVisible();
      await expect(homeStack.getByText("Project condition")).toBeVisible();
      await homeStack
        .getByRole("button", { name: `Open project ${project.name}` })
        .click();
      await expect.poll(() => settledChatSessionId(page), { timeout: 30_000 }).toBe(sessionId);
      await expect(liveChatStage(page)).toBeVisible({ timeout: 30_000 });
    },
  );
});
