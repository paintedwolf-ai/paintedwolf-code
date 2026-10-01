import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { openProjectFilesFixture, modelIndependentWebE2e } from "./helpers.ts";

modelIndependentWebE2e("viewport backpressure recovers while the editor remains usable", async ({ page, request }) => {
  const attempts = new Map<string, number[]>();
  await page.route("**/source/views/*/interests/*", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    const key = route.request().url();
    const lease = attempts.get(key) ?? [];
    attempts.set(key, lease);
    lease.push(Date.now());
    if (lease.length !== 1) return route.continue();
    await route.fulfill({
      status: 429,
      headers: { "Retry-After": "1" },
      contentType: "application/json",
      body: JSON.stringify({
        code: "rate_limited", message: "Temporarily busy", title: "Too many requests",
        retryable: true, tier: "non_catastrophic", scope: "session", resolution: "default",
        suggested_action: "Wait briefly and try again.",
      }),
    });
  });
  let source = "";
  await openProjectFilesFixture(page, request, {
    prefix: "viewport-backpressure",
    name: "Viewport backpressure",
    seed: (root) => {
      source = path.join(root, "editable.ts");
      writeFileSync(source, "export const original = 1;\n");
      for (let i = 0; i < 320; i++) {
        writeFileSync(path.join(root, `file-${String(i).padStart(3, "0")}.ts`), `export const value = ${i};\n`);
      }
    },
  });
  await expect.poll(() => [...attempts.values()].some((lease) => lease.length >= 2)).toBe(true);
  for (const lease of attempts.values()) {
    if (lease.length >= 2) expect(lease[1]! - lease[0]!).toBeGreaterThanOrEqual(900);
  }
  await page.getByTestId("files-tree-file").filter({ hasText: "editable.ts" }).click();
  const editor = page.getByTestId("files-editor-host").locator(".cm-content");
  await expect(editor).toHaveAttribute("contenteditable", "true");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+Home");
  await page.keyboard.insertText("// edited after backpressure\n");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(source, "utf8")).toContain("edited after backpressure");
});
