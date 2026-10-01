import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  bootstrapChatSession,
  liveChatStage,
  openNewChatSession,
  settledChatSessionId,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

webE2e.describe("checkpoint tool approval parity", () => {
  webE2e("fresh session has no tool approval card", async ({ page }) => {
    await bootstrapChatSession(page);
    await expect(page.getByTestId("tool-approval-card")).toHaveCount(0);
    await expect(page.getByTestId("approval-approve-primary")).toHaveCount(0);
  });

  webE2e(
    "pending approval docks above the composer",
    async ({ page, request }) => {
      test.setTimeout(180_000);

      await page.goto("/");
      await waitHarnessConnected(page);
      const project = await apiFindHarnessProject(request);
      const { apiUrl, token } = apiConfig();
      const auth = { Authorization: `Bearer ${token}` };

      await activateProject(page, project.id);
      await openNewChatSession(page);
      const stage = liveChatStage(page);
      await expect(stage.getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });
      const sessionId =
        (
          await stage.getByTestId("chat-stream").getAttribute("data-session-id")
        )?.trim() ?? "";
      expect(sessionId).toBeTruthy();

      const inject = await request.post(
        `${apiUrl}/harness/checkpoints/tool_approval`,
        {
          headers: { ...auth, "Content-Type": "application/json" },
          data: { session_id: sessionId, command: "ls -la" },
        },
      );
      expect(inject.ok(), await inject.text()).toBeTruthy();

      const slot = stage.getByTestId("composer-chrome-slot-checkpoint");
      await expect(slot.getByTestId("tool-approval-card")).toBeVisible({
        timeout: 30_000,
      });
      await expect(
        stage
          .getByTestId("chat-stream")
          .getByTestId("tool-approval-card"),
      ).toHaveCount(0);
    },
  );

  webE2e(
    "complex approval fields fit the minimum split chat pane",
    async ({ page, request }) => {
      test.setTimeout(180_000);
      await page.setViewportSize({ width: 1280, height: 720 });
      await page.goto("/");
      await waitHarnessConnected(page);

      const project = await apiFindHarnessProject(request);
      const { apiUrl, token } = apiConfig();
      const auth = { Authorization: `Bearer ${token}` };
      await activateProject(page, project.id);

      const stage = liveChatStage(page);
      await expect(stage.getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });
      const sessionId = await settledChatSessionId(page);
      expect(sessionId).toBeTruthy();

      const socketPath =
        "/private/var/run/a-very-long-local-development-environment/docker.sock";
      const inject = await request.post(
        `${apiUrl}/harness/checkpoints/tool_approval`,
        {
          headers: { ...auth, "Content-Type": "application/json" },
          data: {
            session_id: sessionId,
            command:
              "docker context use a-very-long-local-development-environment-name",
            approved_path: socketPath,
            resolved_path: socketPath,
            title: "Allow a local development service",
          },
        },
      );
      expect(inject.ok(), await inject.text()).toBeTruthy();
      const card = stage.getByTestId("tool-approval-card");
      await expect(card).toBeVisible({ timeout: 60_000 });

      await page
        .getByTestId("focused-project-nav")
        .getByRole("button", { name: "Files", exact: true })
        .click();
      await page.getByTestId("layout-dock-btn").click();
      await page.getByTestId("layout-tile-split").click();
      const divider = page.getByTestId("split-divider");
      await expect(divider).toBeVisible();
      await divider.press("End");

      const chat = page.getByTestId("split-col-chat");
      await expect
        .poll(async () => (await chat.boundingBox())?.width ?? 0)
        .toBeLessThanOrEqual(341);
      await expect(card.getByRole("button", {name:"Approval details in Files",exact:true})).toBeVisible();

      const fit = await card.evaluate((element) => {
        const cardRect = element.getBoundingClientRect();
        const controls = [
          ...element.querySelectorAll<HTMLElement>(
            "[data-zone=actions] button",
          ),
        ];
        return {
          cardClientWidth: element.clientWidth,
          cardScrollWidth: element.scrollWidth,
          controlsFit: controls.every((control) => {
            const rect = control.getBoundingClientRect();
            return rect.left >= cardRect.left && rect.right <= cardRect.right;
          }),
          controlCount: controls.length,
        };
      });
      expect(fit.cardScrollWidth).toBe(fit.cardClientWidth);
      expect(fit.controlCount).toBeGreaterThan(0);
      expect(fit.controlsFit).toBe(true);
      await card.getByRole("button", {name:"Approval details in Files",exact:true}).click();
      await expect(page.getByTestId("chat-content-page")).toContainText(socketPath);
    },
  );
});
