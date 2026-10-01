import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

async function installCitationMessages(
  request: APIRequestContext,
  sessionId: string,
  assistantContent: string,
) {
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: crypto.randomUUID(),
        role: "user",
        content: "Show me the main file",
        created_at: "2026-07-23T20:00:00Z",
        seq: 1,
        ord: 1,
      },
      {
        id: crypto.randomUUID(),
        role: "assistant",
        content: assistantContent,
        status: "complete",
        created_at: "2026-07-23T20:00:01Z",
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
}

/** Open a chat, then append grounded rows through the harness's real store path. */
async function openMockedChat(
  page: Page,
  request: Parameters<typeof bootstrapChatSession>[1],
  assistantContent: string,
) {
  await bootstrapChatSession(page, request);
  const sessionId = (
    (await liveChatStage(page).getByTestId("chat-stream").getAttribute("data-session-id")) ??
    ""
  ).trim();
  expect(sessionId).toBeTruthy();
  await installCitationMessages(request ?? page.request, sessionId, assistantContent);
}

/** Citation path click opens the Files stage editor; external editor launch needs the desktop app. */
webE2e.describe("citation source opens in-app", () => {
  webE2e(
    "path click in citation panel opens the Files stage editor",
    async ({ page, request }) => {
      await openMockedChat(page, request, "I found the relevant code.");

      const chicklet = page.getByTestId("citation-evidence-chicklet");
      await expect(chicklet).toBeVisible({ timeout: 30_000 });
      const summary = chicklet.locator("summary");
      const geometry = await chicklet.evaluate((element) => {
        const stream = element.closest(".den-chat-transcript-visual")!;
        const header = element.querySelector("summary")!;
        const row = header.firstElementChild!;
        const caret = header.querySelector(".den-tool-chicklet-caret")!;
        return {
          width: element.getBoundingClientRect().width,
          expectedWidth: Math.min(
            stream.getBoundingClientRect().width,
            32 * parseFloat(getComputedStyle(document.documentElement).fontSize),
          ),
          rightGap: header.getBoundingClientRect().right - caret.getBoundingClientRect().right,
          inset: parseFloat(getComputedStyle(row).paddingRight),
          headerWidth: header.getBoundingClientRect().width,
          headerHeight: header.getBoundingClientRect().height,
        };
      });
      expect(geometry.width).toBeCloseTo(geometry.expectedWidth, 0);
      expect(geometry.rightGap).toBeCloseTo(geometry.inset, 0);
      await summary.click({
        position: { x: geometry.headerWidth - 2, y: geometry.headerHeight / 2 },
      });

      const panel = page.getByTestId("citation-evidence-chicklet-panel");
      await expect(panel).toBeVisible();
      await expect.poll(() => chicklet.evaluate((element) => {
        const stream = element.closest(".den-chat-transcript-visual")!;
        return Math.abs(element.getBoundingClientRect().width - stream.getBoundingClientRect().width);
      })).toBeLessThan(1);
      const link = panel.getByTestId("source-path-link");
      await expect(link).toHaveText("cmd/fixture/main.go:9");
      await link.click();

      await expect(page.getByTestId("project-files-view")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("files-editor-crumb")).toContainText(
        "main.go",
      );
      await expect(page.getByTestId("files-editor-host")).toContainText(
        "func main()",
      );
    },
  );

  webE2e(
    "prose citation mention links and opens the Files stage editor (D-PROSE-EVIDENCE-LINK)",
    async ({ page, request }) => {
      await openMockedChat(
        page,
        request,
        "The entrypoint lives in `cmd/fixture/main.go` — nothing else registers.",
      );

      const button = page.locator(".assistant-prose .den-source-path-link");
      await expect(button).toBeVisible({ timeout: 30_000 });
      await expect(button).toHaveText("cmd/fixture/main.go");
      await expect(button).toHaveAttribute("data-den-source-path", "cmd/fixture/main.go");
      // No line in the prose — the cited line is the jump target.
      await expect(button).toHaveAttribute("data-den-source-line", "9");
      await button.click();

      await expect(page.getByTestId("project-files-view")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("files-editor-crumb")).toContainText(
        "main.go",
      );
      await expect(page.getByTestId("files-editor-host")).toContainText(
        "func main()",
      );
    },
  );

  webE2e(
    "prose path:start-end mention carries end line into the Files open",
    async ({ page, request }) => {
      await openMockedChat(
        page,
        request,
        "See the block in `cmd/fixture/main.go:9-13` for the entrypoint.",
      );

      const button = page.locator(".assistant-prose .den-source-path-link");
      await expect(button).toBeVisible({ timeout: 30_000 });
      await expect(button).toHaveAttribute("data-den-source-path", "cmd/fixture/main.go");
      await expect(button).toHaveAttribute("data-den-source-line", "9");
      await expect(button).toHaveAttribute("data-den-source-end-line", "13");
      await button.click();

      await expect(page.getByTestId("project-files-view")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("files-editor-host")).toContainText(
        "func main()",
      );
    },
  );
});
