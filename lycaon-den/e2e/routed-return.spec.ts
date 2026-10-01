import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

/** A chat whose answer links a fixture file, so a click routes to Files. */
async function openChatWithSourceLink(page: Page, request: APIRequestContext) {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: crypto.randomUUID(),
        role: "user",
        content: "Where is the entrypoint?",
        created_at: "2026-09-18T12:00:00Z",
        seq: 1,
        ord: 1,
      },
      {
        id: crypto.randomUUID(),
        role: "assistant",
        content: "The entrypoint lives in `cmd/fixture/main.go`.",
        status: "complete",
        created_at: "2026-09-18T12:00:01Z",
        seq: 2,
        ord: 2,
        grounding: {
          traced: true,
          cited_evidence: [
            {
              handle: "read#1",
              path: "cmd/fixture/main.go",
              line: 9,
              excerpt: "func main()",
              verdict: "matched",
            },
          ],
        },
      },
    ]),
  );
  const link = page.locator(".assistant-prose .den-source-path-link");
  await expect(link).toBeVisible({ timeout: 30_000 });
  return { sessionId, link };
}

const filesBack = (page: Page) => page.getByTestId("files-back");

webE2e.describe("routed return to the conversation", () => {
  webE2e("inline Files offers the way back to the chat it came from", async ({ page, request }) => {
    const { link } = await openChatWithSourceLink(page, request);

    await link.click();
    await expect(page.getByTestId("files-editor-host")).toContainText("func main()", {
      timeout: 15_000,
    });
    await expect(filesBack(page)).toHaveText("Back to chat");

    await filesBack(page).click();
    await expect(liveChatStage(page).getByTestId("chat-stream")).toBeVisible();
    await expect(filesBack(page)).toHaveCount(0);
  });

  webE2e("a split never offers a return to the conversation beside it", async ({ page, request }) => {
    await page.setViewportSize({ width: 1500, height: 900 });
    const { sessionId, link } = await openChatWithSourceLink(page, request);
    await page.getByTestId("layout-dock-btn").click();
    await page.getByTestId("layout-tile-split").click();
    await page.getByTestId("layout-dock-btn").click();
    await expect(page.getByTestId("split-divider")).toBeVisible();

    await link.click();
    await expect(page.getByTestId("files-editor-host")).toContainText("func main()", {
      timeout: 15_000,
    });
    await expect(page.getByTestId("split-col-chat")).toBeVisible();
    await expect(filesBack(page)).toHaveCount(0);

    // A new chat takes the conversation column while Files keeps the stage.
    await page.getByTestId("new-chat-btn").click();
    await expect.poll(() => settledChatSessionId(page)).not.toBe(sessionId);
    await expect(page.getByTestId("split-col-chat")).toBeVisible();
    await expect(filesBack(page)).toHaveCount(0);

    // Hiding the conversation makes the return honest, and taking it shows the column again.
    await page.getByTestId("conversation-collapse-btn").filter({ visible: true }).click();
    await expect(page.getByTestId("split-col-chat")).toBeHidden();
    await expect(filesBack(page)).toHaveText("Back to chat");
    await filesBack(page).click();
    await expect(page.getByTestId("split-col-chat")).toBeVisible();
    await expect(page.getByTestId("split-divider")).toBeVisible();
    await expect(page.getByTestId("files-editor-host")).toBeVisible();
    await expect(filesBack(page)).toHaveCount(0);
  });
});
