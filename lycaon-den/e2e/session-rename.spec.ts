import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  apiConfig,
  bootstrapChatSession,
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

webE2e("row rename button renames the chat", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "Old title");

  const row = page.locator(
    `[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`,
  );
  await expect(listTitle(page, "Old title")).toBeVisible({ timeout: 30_000 });
  await row.hover();
  await page.getByTestId("session-rename-action").click();
  const input = page.getByTestId("session-rename-input");
  await expect(input).toBeVisible();
  await fillSolidControl(input, "Ship readiness checklist");
  await input.press("Enter");

  await expect(listTitle(page, "Ship readiness checklist")).toBeVisible({
    timeout: 15_000,
  });
});

webE2e("revealed rename action retains focus after leaving an unsent composer draft", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "Draft focus check");
  await expect(listTitle(page, "Draft focus check")).toBeVisible();
  const composer = page.getByPlaceholder("What would you like to work on?", { exact: true });
  await composer.fill("Unsent draft with café and 日本語.");
  await expect(composer).toBeFocused();
  // The action is clipped until its row is hovered or contains keyboard focus.
  await page.getByTestId("focused-session-list").locator(`[data-session-id="${sessionId}"]`).hover();
  await page.getByTestId("session-rename-action").click();
  const input = page.getByTestId("session-rename-input");
  await expect(input).toBeFocused();
  await input.fill("Renamed with draft retained");
  await input.press("Enter");
  await expect(listTitle(page, "Renamed with draft retained")).toBeVisible();
  await expect(composer).toHaveValue("Unsent draft with café and 日本語.");
});

webE2e("double-click on a list row renames the chat", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "Dbl seed");

  const row = page.locator(
    `[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`,
  );
  await expect(listTitle(page, "Dbl seed")).toBeVisible({ timeout: 30_000 });
  await row.dblclick();
  const input = page.getByTestId("session-rename-input");
  await expect(input).toBeVisible();
  await fillSolidControl(input, "From double click");
  await input.press("Enter");

  await expect(listTitle(page, "From double click")).toBeVisible({
    timeout: 15_000,
  });
});

webE2e("Esc cancels the list rename", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "Keep me");

  const row = page.locator(
    `[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`,
  );
  await expect(listTitle(page, "Keep me")).toBeVisible({ timeout: 30_000 });
  await row.dblclick();
  const input = page.getByTestId("session-rename-input");
  await fillSolidControl(input, "Should not stick");
  await input.press("Escape");
  await expect(page.getByTestId("session-rename-input")).toHaveCount(0);
  await expect(listTitle(page, "Keep me")).toBeVisible();
});

webE2e("session list context menu renames chat title", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await seedTitle(request, sessionId, "List rename seed");

  const row = page.locator(
    `[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`,
  );
  await expect(listTitle(page, "List rename seed")).toBeVisible({
    timeout: 30_000,
  });
  await row.click({ button: "right" });
  await page.getByTestId("session-menu-rename").click();
  const input = page.getByTestId("session-rename-input");
  await expect(input).toBeVisible();
  await fillSolidControl(input, "From list menu");
  await input.press("Enter");
  await expect(listTitle(page, "From list menu")).toBeVisible({
    timeout: 15_000,
  });
});
