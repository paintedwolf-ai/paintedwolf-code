import { expect } from "@playwright/test";
import {
  apiEnsureDefaultModel,
  apiConfig,
  bootstrapChatSession,
  seedFirstRunUnset,
  sendChatPrompt,
  webE2e,
} from "./helpers.ts";

/**
 * App-level half of the clean-Mac release gate; the install half is the manual
 * procedure in docs/dev-tasks.md § Clean-Mac validation.
 */
webE2e.describe("first-run golden path", () => {
  webE2e("onboarding gates until a provider is configured", async ({ page, request }) => {
    await apiEnsureDefaultModel(request);
    await seedFirstRunUnset(page);
    await page.goto("/");

    // The gate is the hard stop a first-run user meets before anything else.
    await expect(page.getByTestId("onboarding-gate")).toBeVisible({
      timeout: 60_000,
    });
  });

  webE2e("attach a project, send one turn, and see a result", async ({ page }) => {
    await bootstrapChatSession(page);

    await sendChatPrompt(page, "List one file in the repo root");

    // A real turn, not just an echoed user row: the assistant span appears.
    await expect(page.getByTestId("den-chat-span-implement")).toHaveCount(1, {
      timeout: 120_000,
    });
  });

  webE2e("readiness is clean or explained, never silent", async ({ page, request }) => {
    await bootstrapChatSession(page);

    const { token } = apiConfig();
    const res = await request.get("/v1/preflight", {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.ok()).toBe(true);

    const report = (await res.json()) as {
      overall: string;
      probes: { id: string; status: string; code?: string; message?: string }[];
    };
    expect(["ok", "degraded", "blocked"]).toContain(report.overall);
    expect(report.probes.length).toBeGreaterThan(0);

    // A clean Mac without Xcode Command Line Tools is a normal first run, so
    // non-ok probes pass when each carries a code and copy.
    for (const probe of report.probes) {
      if (probe.status === "ok") continue;
      expect(probe.code, `probe ${probe.id} is ${probe.status} with no code`).toBeTruthy();
      expect(probe.message, `probe ${probe.id} has a code but no copy`).toBeTruthy();
    }

    // And the same facts reach the user, not just the API.
    if (report.overall !== "ok") {
      await page.evaluate(async () => {
        await (
          window as unknown as {
            __harness: { goHome(): Promise<unknown> };
          }
        ).__harness.goHome();
      });
      await expect(page.getByTestId("preflight-nudge")).toBeVisible({
        timeout: 30_000,
      });
    }
  });
});
