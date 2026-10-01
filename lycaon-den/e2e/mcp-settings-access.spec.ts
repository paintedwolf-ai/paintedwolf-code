import { expect } from "@playwright/test";
import { expectShellReady, webE2e } from "./helpers.ts";

webE2e.describe("MCP settings access", () => {
  webE2e("explains registration and exposes provider management", async ({
    page,
  }) => {
    await page.goto("/");
    await expectShellReady(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await expect(page.getByTestId("settings-view")).toBeVisible({
      timeout: 15_000,
    });
    const nav = page.getByTestId("settings-nav-mcp");
    await expect(nav).toBeVisible();
    await nav.scrollIntoViewIfNeeded();
    await nav.click();
    await expect(page.getByTestId("mcp-settings-panel")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("mcp-access-explainer")).toContainText(
      "Enabled registers the provider's tools",
    );
    await expect(page.getByTestId("mcp-add-provider")).toBeVisible();

    const firstServer = page
      .locator(
        "[data-testid^='mcp-provider-']:not([data-testid='mcp-provider-list']):not([data-testid='mcp-provider-count']):not([data-testid^='mcp-provider-detail-'])",
      )
      .first();
    if ((await firstServer.count()) === 0) {
      await expect(page.getByTestId("mcp-empty-providers")).toBeVisible();
      return;
    }
    await firstServer.click();
    const firstEnable = page.locator("[data-testid^='mcp-enable-']").first();
    await expect(firstEnable).toBeVisible();
  });
});
