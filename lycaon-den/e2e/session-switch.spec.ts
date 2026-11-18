import { expect } from "@playwright/test";
import type { Session } from "../src/api/types.ts";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiCreateProjectWithRoot,
  apiJson,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  openNewChatSession,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

webE2e.describe("session switch", () => {
  webE2e("launcher cross-project switch settles on the second project's session", async ({ page, request }) => {
    const first = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Session-A"),
    );
    const second = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Session-B"),
    );
    await gotoShell(page);
    await activateProject(page, first.id);
    await openNewChatSession(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 90_000 });
    const before = await settledChatSessionId(page);

    await page.getByTestId("project-switcher").click();
    await page.getByTestId(`project-launcher-row-${second.id}`).click();
    await expect.poll(() => settledChatSessionId(page), { timeout: 90_000 }).not.toBe(before);
    const after = await settledChatSessionId(page);
    const session = await apiJson<Session>(request, "GET", `/v1/sessions/${after}`);
    expect(session.project_id).toBe(second.id);
    await expect(page.getByTestId("project-switcher")).toContainText(second.name ?? "Untitled");
  });
});
