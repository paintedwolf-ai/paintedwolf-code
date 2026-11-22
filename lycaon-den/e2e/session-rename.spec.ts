import { expect, type APIRequestContext, type Page } from "@playwright/test";
import type { Session } from "../src/api/types.ts";
import {
  apiConfig,
  apiJson,
  bootstrapChatSession,
  expectShellReady,
  fillSolidControl,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

/**
 * Session display-title rename — the nav list row is the only rename surface.
 * Seeds the title via PATCH so the smoke does not depend on auto-title / LLM idle.
 */
const seedTitle = async (
  request: APIRequestContext,
  sessionId: string,
  title: string,
) => {
  const { apiUrl, token } = apiConfig();
  const res = await request.patch(`${apiUrl}/v1/sessions/${sessionId}`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: { title },
  });
  expect(res.ok()).toBeTruthy();
};

const listTitle = (page: Page, text: string) =>
  page.locator(".focused-session-list__title", { hasText: text });

webE2e("list renames keep the composer draft and persist the title", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "Draft focus check");
  await expect(listTitle(page, "Draft focus check")).toBeVisible();
  const composer = page.getByPlaceholder("What would you like to work on?", { exact: true });
  await composer.fill("Unsent draft with café and 日本語.");
  await expect(composer).toBeFocused();
  const row = page.getByTestId("focused-session-list").locator(`[data-session-id="${sessionId}"]`);
  // The action is clipped until its row is hovered or contains keyboard focus.
  await row.hover();
  await page.getByTestId("session-rename-action").click();
  const input = page.getByTestId("session-rename-input");
  await expect(input).toBeFocused();
  await input.fill("Renamed with draft retained");
  await input.press("Enter");
  await expect(listTitle(page, "Renamed with draft retained")).toBeVisible();
  await expect(composer).toHaveValue("Unsent draft with café and 日本語.");
  await expect.poll(async () => (await apiJson<Session>(request, "GET", `/v1/sessions/${sessionId}`)).title)
    .toBe("Renamed with draft retained");
  await page.reload();
  await expectShellReady(page);
  await expect(listTitle(page, "Renamed with draft retained")).toBeVisible({ timeout: 30_000 });

  // A real double-click selects the row before it opens the rename input.
  await row.dblclick();
  await expect(input).toBeVisible();
  await fillSolidControl(input, "From double click");
  await input.press("Enter");
  await expect(listTitle(page, "From double click")).toBeVisible({
    timeout: 15_000,
  });
});
