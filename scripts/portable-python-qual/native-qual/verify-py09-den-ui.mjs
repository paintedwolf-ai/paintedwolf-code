#!/usr/bin/env node
/**
 * Drives the real lycaon-den SolidJS UI in headless Chromium via Playwright
 * against a restarted (reopened) `pw serve` sidecar, verifying:
 *   1. Den view renders the reopened session's native Python activity & output.
 *   2. Files view renders and opens `py09-den-witness.txt` with its settled content.
 * Captures screenshots and full rendered DOM text for both views.
 */
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

function arg(name) {
  const idx = process.argv.indexOf(name);
  if (idx === -1 || idx + 1 >= process.argv.length) {
    throw new Error(`missing required arg ${name}`);
  }
  return process.argv[idx + 1];
}

const uiUrl = arg("--ui-url");
const projectId = arg("--project-id");
const sessionId = arg("--session-id");
const executionId = arg("--execution-id");
const outDir = arg("--out-dir");

const browser = await chromium.launch({ headless: true });
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  // Seed onboarding complete so the app opens directly into the workspace shell.
  await page.addInitScript(({ pid, sid }) => {
    const prefix = "paintedwolf:app-state:v1:";
    localStorage.setItem(
      prefix + encodeURIComponent("onboarding"),
      JSON.stringify({ completedSetupRevision: 2, completedLocalModelStepRevision: 2 }),
    );
    localStorage.setItem(prefix + encodeURIComponent("firstTimeTips"), JSON.stringify({ enabled: false }));
    localStorage.setItem(
      prefix + encodeURIComponent("navigation"),
      JSON.stringify({ activeProjectId: pid, activeSessionId: sid }),
    );
  }, { pid: projectId, sid: sessionId });

  await page.goto(uiUrl, { waitUntil: "domcontentloaded", timeout: 60_000 });
  await page.waitForTimeout(3000);

  // Dismiss 2-step welcome wizard if shown, choosing "Files and chat" so both Files & Den are open.
  for (let step = 0; step < 4; step += 1) {
    const txt = await page.locator("body").innerText();
    if (txt.includes("How do you want to work?")) {
      const filesAndChat = page.locator("button, [role='radio'], label").filter({ hasText: /Files and chat/i }).first();
      if (await filesAndChat.count()) {
        await filesAndChat.click({ timeout: 5_000 }).catch(() => {});
        await page.waitForTimeout(400);
      }
      const cont = page.locator("button").filter({ hasText: /^Continue$/i }).first();
      if (await cont.count()) {
        await cont.click({ timeout: 5_000 }).catch(() => {});
        await page.waitForTimeout(1500);
        continue;
      }
    }
    const skipOrDone = page.locator("button").filter({
      hasText: /^(Skip|Skip for now|Not now|Continue|Done|Finish|Get started|Start working|Open workspace)$/i,
    }).first();
    if (await skipOrDone.count() && (txt.includes("2 / 2") || txt.includes("1 / 2") || txt.includes("Local model") || txt.includes("Welcome"))) {
      await skipOrDone.click({ timeout: 5_000 }).catch(() => {});
      await page.waitForTimeout(1500);
      continue;
    }
    break;
  }

  // Click the reopened project/session in the sidebar if not already active.
  const bodyInitial = await page.locator("body").innerText();
  if (!bodyInitial.includes("PY09 witness completed") && !bodyInitial.includes("py09_witness.py")) {
    const projBtn = page.locator("button, a, [role='button'], [role='treeitem']").filter({
      hasText: /py09-den-reopen/i,
    }).first();
    if (await projBtn.count()) {
      await projBtn.click({ timeout: 10_000 }).catch(() => {});
      await page.waitForTimeout(1500);
    }
    const sessBtn = page.locator("button, a, [role='button'], [role='treeitem']").filter({
      hasText: /Run py09_witness|PY09 witness|py09_witness/i,
    }).first();
    if (await sessBtn.count()) {
      await sessBtn.click({ timeout: 10_000 }).catch(() => {});
      await page.waitForTimeout(2500);
    }
  }

  // Wait up to 20s for the reopened session's Python activity to render in Den.
  const denDeadline = Date.now() + 20_000;
  let denText = "";
  while (Date.now() < denDeadline) {
    denText = await page.locator("body").innerText();
    if (
      denText.includes("PY09 witness completed") ||
      denText.includes("py09_witness.py") ||
      denText.includes("PY09_RETAINED_STDOUT_BEGIN")
    ) {
      break;
    }
    await page.waitForTimeout(1000);
  }
  await page.screenshot({ path: path.join(outDir, "den-reopened-session.png"), fullPage: true });
  fs.writeFileSync(path.join(outDir, "den-reopened-dom.txt"), denText, "utf-8");

  const denHasActivity =
    denText.includes("PY09 witness completed") ||
    denText.includes("py09_witness.py") ||
    denText.includes("PY09_RETAINED_STDOUT_BEGIN") ||
    denText.includes(executionId);

  // Switch to Files surface and open py09-den-witness.txt.
  const filesNav = page.locator("button, a, [role='tab'], [role='button']").filter({
    hasText: /^Files$/i,
  }).first();
  if (await filesNav.count()) {
    await filesNav.click({ timeout: 10_000 }).catch(() => {});
    await page.waitForTimeout(2500);
  }

  const fileRow = page.locator('[data-testid="files-tree-file"][data-path="py09-den-witness.txt"]').first();
  if (await fileRow.count()) {
    await fileRow.click({ timeout: 10_000 }).catch(() => {});
    await page.waitForTimeout(2500);
  } else {
    const fallbackRow = page.locator("button, [role='treeitem'], span, div").filter({
      hasText: /^py09-den-witness\.txt$/,
    }).first();
    if (await fallbackRow.count()) {
      await fallbackRow.click({ timeout: 10_000 }).catch(() => {});
      await page.waitForTimeout(2500);
    }
  }

  const filesText = await page.locator("body").innerText();
  await page.screenshot({ path: path.join(outDir, "files-reopened-artifact.png"), fullPage: true });
  fs.writeFileSync(path.join(outDir, "files-reopened-dom.txt"), filesText, "utf-8");

  const filesHasArtifactName = filesText.includes("py09-den-witness.txt");
  const filesHasArtifactContent = filesText.includes("PY09_DEN_WITNESS_OK_NATIVE_AOT");

  const verdict = {
    ui_runtime: "lycaon-den SolidJS web UI rendered in headless Chromium via Playwright against reopened pw serve sidecar",
    den_reopened_has_python_activity: denHasActivity,
    den_screenshot: "den-reopened-session.png",
    den_dom_file: "den-reopened-dom.txt",
    files_reopened_has_artifact_name: filesHasArtifactName,
    files_reopened_has_artifact_content: filesHasArtifactContent,
    files_screenshot: "files-reopened-artifact.png",
    files_dom_file: "files-reopened-dom.txt",
  };
  fs.writeFileSync(path.join(outDir, "ui-reopen-verdict.json"), JSON.stringify(verdict, null, 2) + "\n", "utf-8");
  console.log("UI_REOPEN_VERDICT:", JSON.stringify(verdict));

  if (!denHasActivity || !filesHasArtifactName) {
    process.exitCode = 1;
  }
} finally {
  await browser.close();
}
