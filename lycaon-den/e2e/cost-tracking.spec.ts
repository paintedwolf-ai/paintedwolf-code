import { expect } from "@playwright/test";
import {
  apiConfig,
  bootstrapChatSession,
  liveChatStage,
  webE2e,
} from "./helpers.ts";
import type { SettingsPricing } from "../src/api/types.ts";

async function updatePricingSettings(
  request: Parameters<typeof bootstrapChatSession>[1],
  body: SettingsPricing,
) {
  const req = request!;
  const { apiUrl, token } = apiConfig();
  const res = await req.patch(`${apiUrl}/v1/settings/pricing`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: body,
  });
  expect(res.ok()).toBeTruthy();
  return res.json();
}

webE2e.describe("cost tracking ui", () => {
  webE2e("saved project limits update the retained cost chip through settings events", async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    await page.getByTestId("project-cost-entry").click();
    const enabled = page.getByTestId("project-cost-guardrail-enabled");
    await expect(enabled).toBeVisible();
    if (!(await enabled.isChecked())) await enabled.check();
    await page.getByTestId("project-cost-ceiling-usd").fill("1.23");
    await page.getByTestId("project-cost-save-guardrails").click();
    await expect(page.getByTestId("status-chip-cost")).toContainText("$1.23");
    await page.getByTestId("project-cost-ceiling-usd").fill("1.24");
    await page.getByTestId("project-cost-save-guardrails").click();
    await expect(page.getByTestId("status-chip-cost")).toContainText("$1.24");
    await expect(page.getByTestId("status-chip-cost")).not.toContainText("$1.23");
  });

  webE2e("tracking on shows the composer chip and project Cost stage", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    await updatePricingSettings(request, {
      cost_tracking_enabled: true,
      sources: [
        { id: "models-dev", enabled: true },
        { id: "litellm", enabled: false },
        { id: "ai-pricing-fyi", enabled: false },
      ],
    });
    // Settings hydrate on connect; reload so Den picks up pricing overlay.
    await page.reload();
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    await expect(page.getByTestId("status-chip-cost")).toBeVisible({
      timeout: 30_000,
    });
    await page.getByTestId("status-chip-cost").click();
    await expect(page.getByTestId("status-popover-cost")).toBeVisible();
    await expect(page.getByTestId("cost-summary")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("status-popover-cost")).toHaveCount(0);

    await page.getByTestId("project-cost-entry").click();
    await expect(page.getByTestId("project-cost-view")).toBeVisible({
      timeout: 30_000,
    });
    await expect(page.getByTestId("project-cost-kpis")).toBeVisible();
    await expect(page.getByTestId("project-cost-sessions")).toBeVisible();
    const guardrails = page.getByTestId("project-cost-guardrails");
    await expect(guardrails).toBeVisible();
    const guardrailFields = guardrails.locator(
      ".project-cost-guardrails__fields > .den-field",
    );
    await expect(guardrailFields).toHaveCount(2);
    for (const field of await guardrailFields.all()) {
      const fieldBox = await field.boundingBox();
      const numberBox = await field.locator(".den-number-input").boundingBox();
      expect(fieldBox).not.toBeNull();
      expect(numberBox).not.toBeNull();
      expect(numberBox!.y).toBeGreaterThan(fieldBox!.y);
      expect(Math.abs(numberBox!.width - fieldBox!.width)).toBeLessThan(1);
    }
    await expect(page.getByTestId("project-cost-export")).toBeVisible();
  });

  webE2e("tracking off hides the chip but retains project Cost history", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    await updatePricingSettings(request, {
      cost_tracking_enabled: false,
      sources: [
        { id: "models-dev", enabled: false },
        { id: "litellm", enabled: false },
        { id: "ai-pricing-fyi", enabled: false },
      ],
    });
    await page.reload();
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    // The chip row itself stays (model/posture); only the cost chip is gated.
    await expect(page.getByTestId("composer-status-chips")).toBeVisible({
      timeout: 30_000,
    });
    await expect(page.getByTestId("status-chip-cost")).toHaveCount(0);
    await page.getByTestId("project-cost-entry").click();
    await expect(page.getByTestId("project-cost-view")).toBeVisible({
      timeout: 30_000,
    });
    await expect(page.getByTestId("project-cost-tracking-off")).toBeVisible();
    await expect(page.getByTestId("project-cost-guardrails")).toBeVisible();
  });
});
