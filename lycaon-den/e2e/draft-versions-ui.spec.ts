import path from "node:path";
import { fileURLToPath } from "node:url";
import { mkdirSync } from "node:fs";
import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";
import type { DraftVersionsResponse } from "../src/api/types.ts";

const screenshotDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results/draft-versions-ui-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

webE2e("draft versions UI — tab bar and withdrawn draft", async ({ page, request }) => {
  const slotId = "00000000-0000-4000-8000-000000000101";

  await page.route("**/v1/sessions/*/drafts/*/versions", async (route) => {
    await route.fulfill({
      json: {
        versions: [
          {
            version_index: 0,
            body: "First coordinator attempt.",
            outcome_code: "GUARD_CLOSEOUT_OPEN_PLAN",
            created_at: "2026-07-07T12:00:00Z",
          },
          {
            version_index: 1,
            body: "Second coordinator attempt.",
            outcome_code: "SYNTH_HANDLE_NOT_IN_LEGS",
            created_at: "2026-07-07T12:01:00Z",
          },
        ],
      } satisfies DraftVersionsResponse,
    });
  });

  const fixtureMessages = transcriptMessages([
    {
      id: crypto.randomUUID(),
      role: "user",
      content: "implement feature",
      created_at: "2026-07-07T11:59:00Z",
      seq: 1,
      ord: 1,
    },
    {
      id: slotId,
      role: "assistant",
      kind: "draft",
      content: "Final coordinator draft body.",
      draft_version_count: 3,
      draft_status: "committed",
      created_at: "2026-07-07T12:02:00Z",
      seq: 2,
      ord: 2,
    },
    {
      id: crypto.randomUUID(),
      role: "assistant",
      kind: "draft",
      content: "",
      draft_version_count: 1,
      draft_status: "withdrawn",
      created_at: "2026-07-07T12:03:00Z",
      seq: 3,
      ord: 3,
    },
  ]);
  await bootstrapChatSession(page, request);
  await apiSeedSessionTranscript(
    request,
    await settledChatSessionId(page),
    fixtureMessages,
  );

  const answer = page.locator(".bubble--assistant").first();
  await expect(answer).toBeVisible({ timeout: 30_000 });
  await expect(answer).toContainText("Final coordinator draft body.");

  const rail = page.getByTestId("draft-rail").first();
  await expect(rail).toBeVisible();
  await expect(page.getByTestId("draft-rail-tabs")).toHaveCount(0);
  const toggle = page.getByTestId("draft-rail-toggle").first();
  await expect(toggle).toContainText("Versions (2)");
  await toggle.click();
  await expect(page.getByTestId("draft-rail-tabs")).toBeVisible();
  await expect(page.getByTestId("draft-rail-tab-v0")).toBeVisible();

  await expect(page.locator('[data-draft-withdrawn="true"]')).toHaveCount(0);

});

webE2e("draft version history under a committed answer", async ({ page, request }) => {
  const slotId = "00000000-0000-4000-8000-000000000202";

  await page.route("**/v1/sessions/*/drafts/*/versions", async (route) => {
    await route.fulfill({
      json: {
        versions: [
          {
            version_index: 0,
            body: "Superseded first attempt.",
            outcome_code: "SYNTH_HANDLE_NOT_IN_LEGS",
            created_at: "2026-07-07T12:00:00Z",
          },
        ],
      } satisfies DraftVersionsResponse,
    });
  });

  const fixtureMessages = transcriptMessages([
    {
      id: crypto.randomUUID(),
      role: "user",
      content: "tell me about this repo",
      created_at: "2026-07-07T11:59:00Z",
      seq: 1,
      ord: 1,
    },
    {
      // Committed draft slots are assistant rows without a draft kind.
      id: slotId,
      role: "assistant",
      content: "## Repo overview\n\nThe final committed answer.",
      draft_version_count: 2,
      draft_status: "committed",
      created_at: "2026-07-07T12:02:00Z",
      seq: 2,
      ord: 2,
    },
  ]);
  await bootstrapChatSession(page, request);
  await apiSeedSessionTranscript(
    request,
    await settledChatSessionId(page),
    fixtureMessages,
  );

  const answer = page.locator(".bubble--assistant").first();
  await expect(answer).toBeVisible({ timeout: 30_000 });
  await expect(answer).toContainText("The final committed answer.");

  const toggle = page.getByTestId("draft-rail-toggle");
  await expect(toggle).toBeVisible();
  await expect(toggle).toContainText("Versions (1)");
  await toggle.click();
  await expect(page.getByTestId("draft-rail-tabs")).toBeVisible();
  await expect(page.getByTestId("draft-rail-expanded-body")).toContainText(
    "Superseded first attempt.",
  );

});
