import { expect, type Page } from "@playwright/test";
import type { CostSummary, Error as WireError, SettingsLimitsPatch } from "../src/api/types.ts";
import type { NoticeScope } from "../src/notices/notice-scope.ts";
import {
  apiConfig,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

type HarnessWindow = {
  __harness?: {
    state(): { sessionId?: string | null };
    publishNotice(
      input: { code?: string; title?: string; message: string },
      scope?: NoticeScope,
    ): { ok?: boolean; error?: string };
    click(testidOrSelector: string): Promise<{ ok?: boolean; error?: string }>;
  };
};

async function updateSpendCeiling(
  request: NonNullable<Parameters<typeof bootstrapChatSession>[1]>,
  body: { session_spend_ceiling_usd: number; spend_ceiling_enabled: boolean },
  projectId?: string,
) {
  const { apiUrl, token } = apiConfig();
  const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
  const updateRes = await request.patch(`${apiUrl}/v1/settings/limits${query}`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: {
      session_spend_ceiling_nano_usd: Math.round(body.session_spend_ceiling_usd * 1_000_000_000),
      spend_ceiling_enabled: body.spend_ceiling_enabled,
    },
  });
  expect(updateRes.ok(), await updateRes.text()).toBeTruthy();
}

async function enableCostTracking(
  request: NonNullable<Parameters<typeof bootstrapChatSession>[1]>,
) {
  const { apiUrl, token } = apiConfig();
  const response = await request.patch(`${apiUrl}/v1/settings/pricing`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: {
      cost_tracking_enabled: true,
      sources: [
        { id: "models-dev", enabled: true },
        { id: "litellm", enabled: false },
        { id: "ai-pricing-fyi", enabled: false },
      ],
    },
  });
  expect(response.ok(), await response.text()).toBeTruthy();
}

function costSummaryFixture(opts: {
  estimated_usd: number;
  priced: boolean;
}): CostSummary {
  const nano = Math.round(opts.estimated_usd * 1e9);
  return {
    scope: "session",
    estimated_nano_usd: nano,
    estimate_coverage: opts.priced ? "complete" : "unpriced",
    unpriced_tokens: opts.priced ? 0 : 15,
    pricing_provenance: [],
    token_totals: { prompt: opts.priced ? 1 : 10, completion: opts.priced ? 1 : 5 },
    coordinator: {
      estimated_nano_usd: nano,
      token_totals: { prompt: opts.priced ? 1 : 10, completion: opts.priced ? 1 : 5 },
    },
    workers: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
      task_count: 0,
    },
    summarizer: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
    },
  };
}

async function openBudgetsEditor(
  page: Page,
  scope: { projectId: string; sessionId: string },
) {
  await page.waitForFunction(
    ({ sessionId }) =>
      (window as unknown as HarnessWindow).__harness?.state().sessionId ===
      sessionId,
    scope,
    { timeout: 30_000 },
  );
  const published = await page.evaluate(
    ({ projectId, sessionId }) =>
      (window as unknown as HarnessWindow).__harness?.publishNotice(
        {
          code: "session_spend_ceiling_reached",
          title: "Spend ceiling reached",
          message: "Open budgets to review this session's spend ceiling.",
        },
        { kind: "session", projectId, sessionId },
      ),
    scope,
  );
  expect(published?.ok).toBe(true);
  await expect(page.getByTestId("spend-ceiling-reached-nudge")).toBeVisible({
    timeout: 15_000,
  });
  await page.getByTestId("system-nudge-secondary").click();
  await expect(page.getByTestId("budgets-editor")).toBeVisible({
    timeout: 30_000,
  });
}

