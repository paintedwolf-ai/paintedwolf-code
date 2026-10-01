import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { Error as WireError, UpdateExtensionUnitRequest } from "../src/api/types.ts";
import {
  expect,
  type APIRequestContext,
  type TestInfo,
} from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiCreateProjectWithRoot,
  CONTEXT_NAV_WITH,
  e2eUniqueLabel,
  gotoShell,
  seedAppState,
  webE2e,
} from "./helpers.ts";

function projectRoot(testInfo: TestInfo, name: string) {
  const root = testInfo.outputPath(name);
  mkdirSync(root, { recursive: true });
  writeFileSync(path.join(root, "README.md"), `# ${name}\n`);
  return root;
}

async function expectProjectDisableRejected(
  request: APIRequestContext,
  projectId: string,
  unitId: string,
) {
  const { apiUrl, token } = apiConfig();
  const qs = new URLSearchParams({
    project_id: projectId,
    scope: "project",
  });
  const state = await request.get(`${apiUrl}/v1/extensions?${qs}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(state.ok()).toBeTruthy();
  const { revision } = (await state.json()) as { revision: string };
  const res = await request.patch(
    `${apiUrl}/v1/extensions/units/${encodeURIComponent(unitId)}?${qs}`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      data: { enabled: false, expected_revision: revision } satisfies UpdateExtensionUnitRequest,
    },
  );
  expect(res.status()).toBe(400);
  const rejection = await res.json() as WireError;
  expect(rejection.code).toBe("invalid_request");
}

webE2e.describe("project extension controls", () => {
  webE2e(
    "Context Extensions shows Not applied from this project for a floor refusal",
    async ({ page, request }, testInfo) => {
      const root = projectRoot(testInfo, "project-refusal");
      const project = await apiCreateProjectWithRoot(
        request,
        root,
        e2eUniqueLabel("ExtProjectRefusal"),
      );
      await expectProjectDisableRejected(
        request,
        project.id,
        "policy/WRITE_SCOPE_DENIED",
      );
      // Repository-authored overlays can contain refused intent; the mutation API rejects it.
      mkdirSync(path.join(root, ".paintedwolf"), { recursive: true });
      writeFileSync(path.join(root, ".paintedwolf", "extensions.yaml"), "format: 1\ndisabled:\n  - policy/WRITE_SCOPE_DENIED\n");

      await seedAppState(page, { contextNav: CONTEXT_NAV_WITH("extensions") });
      await gotoShell(page);
      await activateProject(page, project.id);

      if (
        !(await page
          .getByTestId("project-extensions-entry")
          .isVisible()
          .catch(() => false))
      ) {
        await page.getByTestId("project-context-customize").click();
        await page.getByTestId("project-context-show-extensions").click();
      }
      await page.getByTestId("project-extensions-entry").click();
      await expect(page.getByTestId("project-extensions-panel")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("extensions-project-refusals")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("extensions-project-refusals")).toContainText(
        "Not applied from this project",
      );
      await expect(page.getByTestId("extensions-project-refusals")).toContainText(
        "policy/WRITE_SCOPE_DENIED",
      );

      // Device settings omit project refusal rows.
      await page
        .getByRole("button", { name: "Settings", exact: true })
        .click();
      await expect(page.getByTestId("settings-view")).toBeVisible({
        timeout: 15_000,
      });
      const nav = page.getByTestId("settings-nav-extensions");
      await nav.scrollIntoViewIfNeeded();
      await nav.click();
      await expect(page.getByTestId("extensions-settings-panel")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("extensions-settings-panel").getByTestId("extensions-project-refusals")).toHaveCount(0);
    },
  );
});

webE2e("disabling and enabling an extension unit keeps its detail usable", async ({ page, request }, testInfo) => {
  const project = await apiCreateProjectWithRoot(request, projectRoot(testInfo, "unit-toggle"), e2eUniqueLabel("Extension unit"));
  await seedAppState(page, { contextNav: CONTEXT_NAV_WITH("extensions") });
  await gotoShell(page);
  await activateProject(page, project.id);
  await page.getByTestId("project-extensions-entry").click();
  const panel = page.getByTestId("project-extensions-panel");
  await panel.getByRole("searchbox", { name: "Search…", exact: true }).fill("workflows/bugbash");
  await panel.getByTestId(`extensions-unit-row-${encodeURIComponent("workflows/bugbash")}`).click();
  await panel.getByTestId("extensions-unit-disable").click();
  await expect(panel.getByRole("button", { name: "Re-enable", exact: true })).toBeVisible();
  await panel.getByRole("button", { name: "Re-enable", exact: true }).click();
  await expect(panel.getByTestId("extensions-unit-disable")).toBeVisible();
});
