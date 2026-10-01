import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  fillSolidControl,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

const SOCKET_PATH = "/tmp/painted-wolf-harness-approval.sock";

async function grantIDForPattern(
  request: import("@playwright/test").APIRequestContext,
  apiUrl: string,
  auth: Record<string, string>,
  pattern: string,
  sessionId: string,
): Promise<string> {
  const res = await request.get(`${apiUrl}/v1/approval-grants`, {
    headers: auth,
  });
  if (!res.ok()) return "";
  const body = (await res.json()) as {
    grants: Array<{ id: string; pattern: string; chat_session_id?: string }>;
  };
  return body.grants.find(
    (grant) =>
      grant.pattern === pattern && grant.chat_session_id === sessionId,
  )?.id ?? "";
}

webE2e(
  "den:harness — host-offered chat lease can be revoked from Saved approvals",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);
    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    await openNewChatSession(page);
    const stage = liveChatStage(page);
    await expect(stage.getByTestId("chat-composer")).toBeVisible({ timeout: 90_000 });
    const sessionId =
      (await stage.getByTestId("chat-stream").getAttribute("data-session-id"))?.trim() ?? "";
    expect(sessionId).toBeTruthy();

    const inject = await request.post(
      `${apiUrl}/harness/checkpoints/tool_approval`,
      {
        headers: { ...auth, "Content-Type": "application/json" },
        data: {
          session_id: sessionId,
          command: "install helper",
          approved_path: SOCKET_PATH,
          resolved_path: SOCKET_PATH,
        },
      },
    );
    expect(inject.ok(), await inject.text()).toBeTruthy();

    const card = stage.getByTestId("tool-approval-card");
    await expect(card).toBeVisible({ timeout: 60_000 });
    await expect(card.getByTestId("approval-approve-primary")).toContainText(
      "Allow for this chat",
    );
    await card.getByTestId("approval-approve-primary").click();

    let grantId = "";
    await expect
      .poll(async () => {
        grantId = await grantIDForPattern(
          request,
          apiUrl,
          auth,
          SOCKET_PATH,
          sessionId,
        );
        return grantId;
      }, { timeout: 30_000 })
      .toBeTruthy();

    await page.evaluate(async () => {
      await (
        window as unknown as { __harness: { openSettings(): Promise<unknown> } }
      ).__harness.openSettings();
    });
    await expect(page.getByTestId("settings-view")).toBeVisible({ timeout: 15_000 });
    await page.getByTestId("settings-nav-approvals").click();
    await page.getByTestId("approvals-tab-saved").click();
    await expect(page.getByTestId("saved-approvals-panel")).toBeVisible();
    await fillSolidControl(page.getByTestId("saved-approvals-search"), SOCKET_PATH);

    const row = page.locator(
      `[data-testid="saved-approvals-row"][data-grant-id="${grantId}"]`,
    );
    await expect(row).toBeVisible({ timeout: 30_000 });
    const revoked = page.waitForResponse(
      (response) =>
        response.url().includes("/v1/approval-grants/revoke") &&
        response.request().method() === "POST",
    );
    await row.getByTestId("saved-approvals-revoke").click();
    const confirm = page.getByRole("alertdialog", {
      name: "Revoke local service access?",
    });
    await expect(confirm).toBeVisible();
    await confirm.getByRole("button", { name: "Revoke", exact: true }).click();
    expect((await revoked).status()).toBe(200);

    await expect
      .poll(() =>
        grantIDForPattern(request, apiUrl, auth, SOCKET_PATH, sessionId),
      )
      .toBe("");
    await expect(row).toHaveCount(0);
  },
);
