import { expect } from "@playwright/test";
import {
  activateProject, apiCreateProjectWithRoot, e2eTempDir, gotoShell, webE2e,
} from "./helpers.ts";

for (const outcome of ["success", "failure"] as const) {
  webE2e(`workspace remains usable while trust inventory waits for ${outcome}`, async ({ page, request }) => {
    const project = await apiCreateProjectWithRoot(request, e2eTempDir(request, "trust-loading"));
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    await page.route(`**/v1/projects/${project.id}/trust`, async (route) => {
      await pending;
      if (outcome === "failure") {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({
          error: { code: "internal_error", message: "Trust inventory unavailable" },
        }) });
      } else {
        await route.continue();
      }
    });
    try {
      await gotoShell(page);
      await activateProject(page, project.id);
      await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true", { timeout: 15_000 });
      const chip = page.getByTestId("status-chip-trust");
      await expect(chip).toHaveText("Checking trust…");
      await expect(chip).toHaveAttribute("aria-busy", "true");
      await page.getByTestId("project-switcher").click();
      await expect(page.getByTestId("project-launcher")).toBeVisible();
      await page.getByRole("textbox", { name: "Search projects" }).press("Escape");

      release();
      await expect(chip).toHaveText(outcome === "success" ? "Nothing to trust" : "Trust unavailable");
      await expect(chip).not.toHaveAttribute("aria-busy", "true");
      await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true");
    } finally {
      release();
    }
  });
}
