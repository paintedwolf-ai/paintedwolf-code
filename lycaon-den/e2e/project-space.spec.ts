import { expect } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiAttachProjectRoot,
  apiConfig,
  apiCreateProjectWithRoot,
  bootstrapActiveProject,
  e2eUniqueLabel,
  e2eTempDir,
  expectShellReady,
  waitForProjectList,
  fillSolidControl,
  findProjectListRow,
  gotoShell,
  waitForProjectButton,
  webE2e,
} from "./helpers.ts";

webE2e.describe("project space", () => {
  webE2e("welcome shows composer and recents without auto-opening overlays", async ({
    page,
    request,
  }) => {
    const name = e2eUniqueLabel("Welcome Recent");
    await apiCreateProjectWithRoot(request, E2E_FIXTURE_PROJECT, name);
    await gotoShell(page);
    await waitForProjectButton(page, name);
    await expect(page.getByTestId("home-view")).toBeVisible();
    await expect(page.getByTestId("home-idea-input")).toBeVisible();
    await expect(page.getByRole("button", { name: "Start" })).toBeVisible();
    await expect(page.getByTestId("project-launcher")).toHaveCount(0);
    await expect(page.getByTestId("new-session-modal")).toHaveCount(0);
    await expect(page.getByTestId("home-grid")).toBeVisible();
    await expect(page.getByText(name)).toBeVisible();
  });

  webE2e("launcher switches projects with keyboard", async ({ page, request }) => {
    const first = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Alpha"),
    );
    const second = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Beta"),
    );
    await gotoShell(page);
    await activateProject(page, first.id);
    await page.getByTestId("project-switcher").click();
    await expect(page.getByTestId("project-launcher")).toBeVisible();
    await page.getByTestId(`project-launcher-row-${second.id}`).click();
    await expect(page.getByTestId("project-switcher")).toContainText(second.name ?? "Untitled");
    await expectShellReady(page);
    await page.keyboard.press("ControlOrMeta+p");
    await expect(page.getByTestId("project-launcher")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("project-launcher")).toHaveCount(0);
  });

  webE2e("manage view renames and deletes projects", async ({ page, request }) => {
    const name = e2eUniqueLabel("Delete Me");
    const renamed = "Renamed E2E";
    const project = await apiCreateProjectWithRoot(request, E2E_FIXTURE_PROJECT, name);
    const { apiUrl, token } = apiConfig();
    const patchRes = await request.patch(`${apiUrl}/v1/projects/${project.id}`, {
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      data: { name: renamed },
    });
    expect(patchRes.ok()).toBeTruthy();
    await gotoShell(page);
    await page.getByTestId("home-nav-all").click();
    await expect(page.getByTestId("home-view")).toHaveAttribute("data-boot", "ready");
    await expect(page.getByTestId("home-list")).toBeVisible();
    const row = await findProjectListRow(page, project.id);
    await expect(row.getByText(renamed)).toBeVisible();
    await row.getByTestId(`project-list-menu-${project.id}`).click();
    // The row menu is an anchored surface on the document body.
    await page.getByTestId(`project-list-delete-${project.id}`).click();
    const removal = page.getByTestId("project-removal-dialog");
    await expect(removal).toBeVisible();
    // The project has no extensions to remove once its assessment loads.
    await removal.getByRole("button", { name: "Delete project", exact: true }).click();
    await expect(page.getByTestId(`project-list-row-${project.id}`)).toHaveCount(0);
  });

  webE2e("focused nav shows the context zone", async ({ page, request }) => {
    await bootstrapActiveProject(page, request);
    await expect(page.getByTestId("project-context-zone")).toBeVisible();
    await expect(page.getByTestId("project-files-entry")).toBeEnabled();
  });

  webE2e("context zone opens project-scoped AI providers editor", async ({ page, request }) => {
    await bootstrapActiveProject(page, request);
    await page.getByTestId("project-configuration-entry").click();
    await page.getByTestId("project-context-entry-providers").click();
    await expect(page.getByTestId("project-context-view")).toBeVisible();
    await expect(page.getByTestId("models-editor")).toBeVisible();
  });

  webE2e("global settings excludes moved project sections", async ({ page, request }) => {
    await bootstrapActiveProject(page, request);
    await page.getByRole("button", { name: "Settings" }).click();
    await expect(page.getByTestId("settings-nav-providers")).toBeVisible();
    await expect(page.getByTestId("settings-nav-approvals")).toBeVisible();
    await expect(page.getByTestId("settings-nav-debug")).toBeVisible();
    await expect(page.getByTestId("settings-nav-security")).toHaveCount(0);
    await expect(page.getByTestId("settings-nav-revoke-approvals")).toHaveCount(0);
    await expect(page.getByTestId("settings-nav-models")).toHaveCount(0);
  });

  webE2e("folders panel renames root label and sets primary", async ({ page, request }) => {
    const name = e2eUniqueLabel("Multi Root");
    const project = await apiCreateProjectWithRoot(request, E2E_FIXTURE_PROJECT, name);
    const secondRoot = e2eTempDir(request, "project-root");
    const updated = await apiAttachProjectRoot(request, project.id, secondRoot, "secondary");
    const secondary = updated.roots.find((r) => (r.label ?? "") === "secondary");
    expect(secondary?.id).toBeTruthy();

    await gotoShell(page);
    await page.getByTestId("home-nav-all").click();
    const row = await findProjectListRow(page, project.id);
    await expect(row).toBeVisible();
    await row.getByTestId(`project-list-menu-${project.id}`).click();
    await page.getByTestId(`project-list-attach-${project.id}`).click();
    await expect(page.getByTestId("workspace-folders-panel")).toBeVisible({
      timeout: 15_000,
    });

    const renameTrigger = page.getByTestId(
      `project-root-rename-trigger-${secondary!.id}`,
    );
    await expect(renameTrigger).toHaveText("secondary");
    await renameTrigger.click();
    const input = page.getByTestId("project-root-rename-input");
    await expect(input).toBeVisible();
    await fillSolidControl(input, "docs");
    await input.press("Enter");
    await expect(
      page.getByTestId(`project-root-rename-trigger-${secondary!.id}`),
    ).toHaveText("docs", { timeout: 15_000 });

    await page.getByTestId(`workspace-folder-set-main-${secondary!.id}`).click();
    await expect(
      page.getByTestId(`workspace-folder-row-${secondary!.id}`),
    ).toContainText("Main folder", { timeout: 15_000 });

    // Home paints ready only after the registry re-reads, as gotoShell waits.
    const relisted = waitForProjectList(page);
    await page.reload();
    await expectShellReady(page);
    await relisted;
    await page.getByTestId("home-nav-all").click();
    await expect(page.getByTestId("home-view")).toHaveAttribute("data-boot", "ready");
    const rowAfter = await findProjectListRow(page, project.id);
    await rowAfter.getByTestId(`project-list-menu-${project.id}`).click();
    await page.getByTestId(`project-list-attach-${project.id}`).click();
    await expect(
      page.getByTestId(`project-root-rename-trigger-${secondary!.id}`),
    ).toHaveText("docs", { timeout: 15_000 });
    await expect(
      page.getByTestId(`workspace-folder-row-${secondary!.id}`),
    ).toContainText("Main folder");
  });
});
