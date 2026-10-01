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

/**
 * Direct IP approval exposes scoped reuse and a one-action option while
 * keeping declared destinations distinct from observed network activity.
 */
webE2e(
  "den:harness — direct IP capability card",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const declared = "db.example.test:5432";
    const inject = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        command: "psql -h db.example.test",
        direct_ip: true,
        declared_destinations: [declared],
      },
    });
    const injectBody = await inject.json();
    expect(inject.ok(), `harness inject: ${JSON.stringify(injectBody)}`).toBeTruthy();

    await expect
      .poll(
        async () => {
          const res = await request.get(
            `${apiUrl}/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`,
            { headers: auth },
          );
          if (!res.ok()) return `http ${res.status()}`;
          const body = (await res.json()) as Array<{
            tool_approval?: {
              plan?: {
                subject?: {
                  kind?: string;
                  targets?: Array<{ details?: Record<string, unknown> }>;
                };
              };
            };
          }>;
          const hit = (Array.isArray(body) ? body : []).some(
            (c) =>
              c.tool_approval?.plan?.subject?.kind === "direct_ip" &&
              c.tool_approval.plan.subject.targets?.some(
                (target) => target.details?.visibility === "unobserved",
              ),
          );
          return hit ? "ready" : "waiting";
        },
        { timeout: 30_000 },
      )
      .toBe("ready");

    const card = liveChatStage(page).getByTestId("tool-approval-card");
    await expect(card).toBeVisible({ timeout: 30_000 });
    await expect(card.getByTestId("approval-high-risk-consequence")).toContainText(
      "cannot observe or check the destinations",
    );
    await expect(card.getByTestId("approval-approve-primary")).toContainText(
      "Allow for this chat",
    );
    await card.getByRole("button", { name: "Allow with a grant", exact: true }).click();
    await expect(page.getByRole("menuitem", { name: /Allow once/ })).toBeVisible();
    await page.keyboard.press("Escape");
    const no = card.getByTestId("approval-no");
    await expect(no).toContainText("No");
    // The shell never autofocuses a destructive control on mount.
    await expect(no).not.toBeFocused();
    await card.getByRole("button", {name:"Approval details in Files",exact:true}).click();
    const details = page.getByTestId("chat-content-page");
    await expect(details).toContainText("Unobserved");
    await expect(details).toContainText("not observed");
    await expect(details).toContainText(declared);
  },
);
