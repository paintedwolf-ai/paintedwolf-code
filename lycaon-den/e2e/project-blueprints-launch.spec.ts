import { expect, test } from "@playwright/test";
import { overlayRel } from "../src/platform/files/overlay-dir.ts";
import {
  PLAN_STUB_VALID_CONTENT,
  activateProject,
  apiCreateBlueprint,
  apiFindHarnessProject,
  apiGetActiveWorkflowRun,
  apiTryGetActiveWorkflowRun,
  apiGetBlueprint,
  apiUpdatePlanContent,
  e2eUniqueLabel,
  liveChatStage,
  openNewChatSession,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

/**
 * Den Blueprints zone → Run → armed workflow → composer send → seeded run.
 * Source file stays draft (not auto-approved); launch mints a new path.
 */
webE2e.describe("project blueprints launch", () => {
  webE2e(
    "Blueprints → Run → seeded plan session (not auto-approved)",
    async ({ page, request }) => {
      test.setTimeout(180_000);

      await page.goto("/");
      await page.waitForFunction(
        () =>
          (
            window as unknown as {
              __harness?: { state(): { connected: boolean } };
            }
          ).__harness?.state().connected === true,
        undefined,
        { timeout: 60_000 },
      );

      const project = await apiFindHarnessProject(request);
      await activateProject(page, project.id);
      await openNewChatSession(page);

      const source = await apiCreateBlueprint(request, project.id, {
        title: e2eUniqueLabel("Harness blueprint seed"),
        path: overlayRel("blueprints", `${e2eUniqueLabel("harness-seed")}.md`),
        source_workflow_id: "plan",
      });
      await apiUpdatePlanContent(
        request,
        project.id,
        source.path,
        PLAN_STUB_VALID_CONTENT,
      );
      const sourceBeforeLaunch = await apiGetBlueprint(
        request,
        project.id,
        source.path,
      );

      await page.getByTestId("project-blueprints-entry").click();
      await expect(page.getByTestId("project-blueprints-view")).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.getByTestId("project-blueprints-list")).toBeVisible({
        timeout: 60_000,
      });

      const row = page.locator(
        `[data-testid="project-blueprint-row"][data-blueprint-path="${source.path}"]`,
      );
      await expect(row).toBeVisible();
      await row.getByTestId("project-blueprint-run").click();

      const picker = page.getByTestId("project-blueprint-workflow-picker");
      if (await picker.isVisible().catch(() => false)) {
        await expect(picker).toContainText("Plan");
        await page.getByTestId("project-blueprint-launch").click();
      }

      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });
      await expect(liveChatStage(page).getByTestId("workflow-arm-chip")).toContainText(
        "Plan — send to start",
      );
      const launchedSessionId = await settledChatSessionId(page);
      expect(launchedSessionId, "launched chat-stream session").toBeTruthy();

      await liveChatStage(page)
        .getByTestId("chat-composer")
        .fill("Use the selected blueprint as the plan seed");
      await liveChatStage(page).getByTestId("chat-composer").press("Enter");

      await expect
        .poll(
          async () => {
            const active = await apiTryGetActiveWorkflowRun(request, launchedSessionId);
            if (!active) return null;
            return active.workflow_id === "plan" ? active : null;
          },
          { timeout: 60_000 },
        )
        .toBeTruthy();
      const run = await apiGetActiveWorkflowRun(request, launchedSessionId);
      expect(run.workflow_id).toBe("plan");
      expect(run.blueprint_path).toBeTruthy();
      expect(run.blueprint_path).not.toBe(source.path);

      const seed = await apiGetBlueprint(
        request,
        project.id,
        run.blueprint_path!,
      );
      expect(seed.content).toContain("## Goal");
      expect(seed.status).toBe("draft");

      const sourceReload = await apiGetBlueprint(
        request,
        project.id,
        source.path,
      );
      expect(sourceReload.status).toBe("draft");
      expect(sourceReload.content).toBe(sourceBeforeLaunch.content);

    },
  );
});
