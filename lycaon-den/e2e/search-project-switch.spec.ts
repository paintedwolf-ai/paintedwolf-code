import path from "node:path";
import { writeFileSync } from "node:fs";
import { expect } from "@playwright/test";
import {
  activateProject, apiCreateProjectWithRoot, e2eTempDir, gotoShell, webE2e,
} from "./helpers.ts";

webE2e("This project search follows workspace switches in both directions", async ({ page, request }) => {
  const projects = [];
  for (const name of ["first", "second"]) {
    const root = e2eTempDir(request, `search-${name}`);
    writeFileSync(path.join(root, `${name}.txt`), `${name} workspace search sentinel\n`);
    projects.push(await apiCreateProjectWithRoot(request, root));
  }
  await gotoShell(page);
  for (const [turn, index] of [0, 1, 0].entries()) {
    const project = projects[index]!;
    if (turn === 0) {
      await activateProject(page, project.id);

    } else {
      await page.getByTestId("project-switcher").click();
      await page.getByTestId(`project-launcher-row-${project.id}`).click();
    }
    await page.getByTestId("project-search-entry").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible();
    await expect(page.getByTestId("search-scope-control")).toContainText("This project");
    await page.getByTestId("search-query-input").fill("kind:code sentinel");
    const rows = page.getByTestId("search-result-row");
    await expect(rows).toHaveCount(1, { timeout: 30_000 });
    await expect(rows.first()).toContainText(index === 0 ? "first workspace" : "second workspace");
  }
});
