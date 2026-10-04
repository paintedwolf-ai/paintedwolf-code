import { expect } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, settledChatSessionId, transcriptMessages, webE2e } from "./helpers.ts";

const CONTEXT_ACTIVE = "project-context-zone__entry--active";
const GEAR_ACTIVE = "project-cluster__icon-btn--active";

webE2e.describe("nav single selection", () => {
  webE2e("idle stages cannot paint through the active stage", async ({ page }) => {
    await bootstrapChatSession(page);

    await page.getByTestId("project-files-entry").click();
    const files = page.getByTestId("project-files-view");
    await expect(files).toBeVisible();

    await page.getByTestId("project-search-entry").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible();

    const residentFiles = page
      .getByTestId("resident-surface")
      .filter({ has: files });
    await expect(residentFiles).toHaveAttribute("data-resident", "idle");
    await expect(residentFiles).toHaveAttribute("inert", "");
    const hidden = await residentFiles.evaluate((element) => {
      const style = getComputedStyle(element);
      return { visibility: style.visibility, clip: style.clipPath, content: style.contentVisibility };
    });
    // Idle surfaces stay rendered so their scroll offsets survive; WebKit
    // never resumes compositor animations inside revealed skipped content.
    expect(hidden).toEqual({ visibility: "hidden", clip: "inset(50%)", content: "visible" });
  });

  webE2e("opening search clears the configuration selection", async ({ page }) => {
    await bootstrapChatSession(page);

    const config = page.getByTestId("project-configuration-entry");
    const search = page.getByTestId("project-search-entry");

    await config.click();
    await expect(config).toHaveClass(new RegExp(GEAR_ACTIVE));
    await expect(page.getByTestId("project-context-view")).toBeVisible();

    await search.click();
    // Search opens as a full stage.
    await expect(page.getByTestId("global-search-view")).toBeVisible();
    // Only the active destination stays selected.
    await expect(search).toHaveClass(new RegExp(CONTEXT_ACTIVE));
    await expect(config).not.toHaveClass(new RegExp(GEAR_ACTIVE));
    await page.keyboard.press("Escape");
    const searchSurface = page.locator('[data-resident-key$=":search"]');
    await expect(searchSurface).toHaveAttribute("data-resident", "idle");
    await expect(searchSurface).toHaveAttribute("inert", "");
  });

  for (const started of [false, true]) {
    webE2e(`New chat leaves configuration with a ${started ? "started" : "empty"} session`, async ({
      page, request,
    }) => {
      await bootstrapChatSession(page);
      if (started) {
        await apiSeedSessionTranscript(request, await settledChatSessionId(page), transcriptMessages([
          { id: crypto.randomUUID(), role: "user", content: "Existing conversation", created_at: new Date().toISOString(), ord: 1, seq: 1 },
        ]));
        await expect(liveChatStage(page).getByTestId("transcript-article-user")).toContainText("Existing conversation");
      }

      const rows = page
        .getByTestId("focused-session-list")
        .locator(".focused-session-list__row");
      await expect(rows).toHaveCount(1);
      const before = await rows.count();

      const config = page.getByTestId("project-configuration-entry");
      await config.click();
      await expect(config).toHaveClass(new RegExp(GEAR_ACTIVE));

      // An empty session is reused; a started conversation creates another row.
      await page.getByTestId("new-chat-btn").click();
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
      await expect(liveChatStage(page).getByTestId("chat-composer")).toBeFocused();
      await expect(config).not.toHaveClass(new RegExp(GEAR_ACTIVE));
      await expect(rows).toHaveCount(before + (started ? 1 : 0));
    });
  }
});
