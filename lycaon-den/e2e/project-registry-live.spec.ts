import { expect } from "@playwright/test";
import {
  activateProject, apiAttachProjectRoot, apiConfig, apiCreateProjectWithRoot, apiRemoveProject, e2eTempDir,
  e2eUniqueLabel, gotoShell, webE2e,
} from "./helpers.ts";

webE2e("project registry mutations reach a window watching another project", async ({ page, request }) => {
  const foreground = await apiCreateProjectWithRoot(request, e2eTempDir(request, "registry-foreground"));
  await gotoShell(page);
  await activateProject(page, foreground.id);
  await page.getByTestId("project-switcher").click();
  await expect(page.getByTestId("project-launcher")).toBeVisible();

  const background = await apiCreateProjectWithRoot(
    request, e2eTempDir(request, "registry-background"), e2eUniqueLabel("Background"),
  );
  const row = page.getByTestId(`project-launcher-row-${background.id}`);
  await expect(row).toContainText(background.name ?? "", { timeout: 15_000 });

  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const renamed = e2eUniqueLabel("Renamed background");
  const patched = await request.patch(`${apiUrl}/v1/projects/${background.id}`, {
    headers, data: { name: renamed },
  });
  expect(patched.ok(), await patched.text()).toBeTruthy();
  await expect(row).toContainText(renamed);

  expect((await apiRemoveProject(request, background.id))?.project_state).toBe("deleted");
  await expect(row).toHaveCount(0);
  await expect(page.getByTestId("project-switcher")).toContainText(foreground.name ?? "");
});

webE2e("mounted Files follows root attachment, metadata changes, and removal", async ({ page, request }) => {
  const project = await apiCreateProjectWithRoot(request, e2eTempDir(request, "root-registry"));
  await gotoShell(page);
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();
  const tree = page.getByRole("navigation", { name: "Project files", exact: true });
  await expect(tree).toBeVisible();
  const attached = await apiAttachProjectRoot(request, project.id, e2eTempDir(request, "secondary-root"), "reference-notes");
  const secondary = attached.roots.find((root) => root.label === "reference-notes")!;
  await expect(tree.getByRole("button", { name: "@reference-notes", exact: true })).toBeVisible();

  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const rootUrl = `${apiUrl}/v1/projects/${project.id}/roots/${secondary.id}`;
  const renamed = await request.patch(rootUrl, { headers, data: { label: "reference-café" } });
  expect(renamed.ok(), await renamed.text()).toBeTruthy();
  await expect(tree.getByRole("button", { name: "@reference-café", exact: true })).toBeVisible();
  await expect(tree.getByRole("button", { name: "@reference-notes", exact: true })).toHaveCount(0);

  const primary = await request.patch(rootUrl, { headers, data: { is_primary: true } });
  expect(primary.ok(), await primary.text()).toBeTruthy();
  await tree.getByRole("button", { name: "@reference-café", exact: true }).click();
  await expect(page.getByRole("region", { name: "@reference-café", exact: true })).toContainText("Primary root");

  const removed = await request.delete(rootUrl, { headers });
  expect(removed.ok(), await removed.text()).toBeTruthy();
  await expect(tree.getByRole("button", { name: "@reference-café", exact: true })).toHaveCount(0);
  await expect(tree.getByRole("button", { name: `@${project.roots[0]!.label}`, exact: true })).toBeVisible();
});
