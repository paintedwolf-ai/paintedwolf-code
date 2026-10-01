import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, webE2e } from "./helpers.ts";

webE2e("starting an idea reveals the conversation after a collapsed project workspace", async ({ page, request }) => {
  await page.setViewportSize({ width: 1500, height: 900 });
  await bootstrapChatSession(page, request);
  await page.getByRole("region", { name: "Project context", exact: true })
    .getByRole("button", { name: "Files", exact: true }).click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("conversation-collapse-btn").click();
  await page.getByRole("button", { name: "Painted Wolf Code", exact: true }).click();
  const prompt = "Draft a short calculator test checklist. Do not write files.";
  await page.getByRole("textbox", { name: "Describe your idea…", exact: true }).fill(prompt);
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 60_000 });
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toContainText(prompt);
});
