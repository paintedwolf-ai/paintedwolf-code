import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("settings", () => {
  webE2e("AI providers lives in Settings and project configuration", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    await expect(page.getByTestId("settings-nav-providers")).toBeVisible();
    await page.getByTestId("settings-nav-providers").click();
    await expect(page.getByTestId("providers-settings")).toBeVisible();
    await page.getByTestId("project-configuration-entry").click();
    await page.getByTestId("project-context-entry-providers").click();
    await expect(page.getByTestId("models-editor")).toBeVisible();
  });

  webE2e("Settings Scanners hosts security main and engine list", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByTestId("settings-nav-scanners").click();
    await expect(page.getByTestId("scanners-settings-panel")).toBeVisible();
    await expect(page.getByTestId("scanners-main")).toBeVisible();
    await expect(page.getByTestId("scanners-list")).toBeVisible();
  });

  webE2e("advanced budgets tab is device-global", async ({ page }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByTestId("settings-nav-debug").click();
    await page.getByTestId("advanced-tab-budgets").click();
    await expect(page.getByTestId("budgets-editor")).toBeVisible();
    await expect(page.getByTestId("project-context-entry-budgets")).toHaveCount(0);
  });

  webE2e("advanced diagnostics describes desktop-only shell integration in web mode", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    await page.getByTestId("settings-nav-debug").click();
    await page.getByTestId("advanced-tab-diagnostics").click();
    await expect(page.getByTestId("shell-command-panel")).toBeVisible();
    await expect(page.getByTestId("shell-command-unavailable")).toContainText(
      "desktop app",
    );
    await expect(page.getByTestId("shell-command-error")).toHaveCount(0);
  });

  webE2e("web research tab shows local index stats and engine health", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    // The settings takeover animates in over the shell nav dock; clicking the
    // nav item before the view settles gets intercepted by the dock overlay.
    await expect(page.getByTestId("settings-view")).toBeVisible();
    await page.getByTestId("settings-nav-web-research").click();
    await expect(page.getByTestId("web-research-panel-providers")).toBeVisible();
    // Providers use main-detail SettingsList chrome: the add control lives in
    // the list chrome, and the direct card renders after selecting its row.
    await expect(page.getByTestId("web-research-add")).toBeVisible();
    await page.getByTestId("web-research-row-direct").click();
    const directCard = page.getByTestId("web-research-direct-card");
    await expect(directCard).toBeVisible();
    await expect(directCard).toContainText("Local index");
    await expect(directCard).toContainText("Optional");
    await expect(page.getByTestId("web-research-domain-guess-toggle")).toBeVisible();
    await expect(page.getByTestId("web-research-domain-guess-toggle")).toBeChecked();
    await expect(directCard).toContainText("Guess domains");
    await expect(page.getByTestId("web-research-remove-direct")).toBeVisible();

    await expect(page.getByTestId("web-research-enabled-toggle")).toBeVisible();
    await expect(page.getByTestId("web-research-enabled-toggle")).toBeChecked();

    await page.getByTestId("web-research-tab-index").click();
    await expect(page.getByTestId("web-research-panel-index")).toBeVisible();
    await expect(page.getByTestId("web-research-index-section")).toBeVisible();
    await expect(page.getByTestId("web-research-index-stats")).toBeVisible();
    const health = page.getByTestId("web-research-index-health");
    await expect(health).toBeVisible();
    await expect(health).toContainText("writes");
    await expect(health).toContainText("search");
    await expect(page.getByTestId("web-research-enabled-toggle")).toBeHidden();
  });

  webE2e("web research main toggle offs disables details sub-panel", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings" }).click();
    await expect(page.getByTestId("settings-view")).toBeVisible({ timeout: 15_000 });
    // Bring the navigation item into view before clicking it.
    await page
      .getByTestId("settings-nav-web-research")
      .evaluate((el) => el.scrollIntoView({ block: "center" }));
    await page.getByTestId("settings-nav-web-research").click();
    const toggle = page.getByTestId("web-research-enabled-toggle");
    await expect(toggle).toBeVisible({ timeout: 15_000 });
    if (!(await toggle.isChecked())) {
      await toggle.click();
    }
    await expect(toggle).toBeChecked();
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    await expect(page.getByTestId("web-research-disabled-note")).toBeVisible();
    await expect(page.getByTestId("web-research-details")).toHaveAttribute("disabled", "");
    await page.getByTestId("web-research-tab-index").click();
    await expect(page.getByTestId("web-research-warming-toggle")).toBeDisabled();
    await page.getByTestId("web-research-tab-providers").click();
    await toggle.click();
    await expect(toggle).toBeChecked();
    await expect(page.getByTestId("web-research-details")).not.toHaveAttribute("disabled");
  });
});
