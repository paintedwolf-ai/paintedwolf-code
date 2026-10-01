import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import {
  activateProject,
  apiCreateProjectWithRoot,
  CONTEXT_NAV_WITH,
  e2eUniqueLabel,
  e2eTempDir,
  gotoShell,
  seedAppState,
  webE2e,
} from "./helpers.ts";

webE2e("skills settings: units list normal + conflict chrome", async ({
  page,
  request,
}) => {
  const root = e2eTempDir(request, "skills-units");
  writeFileSync(path.join(root, "README.md"), "# skills units\n");
  const project = await apiCreateProjectWithRoot(
    request,
    root,
    e2eUniqueLabel("SkillsUnits"),
  );
  const contextNav = CONTEXT_NAV_WITH("extensions");
  await seedAppState(page, { contextNav });
  await gotoShell(page);
  await activateProject(page, project.id);

  if (!(await page.getByTestId("project-extensions-entry").isVisible().catch(() => false))) {
    await page.getByTestId("project-context-customize").click();
    await page.getByTestId("project-context-show-extensions").click();
  }
  await page.getByTestId("project-extensions-entry").click();
  await expect(page.getByTestId("project-extensions-panel")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("extensions-panel-units")).toBeVisible();
  await page.getByTestId("extensions-kind-skills").click();
  const skillRow = page
    .locator('[data-testid^="extensions-unit-row-skills%2F"]')
    .first();
  await expect(skillRow).toBeVisible({ timeout: 15_000 });
  await expect(skillRow).toContainText("Skill");

  await page.route("**/v1/extensions?*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    const catalog = await response.json();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...catalog,
        units: [
          {
            id: "skills/verify-a-change",
            kind: "skills",
            title: "Verify a change",
            status: "conflict",
            contributions: [
              {
                pack_id: "painted-wolf/platform",
                path: "skills/verify-a-change/SKILL.md",
              },
              {
                pack_id: "acme/extras",
                path: "skills/verify-a-change/SKILL.md",
              },
            ],
          },
        ],
      }),
    });
  });
  // Reactivation revalidates the retained catalog.
  await page.getByTestId("project-cost-entry").click();
  await expect(page.getByTestId("project-extensions-panel")).toBeHidden({ timeout: 30_000 });
  await page.getByTestId("project-extensions-entry").click();
  await expect(page.getByTestId("project-extensions-panel")).toBeVisible();
  await page.getByTestId("extensions-kind-skills").click();
  const conflictRow = page.getByTestId(
    `extensions-unit-row-${encodeURIComponent("skills/verify-a-change")}`,
  );
  await expect(conflictRow).toBeVisible({ timeout: 15_000 });
  await expect(conflictRow).toContainText("Conflict");
});
