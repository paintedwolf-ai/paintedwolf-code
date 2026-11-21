import { expect, type Page } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  bootstrapActiveProject,
  e2eTempDir,
  expectShellReady,
  webE2e,
} from "./helpers.ts";
import type { ReviewSettingsResponse } from "../src/api/types.ts";

async function openEditReview(page: Page) {
  await page.getByTestId("project-configuration-entry").click();
  await page.getByTestId("project-context-entry-edit-review").click();
  await expect(page.getByTestId("edit-review-settings")).toBeVisible();
}

webE2e("an added edit review rule is saved for the project", async ({ page, request }) => {
  // Project review rules are written into the root's overlay, so use a scratch root.
  const project = await bootstrapActiveProject(page, request, e2eTempDir(request, "edit-review"));
  const { apiUrl, token } = apiConfig();
  const reviewPath = "infra/**";

  await openEditReview(page);
  await page.getByTestId("edit-review-add-rule").click();
  await page.getByTestId("edit-review-path-0").fill(reviewPath);
  await expect
    .poll(async () => {
      const res = await request.get(
        `${apiUrl}/v1/settings/review?project_id=${encodeURIComponent(project.id)}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      if (!res.ok()) return [];
      return ((await res.json()) as ReviewSettingsResponse).review_paths.map((rule) => rule.path);
    })
    .toContain(reviewPath);
  await expect(page.getByTestId("edit-review-saving")).toHaveCount(0);

  await page.reload();
  await expectShellReady(page);
  if (await page.getByTestId("home-nav").isVisible()) await activateProject(page, project.id);
  await openEditReview(page);
  await expect(page.getByTestId("edit-review-path-0")).toHaveValue(reviewPath);
});
