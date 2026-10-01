import path from "node:path";
import { fileURLToPath } from "node:url";
import { mkdirSync } from "node:fs";
import { expect } from "@playwright/test";
import { expectShellReady, webE2e } from "./helpers.ts";

const screenshotDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results/shortcut-help-overlay-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

const SEARCH_BINDING_ID = "painted-wolf/platform:key-search-open";

webE2e("den:harness — shortcut help overlay light/dark", async ({ page }) => {
  await page.goto("/");

  await expectShellReady(page);
  await expect(page.getByTestId("shell-nav-dock")).toBeVisible({
    timeout: 30_000,
  });
  await page.getByTestId("shell-nav-dock").click();

  await page.keyboard.press("?");
  const overlay = page.getByTestId("shortcut-help-overlay");
  await expect(overlay).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId(`shortcut-help-row-${SEARCH_BINDING_ID}`)).toBeVisible();
  await expect(page.getByTestId(`shortcut-help-chord-${SEARCH_BINDING_ID}`)).toContainText(
    /⌘K|Ctrl\+K/,
  );

  await page.keyboard.press("Escape");
  await expect(overlay).toBeHidden();

  await page.keyboard.press("?");
  await expect(overlay).toBeVisible();
  await page.getByTestId("shortcut-help-open-settings").click();
  await expect(overlay).toBeHidden();
  await expect(page.getByTestId("keyboard-settings-panel")).toBeVisible({
    timeout: 15_000,
  });
});
