import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import type { CheckpointListResponse } from "../src/api/types.ts";

/**
 * Exact local-service tool_approval card. Seed via harness with socket_capability
 * payload; confirm locked authority copy, dual paths, once/task/deny, and deny focus.
 */
webE2e(
  "den:harness — local service socket capability card",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const approved = "/tmp/lyc-harness-svc.sock";
    const resolved = "/private/tmp/lyc-harness-svc.sock";
    const inject = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        command: "true",
        title: "Allow local service: lyc-harness-svc.sock",
        approved_path: approved,
        resolved_path: resolved,
      },
    });
    const injectBody = await inject.json();
    expect(inject.ok(), `harness inject: ${JSON.stringify(injectBody)}`).toBeTruthy();

    // Confirm the host published the typed socket payload before asserting UI.
    await expect
      .poll(
        async () => {
          const res = await request.get(
            `${apiUrl}/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`,
            { headers: auth },
          );
          if (!res.ok()) return `http ${res.status()}`;
          const body = (await res.json()) as CheckpointListResponse;
          const hit = body.checkpoints.some(
            (c) =>
              c.tool_approval?.plan?.subject?.kind === "socket_set" &&
              c.tool_approval.plan.subject.targets?.some(
                (target) => target.details?.approved_path === approved,
              ),
          );
          return hit ? true : JSON.stringify(body).slice(0, 400);
        },
        { timeout: 30_000 },
      )
      .toBe(true);

    // Tool-approval cards attach to the tool-call group; open it if collapsed.
    const activitySpan = liveChatStage(page).getByRole("group").filter({ hasText: "command" }).first();
    if (await activitySpan.count()) {
      await activitySpan.click({ trial: true }).catch(() => undefined);
      await activitySpan.click().catch(() => undefined);
    }

    const card = page.getByTestId("tool-approval-card");
    await expect(card).toBeVisible({ timeout: 60_000 });
    await expect(card).toContainText("Run command");
    await expect(card.getByTestId("approval-subject")).toContainText(approved);
    await expect(card.getByTestId("approval-approve-primary")).toBeVisible();
    await expect(card.getByTestId("approval-grant-face")).toBeVisible();
    const no = card.getByTestId("approval-no");
    await expect(no).toBeVisible();
    // The shell never autofocuses a destructive control on mount.
    await expect(no).not.toBeFocused();
    await expect(card).toContainText("outside the sandbox");
    await card.getByRole("button", {name:"Approval details in Files",exact:true}).click();
    await expect(page.getByTestId("chat-content-page")).toContainText(resolved);
  },
);
