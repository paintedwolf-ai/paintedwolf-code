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
  test("kill sidecar → offline → restart → reconnected", async ({ page }) => {
    await seedAppState(page);
    await page.goto("/");
    await expectShellReady(page);

    const shell = page.getByTestId("shell");
    runScript("e2e-sidecar-stop.sh");
    // A window that already connected stays open offline instead of stopping.
    await expect(shell).toHaveAttribute("data-sidecar-status", /^(reconnecting|disconnected)$/, { timeout: 30_000 });
    await expect(page.getByTestId("critical-stop")).toHaveCount(0);

    runScript("e2e-sidecar-bg.sh");
    await expect(shell).toHaveAttribute("data-sidecar-status", "connected", { timeout: 60_000 });
    await expectShellReady(page);
  });
});
