import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { openProjectFilesFixture, modelIndependentWebE2e } from "./helpers.ts";

modelIndependentWebE2e("viewport backpressure recovers while the editor remains usable", async ({ page, request }) => {
  const attempts = new Map<string, number>();
  // The page records when each interest write starts and is answered, on its own clock.
  await page.addInitScript(() => {
    const nativeFetch = window.fetch.bind(window);
    const puts: { url: string; startedAt: number; answeredAt?: number }[] = [];
    Object.assign(window, { __interestPuts: puts });
    window.fetch = async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
      if (method !== "PUT" || !/\/source\/views\/[^/]+\/interests\//.test(url)) return nativeFetch(input, init);
      const put: { url: string; startedAt: number; answeredAt?: number } = { url, startedAt: performance.now() };
      puts.push(put);
      const response = await nativeFetch(input, init);
      put.answeredAt = performance.now();
      return response;
    };
  });
  await page.route("**/source/views/*/interests/*", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    const key = route.request().url();
    const attempt = (attempts.get(key) ?? 0) + 1;
    attempts.set(key, attempt);
    if (attempt !== 1) return route.continue();
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
  await expect.poll(() => [...attempts.values()].some((attempt) => attempt >= 2)).toBe(true);
  const puts = await page.evaluate(() => (window as unknown as { __interestPuts: { url: string; startedAt: number; answeredAt?: number }[] }).__interestPuts);
  let replays = 0;
  for (const url of new Set(puts.map((put) => put.url))) {
    const [refused, replay] = puts.filter((put) => put.url === url);
    if (!refused?.answeredAt || !replay) continue;
    replays++;
    // Retry-After: 1 holds the replay at least a second past the refusal; performance.now is coarsened to 0.1 ms.
    expect(replay.startedAt - refused.answeredAt, url).toBeGreaterThanOrEqual(999.9);
  }
  expect(replays).toBeGreaterThan(0);
  await page.getByTestId("files-tree-file").filter({ hasText: "editable.ts" }).click();
  const editor = page.getByTestId("files-editor-host").locator(".cm-content");
  await expect(editor).toHaveAttribute("contenteditable", "true");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+Home");
  await page.keyboard.insertText("// edited after backpressure\n");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(source, "utf8")).toContain("edited after backpressure");
});
