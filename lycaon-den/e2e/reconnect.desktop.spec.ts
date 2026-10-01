import { execSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";
import { expectShellReady, seedAppState } from "./helpers.ts";

const scriptsDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../scripts",
);

function runScript(name: string) {
  execSync(`bash ${path.join(scriptsDir, name)}`, {
    stdio: "inherit",
    env: process.env,
  });
}

test.describe("reconnect desktop", () => {
  test("kill sidecar → offline → restart → connected", async ({ page }) => {
    await seedAppState(page);
    await page.goto("/");
    await expectShellReady(page);

    runScript("e2e-sidecar-stop.sh");
    const stop = page.getByTestId("critical-stop");
    await expect(stop).toBeVisible({ timeout: 30_000 });
    // The stage must report the backend, not a readiness probe it cannot reach.
    await expect(stop).toHaveAttribute("data-code", "offline");

    runScript("e2e-sidecar-bg.sh");
    await page.getByRole("button", { name: /^connect$/i }).click();
    await expectShellReady(page);
  });
});
