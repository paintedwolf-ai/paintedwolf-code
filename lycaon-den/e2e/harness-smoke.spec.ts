import { expect } from "@playwright/test";

import { webE2e } from "./helpers.ts";

webE2e("den:harness — shell boots and reaches sidecar same-origin", async ({ page }) => {
  const apiRequests: string[] = [];
  page.on("request", (req) => {
    const url = new URL(req.url());
    if (url.pathname.startsWith("/v1/") || url.pathname === "/health") {
      apiRequests.push(url.origin);
    }
  });

  await page.goto("/");

  await expect(page.getByTestId("shell")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("Harness").first()).toBeVisible({ timeout: 30_000 });

  expect(apiRequests.length).toBeGreaterThan(0);
  const pageOrigin = new URL(page.url()).origin;
  for (const origin of apiRequests) {
    expect(origin).toBe(pageOrigin);
  }
});

webE2e("den:harness — window.__harness drives a chat end-to-end", async ({ page }) => {
  await page.goto("/");

  await page.waitForFunction(
    () => (window as unknown as { __harness?: { state(): { connected: boolean } } }).__harness?.state().connected === true,
    undefined,
    { timeout: 60_000 },
  );

  const transcript = await page.evaluate(async () => {
    const h = (
      window as unknown as {
        __harness: {
          openProject: (name: string) => Promise<unknown>;
          newSession: () => Promise<unknown>;
          prompt: (text: string) => Promise<unknown>;
          transcript: () => unknown;
          llm: { auto: (text: string) => Promise<unknown> };
        };
      }
    ).__harness;
    // Per-action assertions identify failures before transcript checks.
    for (const [label, step] of [
      ["openProject", () => h.openProject("Harness")],
      ["newSession", () => h.newSession()],
      ["llm.auto", () => h.llm.auto("Harness response received.")],
      ["prompt", () => h.prompt("hello from the harness driver")],
    ] as const) {
      const r = (await step()) as { ok?: boolean };
      if (!r.ok) throw new Error(`__harness.${label} failed: ${JSON.stringify(r)}`);
    }
    return h.transcript() as unknown as { text: string };
  });

  expect(transcript.text).toContain("hello from the harness driver");
});
