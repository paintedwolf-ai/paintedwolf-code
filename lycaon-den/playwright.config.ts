import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "@playwright/test";

const tier = process.env.PLAYWRIGHT_E2E;
const runWeb = tier === "web";
const runDesktop = tier === "desktop";
const runE2E = runWeb || runDesktop;
const webBrowser = process.env.PLAYWRIGHT_WEB_BROWSER ?? "chromium";
if (webBrowser !== "chromium" && webBrowser !== "webkit") {
  throw new Error("PLAYWRIGHT_WEB_BROWSER must be chromium or webkit");
}

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const scripts = path.join(repoRoot, "scripts");

const baseURL = process.env.LYCAON_E2E_BASE_URL ?? "http://127.0.0.1:1420";

const tauriStack = {
  command: `bash ${path.join(scripts, "e2e-tauri-dev.sh")}`,
  url: baseURL,
  reuseExistingServer: process.env.PLAYWRIGHT_REUSE_SERVER === "1",
  timeout: 600_000,
  stdout: "pipe" as const,
  stderr: "pipe" as const,
  // The script runs Tauri in its own process group; SIGTERM lets its trap stop
  // that group and the sidecar, which a group SIGKILL would orphan.
  gracefulShutdown: { signal: "SIGTERM" as const, timeout: 15_000 },
};

export default defineConfig({
  testDir: "./e2e",
  outputDir: process.env.LYCAON_E2E_OUTPUT_DIR ?? "./test-results",
  timeout: 120_000,
  retries: runE2E ? 1 : 0,
  workers: 1,
  reporter: runE2E ? "list" : "dot",
  use: {
    baseURL,
    trace: "on-first-retry",
  },
  webServer: runDesktop ? tauriStack : undefined,
  projects: [
    {
      name: "web",
      use: { browserName: webBrowser },
      testMatch: "**/*.spec.ts",
      testIgnore: "**/*.desktop.spec.ts",
    },
    {
      name: "desktop",
      testMatch: [
        "**/*.desktop.spec.ts",
        "**/boot.spec.ts",
      ],
    },
  ],
});
