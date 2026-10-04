import { expect } from "@playwright/test";
import { apiJson, bootstrapChatSession, liveChatStage, settledChatSessionId, webE2e } from "./helpers.ts";

for (const action of ["Explain in context", "Give me the gist"]) {
  webE2e(`${action} keeps an existing composer draft`, async ({ page, request }) => {
    await page.setViewportSize({ width: 1500, height: 900 });
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Selection explanation fixture." });
    try {
      await page.getByTestId("focused-project-nav").getByRole("button", { name: "Files", exact: true }).click();
      await page.getByTestId("layout-dock-btn").click();
      await page.getByTestId("layout-tile-split").click();
      await page.getByTestId("layout-dock-btn").click();
      await expect(page.getByTestId("split-divider")).toBeVisible();
      const composer = liveChatStage(page).getByTestId("chat-composer");
      await composer.fill("My next question stays here.");
      await page.getByTestId("files-tree-file").filter({ hasText: /^README\.md$/ }).click();
      const editor = page.getByTestId("split-col-stage").locator(".cm-content[contenteditable=true]");
      // Pressing the content's center scrolls a wide line horizontally, and that scroll dismisses the menu.
      const firstLine = { x: 8, y: 8 };
      await editor.click({ position: firstLine });
      await editor.press("ControlOrMeta+a");
      await editor.click({ button: "right", position: firstLine });
      await page.getByRole("menuitem", { name: "Ask about selection", exact: true }).click();
      await page.getByRole("menuitem", { name: action, exact: true }).click();
      await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText("Selection explanation fixture.", { timeout: 45_000 });
      await expect(liveChatStage(page).getByRole("button", { name: "Clear all", exact: true })).toBeVisible();
      await expect(composer).toHaveValue("My next question stays here.");
      await expect.poll(async () => (await apiJson<{ status: string }>(request, "GET", `/v1/sessions/${sessionId}`)).status).toBe("idle");
      await expect(liveChatStage(page).getByTestId("transcript-article-assistant")).toHaveCount(1);
      await expect(composer).toHaveValue("My next question stays here.");
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
    }
  });
}
