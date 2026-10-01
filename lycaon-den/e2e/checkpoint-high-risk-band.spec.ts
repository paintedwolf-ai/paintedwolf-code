import { mkdirSync } from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

const screenshotDir = path.resolve(
  import.meta.dirname,
  "../test-results/approval-consequence-band-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

/**
 * High-risk + standard approval plans (separate chats): host band chrome,
 * greyscale-identifiable treatment, identical controls, empty dock.
 * Keybinding / structural parity is pinned in ApprovalCard unit tests.
 */
webE2e(
  "den:harness — high-risk vs standard approval plans (parity)",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionHi = await openNewChatSession(page);

    const injectHi = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionHi,
        command: "install helper",
        consequence_band: "high_risk",
        consequence_code: "write_root",
      },
    });
    expect(injectHi.ok(), JSON.stringify(await injectHi.json())).toBeTruthy();

    const high = liveChatStage(page).locator(
      '[data-testid="tool-approval-card"][data-consequence-band="high_risk"]',
    );
	await expect(high.getByTestId("approval-approve-primary")).toBeVisible({
      timeout: 60_000,
    });
    await expect(high.getByTestId("approval-high-risk-label")).toContainText("High risk");
    await expect(high.getByTestId("approval-high-risk-consequence")).toContainText(
      "programs are launched",
    );
    await expect(high.getByTestId("approval-no")).toBeVisible();
    await expect(page.getByTestId("system-nudge")).toHaveCount(0);

    await page.evaluate(() => {
      document.documentElement.style.filter = "grayscale(1)";
    });
    await page.evaluate(() => {
      document.documentElement.style.filter = "";
    });

    const sessionStd = await openNewChatSession(page);
    expect(sessionStd).not.toBe(sessionHi);

    const injectStd = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionStd,
        command: "go get example.com/mod",
      },
    });
    expect(injectStd.ok(), JSON.stringify(await injectStd.json())).toBeTruthy();

    const standard = liveChatStage(page).locator('[data-testid="tool-approval-card"]');
	await expect(standard.getByTestId("approval-approve-primary")).toBeVisible({
      timeout: 60_000,
    });
    await expect(standard).toHaveAttribute("data-consequence-band", "standard");
    await expect(standard.getByTestId("approval-high-risk-label")).toHaveCount(0);
    await expect(standard.getByTestId("approval-no")).toBeVisible();
    await expect(page.getByTestId("system-nudge")).toHaveCount(0);

  },
);
