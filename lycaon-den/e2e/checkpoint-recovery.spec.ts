import { execFile } from "node:child_process";
import { realpathSync, rmSync, symlinkSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  expectShellReady,
  liveChatStage,
  waitForSessionReady,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import { harnessSocketPath, listenHarnessSocket } from "./fixtures/harness-socket.ts";
import type { CheckpointListResponse } from "../src/api/types.ts";

type ApprovalPlanSubject = NonNullable<
  NonNullable<CheckpointListResponse["checkpoints"][number]["tool_approval"]>["plan"]
>["subject"];

async function expectPlanSubject(
  request: APIRequestContext,
  sessionId: string,
  matches: (subject: ApprovalPlanSubject) => boolean,
): Promise<void> {
  const { apiUrl, token } = apiConfig();
  await expect
    .poll(async () => {
      const res = await request.get(
        `${apiUrl}/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      if (!res.ok()) return `http ${res.status()}`;
      const body = (await res.json()) as CheckpointListResponse;
      return body.checkpoints.some((c) => {
        const subject = c.tool_approval?.plan?.subject;
        return subject != null && matches(subject);
      })
        ? "ready"
        : JSON.stringify(body).slice(0, 400);
    }, { timeout: 30_000 })
    .toBe("ready");
}

async function openApprovalDetails(page: Page, card: Locator): Promise<Locator> {
  await card.getByRole("button", { name: "Approval details in Files", exact: true }).click();
  return page.getByTestId("chat-content-page");
}

async function foregroundSession(page: Page): Promise<string> {
  const stream = liveChatStage(page).getByTestId("chat-stream").first();
  await expect(stream).toHaveAttribute("data-session-id", /.+/);
  return (await stream.getAttribute("data-session-id"))!.trim();
}

async function openBlankChat(page: Page): Promise<string> {
  const previous = await foregroundSession(page);
  await waitForSessionReady(page.request, previous);
  const newChat = page.getByTestId("new-chat-btn");
  if (await newChat.isEnabled()) {
    await newChat.click();
    await expect.poll(() => foregroundSession(page)).not.toBe(previous);
  }
  const sessionId = await foregroundSession(page);
  await waitForSessionReady(page.request, sessionId);
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
  return sessionId;
}

async function openApprovalTask(page: Page, projectId: string, sessionId: string): Promise<void> {
  await expectShellReady(page);
  if (await page.getByTestId("home-nav").isVisible()) await activateProject(page, projectId);
  const task = page.locator(`[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`);
  await expect(task).toBeVisible({ timeout: 30_000 });
  await task.click();
  await expect.poll(() => foregroundSession(page)).toBe(sessionId);
}

async function openWaitingTask(page: Page, projectId: string, sessionId: string): Promise<void> {
  await page.getByTestId("nav-brand").click();
  await expect(page.getByTestId("attention-band")).toBeVisible({ timeout: 30_000 });
  const waiting = page.locator(`[data-testid="attention-band-row"][data-session-id="${sessionId}"]`);
  if (await waiting.isVisible()) {
    await waiting.click();
  } else {
    await expect(page.getByTestId("attention-band-more")).toBeVisible();
    await openApprovalTask(page, projectId, sessionId);
  }
  await expect.poll(() => foregroundSession(page), { timeout: 30_000 }).toBe(sessionId);
}

let approvedSocketPath: string;
let resolvedSocketPath: string;
let closeSocket: () => Promise<void>;

// The card shows both paths, so the approved path is a link resolving elsewhere.
webE2e.beforeAll(async () => {
  const target = harnessSocketPath("recovery.sock");
  closeSocket = await listenHarnessSocket(target);
  approvedSocketPath = harnessSocketPath("recovery-link.sock");
  rmSync(approvedSocketPath, { force: true });
  symlinkSync(target, approvedSocketPath);
  resolvedSocketPath = realpathSync(target);
});
webE2e.afterAll(async () => {
  await closeSocket();
  rmSync(approvedSocketPath, { force: true });
});

type ApprovalCheck = { page: Page; request: APIRequestContext; sessionId: string; card: Locator };

const directIpDestination = "https://example.test/";

// Fields are read when a test runs: the socket paths exist only after beforeAll.
const approvals: Array<{
  name: string;
  fields: () => Record<string, unknown>;
  // Runs on the reopened card while it is live.
  live?: (check: ApprovalCheck) => Promise<void>;
  // Runs after reload, before the approval resolves.
  details?: (check: ApprovalCheck) => Promise<void>;
}> = [
  { name: "command", fields: () => ({}) },
  {
    name: "local service",
    fields: () => ({ approved_path: approvedSocketPath, resolved_path: resolvedSocketPath }),
    live: async ({ request, sessionId, card }) => {
      await expectPlanSubject(request, sessionId, (subject) =>
        subject.kind === "socket_set" &&
        (subject.targets ?? []).some((target) => target.details?.approved_path === approvedSocketPath));
      await expect(card.getByTestId("approval-subject")).toContainText(approvedSocketPath);
      await expect(card.getByTestId("approval-approve-primary")).toBeVisible();
      await expect(card.getByTestId("approval-grant-face")).toBeVisible();
      const no = card.getByTestId("approval-no");
      await expect(no).toBeVisible();
      // The shell never autofocuses a destructive control on mount.
      await expect(no).not.toBeFocused();
      await expect(card).toContainText("outside the sandbox");
    },
    details: async ({ page, card }) => {
      await expect(await openApprovalDetails(page, card)).toContainText(resolvedSocketPath);
    },
  },
  {
    name: "direct IP",
    fields: () => ({ direct_ip: true, declared_destinations: [directIpDestination] }),
    live: async ({ page, request, sessionId, card }) => {
      await expectPlanSubject(request, sessionId, (subject) =>
        subject.kind === "direct_ip" &&
        (subject.targets ?? []).some((target) => target.details?.visibility === "unobserved"));
      await expect(card.getByTestId("approval-high-risk-consequence")).toBeVisible();
      await expect(card.getByTestId("approval-approve-primary")).toContainText("Allow for this chat");
      await card.getByRole("button", { name: "Other choices", exact: true }).click();
      await expect(page.getByRole("menuitem", { name: /Allow once/ })).toBeVisible();
      await page.keyboard.press("Escape");
      // The shell never autofocuses a destructive control on mount.
      await expect(card.getByTestId("approval-no")).not.toBeFocused();
    },
    details: async ({ page, card }) => {
      const details = await openApprovalDetails(page, card);
      await expect(details).toContainText("Unobserved");
      await expect(details).toContainText(directIpDestination);
    },
  },
  {
    name: "secret screen",
    fields: () => ({
      secret: {
        surface: "model",
        destination: "fixture-model",
        rule_title: "Fixture credential",
        shape: "fixture",
      },
    }),
  },
];

for (const approval of approvals) {
  webE2e(`pending ${approval.name} approval survives reopening and reload`, async ({ page, request }) => {
    if (approval.name === "command") webE2e.setTimeout(360_000);
    await page.goto("/");
    await waitHarnessConnected(page);
    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);
    const sessionId = await openBlankChat(page);
    const { apiUrl, token } = apiConfig();
    const headers = { Authorization: `Bearer ${token}` };
    const response = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers,
      data: { session_id: sessionId, command: "printf fixture", ...approval.fields() },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    const { checkpoint_id: checkpointId } = await response.json() as { checkpoint_id: string };
    const card = () => liveChatStage(page).locator(
      `[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`,
    );
    await expect(card()).toBeVisible();

    await openBlankChat(page);
    await openApprovalTask(page, project.id, sessionId);
    await expect(card()).toBeVisible();
    await approval.live?.({ page, request, sessionId, card: card() });
    if (approval.name === "command") {
      const restart = fileURLToPath(new URL("../../scripts/harness/crash-restart.sh", import.meta.url));
      await promisify(execFile)("bash", [restart], { timeout: 300_000 });
      await waitHarnessConnected(page);
    }
    await page.reload();
    await waitHarnessConnected(page);
    await openWaitingTask(page, project.id, sessionId);
    await expect(card()).toBeVisible({ timeout: 30_000 });

    const pending = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints?include_children=true`, { headers });
    expect(pending.ok()).toBeTruthy();
    expect((await pending.json() as CheckpointListResponse).checkpoints.map((cp) => cp.id)).toEqual([checkpointId]);
    const attention = await request.get(`${apiUrl}/v1/attention`, { headers });
    expect(attention.ok()).toBeTruthy();
    expect((await attention.json()).rows).toEqual(expect.arrayContaining([
      expect.objectContaining({ session_id: sessionId, class: "needs_you", reason: "checkpoint" }),
    ]));

    await approval.details?.({ page, request, sessionId, card: card() });
    if (approval.name === "command") {
      await card().getByTestId("approval-approve-primary").click();
    } else {
      const resolved = await request.post(`${apiUrl}/v1/sessions/${sessionId}/checkpoints/${checkpointId}`, {
        headers,
        data: { kind: "tool_approval", action: "reject", guidance: "Fixture complete" },
      });
      expect(resolved.ok(), await resolved.text()).toBeTruthy();
    }
    await expect(card()).toHaveCount(0);
    await page.reload();
    await waitHarnessConnected(page);
    await openApprovalTask(page, project.id, sessionId);
    await expect(card()).toHaveCount(0);
    const bootstrap = await request.get(`${apiUrl}/v1/sessions/${sessionId}/bootstrap`, { headers });
    expect(bootstrap.ok()).toBeTruthy();
    expect((await bootstrap.json()).checkpoints).toEqual([]);
  });
}