webE2e.describe("spend safety ceiling den", () => {
  webE2e(
    "Budgets enable + unpriced readout says not enforced",
    async ({ page, request }) => {
      await page.route("**/v1/cost/summary**", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          json: costSummaryFixture({ estimated_usd: 0, priced: false }),
        });
      });

      await enableCostTracking(request!);
      const project = await bootstrapChatSession(page, request);
      await updateSpendCeiling(request!, {
        session_spend_ceiling_usd: 5,
        spend_ceiling_enabled: true,
      });
      await updateSpendCeiling(request!, {
        session_spend_ceiling_usd: 5,
        spend_ceiling_enabled: true,
      }, project.id);
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });

      const sessionId = await settledChatSessionId(page);
      await openBudgetsEditor(page, { projectId: project.id, sessionId });
      await expect(page.getByTestId("spend-ceiling-enabled")).toBeChecked({
        timeout: 15_000,
      });
      const readout = page.getByTestId("spend-ceiling-readout");
      await expect(readout).toHaveAttribute("data-kind", "unpriced");
      await expect(readout).toContainText("not enforced (pricing unavailable)");
      await expect(readout).not.toContainText(/lycaon/i);
    },
  );

  for (const failureStage of ["none", "limits", "resume"] as const) {
    webE2e(
      `reached card raises the ceiling and continues after ${failureStage} failure`,
      async ({ page, request }) => {
        await enableCostTracking(request!);
        const project = await bootstrapChatSession(page, request);
        const sessionId = await settledChatSessionId(page);
        expect(sessionId).toBeTruthy();

        await updateSpendCeiling(request!, {
          session_spend_ceiling_usd: 5,
          spend_ceiling_enabled: true,
        });
        await updateSpendCeiling(request!, {
          session_spend_ceiling_usd: 5,
          spend_ceiling_enabled: true,
        }, project.id);

        await page.route("**/v1/cost/summary**", async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            json: costSummaryFixture({ estimated_usd: 5.12, priced: true }),
          });
        });

        await page.waitForFunction(
          () =>
            (window as unknown as HarnessWindow).__harness != null,
          undefined,
          { timeout: 30_000 },
        );
        const published = await page.evaluate(
          ({ projectId, sessionId }) => {
            const h = (window as unknown as HarnessWindow).__harness;
            return h?.publishNotice(
              {
                code: "session_spend_ceiling_reached",
                title: "Spend ceiling reached",
                message:
                  "This session stopped at its spend ceiling ($5.00). It has spent about $5.12.",
              },
              { kind: "session", projectId, sessionId },
            );
          },
          { projectId: project.id, sessionId },
        );
        expect(published?.ok).toBe(true);

        const nudge = page.getByTestId("spend-ceiling-reached-nudge");
        await expect(nudge).toBeVisible({ timeout: 15_000 });
        await expect(nudge).toContainText("Spend ceiling reached");
        await expect(nudge).not.toContainText(/lycaon/i);

        let failedOnce = false;
        let limitWrites = 0;
        let continuationRequests = 0;
        await page.route("**/v1/settings/limits**", async (route) => {
          if (route.request().method() === "PATCH") {
            limitWrites += 1;
            if (failureStage === "limits" && !failedOnce) {
              failedOnce = true;
              await route.fulfill({ status: 500, json: { message: "Unavailable", code: "internal_error" } satisfies WireError });
              return;
            }
          }
          await route.continue();
        });
        await page.route(`**/v1/sessions/${sessionId}/prompts`, async (route) => {
          continuationRequests += 1;
          if (failureStage === "resume" && !failedOnce) {
            failedOnce = true;
            await route.fulfill({ status: 500, json: { message: "Unavailable", code: "internal_error" } satisfies WireError });
            return;
          }
          await route.continue();
        });

        const resumeWait = page.waitForRequest(
          (req) => req.method() === "POST" && req.url().endsWith(`/v1/sessions/${sessionId}/prompts`),
        );
        const updateWait = page.waitForRequest(
          (req) =>
            req.method() === "PATCH" &&
            req.url().includes("/v1/settings/limits") &&
            !new URL(req.url()).searchParams.has("project_id"),
        );
        await page.getByTestId("system-nudge-primary").click();
        let writesBeforeRetry: number | undefined;
        if (failureStage !== "none") {
          await expect(nudge.getByRole("alert")).toContainText(failureStage === "resume"
            ? "work could not resume" : "Could not raise the ceiling");
          await expect(nudge.getByTestId("system-nudge-primary")).toBeEnabled();
          writesBeforeRetry = limitWrites;
          if (failureStage === "resume") await expect(nudge.getByTestId("system-nudge-primary")).toHaveText("Resume");
          else expect(continuationRequests).toBe(0);
          await nudge.getByTestId("system-nudge-primary").click();
        }
        const updateReq = await updateWait;
        const updateBody = updateReq.postDataJSON() as SettingsLimitsPatch;
        expect(updateBody.spend_ceiling_enabled).toBe(true);
        expect(updateBody.session_spend_ceiling_nano_usd).toBeGreaterThan(5 * 1_000_000_000);

        const resumed = (await resumeWait).postDataJSON() as { text: string };
        expect(resumed.text).toBe("The spending ceiling has been raised. Continue the current work from where it stopped, preserving completed work.");
        await expect(page.getByTestId("status-chip-cost")).toHaveAttribute(
          "data-ceiling-ratio", (5.12 / ((updateBody.session_spend_ceiling_nano_usd ?? 0) / 1_000_000_000)).toFixed(2),
        );
        await expect(nudge).toHaveCount(0, { timeout: 15_000 });
        expect(continuationRequests).toBe(failureStage === "resume" ? 2 : 1);
        if (failureStage === "resume") expect(limitWrites).toBe(writesBeforeRetry);

      },
    );

  }

  webE2e("Open budgets secondary navigates to Advanced Budgets", async ({
    page,
    request,
  }) => {
    const project = await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    await page.waitForFunction(
      () => (window as unknown as HarnessWindow).__harness != null,
      undefined,
      { timeout: 30_000 },
    );
    const published = await page.evaluate(
      ({ projectId, sessionId }) => {
        return (window as unknown as HarnessWindow).__harness?.publishNotice(
          {
            code: "session_spend_ceiling_reached",
            title: "Spend ceiling reached",
            message: "This session stopped at its spend ceiling ($5.00).",
          },
          { kind: "session", projectId, sessionId },
        );
      },
      { projectId: project.id, sessionId },
    );
    expect(published?.ok).toBe(true);
    await expect(page.getByTestId("spend-ceiling-reached-nudge")).toBeVisible({
      timeout: 15_000,
    });
    await page.getByTestId("system-nudge-secondary").click();
    await expect(page.getByTestId("settings-view")).toBeVisible({
      timeout: 30_000,
    });
    await expect(page.getByTestId("budgets-editor")).toBeVisible({
      timeout: 30_000,
    });
  });

  webE2e(
    "approaching warn chip, nudge, dismiss, and ceiling-off silence",
    async ({ page, request }) => {
      await page.route("**/v1/cost/summary**", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          json: costSummaryFixture({ estimated_usd: 4.25, priced: true }),
        });
      });
      await enableCostTracking(request!);
      await updateSpendCeiling(request!, {
        session_spend_ceiling_usd: 5,
        spend_ceiling_enabled: true,
      });
      const project = await bootstrapChatSession(page, request);
      await updateSpendCeiling(request!, {
        session_spend_ceiling_usd: 5,
        spend_ceiling_enabled: true,
      }, project.id);
      await page.reload();
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });

      const costChip = page.getByTestId("status-chip-cost");
      await expect(costChip).toBeVisible({ timeout: 15_000 });
      await expect(costChip).toHaveClass(/den-status-chip--warn/);

      const approaching = liveChatStage(page).getByTestId(
        "spend-ceiling-approaching-nudge",
      );
      await expect(approaching).toBeVisible({ timeout: 15_000 });
      await expect(approaching).toContainText("Approaching the spend ceiling");
      await expect(approaching).not.toContainText(/lycaon/i);
      await approaching.getByRole("button", { name: "Dismiss", exact: true }).click();
      await expect(approaching).toHaveCount(0, { timeout: 15_000 });

      await updateSpendCeiling(request!, {
        session_spend_ceiling_usd: 5,
        spend_ceiling_enabled: false,
      });
      await page.reload();
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
        timeout: 90_000,
      });
      const chipOff = page.getByTestId("status-chip-cost");
      await expect(chipOff).toBeVisible({ timeout: 15_000 });
      await expect(chipOff).not.toHaveClass(/den-status-chip--warn/);
      await expect(
        liveChatStage(page).getByTestId("spend-ceiling-approaching-nudge"),
      ).toHaveCount(0);
    },
  );
});
