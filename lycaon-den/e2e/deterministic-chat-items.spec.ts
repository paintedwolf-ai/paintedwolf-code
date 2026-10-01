import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";
import type { DraftVersionsResponse } from "../src/api/types.ts";

webE2e("deterministic chat items", async ({ page, request }) => {
  const slotId = "00000000-0000-4000-8000-000000000201";

  await page.route("**/v1/sessions/*/drafts/*/versions", async (route) => {
    await route.fulfill({
      json: {
        versions: [
          {
            version_index: 0,
            body: "Rejected attempt",
            outcome_code: "PROGRESS_MISSING",
            created_at: "2026-07-16T12:00:00Z",
          },
        ],
      } satisfies DraftVersionsResponse,
    });
  });

  // The fixture's draft row is a mid-run orchestration step, which chat hides
  // unless verbose mode is on — seed it rather than inherit an ambient pref.
  await bootstrapChatSession(page, request, { debug: { verboseMode: true } });
  const sessionId = await settledChatSessionId(page);
  const taskAssistantId = crypto.randomUUID();
  const commandAssistantId = crypto.randomUUID();
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: crypto.randomUUID(),
        role: "user",
        content: "ship the feature",
        created_at: "2026-07-16T12:00:00Z",
        seq: 1,
        ord: 1,
      },
      {
        id: crypto.randomUUID(),
        role: "assistant",
        kind: "blueprint",
        content: "# Plan\n\nDo the work",
        blueprint: {
          blueprint_path: "plan-1",
          revision: 1,
          revision_key: "rev-1",
          status: "awaiting_approval",
          phase: "ready",
          phase_label: "Ready for review",
          blueprint_title: "Release plan",
          can_approve: true,
          collapsed: false,
          show_actions: true,
        },
        created_at: "2026-07-16T12:01:00Z",
        seq: 2,
        ord: 2,
      },
      {
        id: taskAssistantId,
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "tc-task",
            name: "task",
            args: {
              agent_type: "implementer",
              brief: { goal: "wire", done_when: ["Return results."] },
            },
          },
        ],
        created_at: "2026-07-16T12:02:00Z",
        seq: 3,
        ord: 3,
      },
      {
        id: crypto.randomUUID(),
        role: "tool",
        content: '{"status":"canceled"}',
        tool_result: {
          content: '{"status":"canceled"}',
          dispatch: { worker_id: "job-1" },
          tool: "task",
          tool_call_id: "tc-task",
          assistant_message_id: taskAssistantId,
        },
        worker_summary: {
          worker_id: "job-1",
          child_session_id: "child-1",
          agent_type: "implementer",
          status: "canceled",
          envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="canceled"></task>',
        },
        created_at: "2026-07-16T12:03:00Z",
        seq: 4,
        ord: 4,
      },
      {
        id: commandAssistantId,
        role: "assistant",
        content: "",
        tool_calls: [
          { id: "tc-command", name: "command", args: { command: "rm" } },
        ],
        created_at: "2026-07-16T12:04:00Z",
        seq: 5,
        ord: 5,
      },
      {
        id: crypto.randomUUID(),
        role: "tool",
        content: "denied",
        tool_result: {
          content: "denied",
          tool: "command",
          tool_call_id: "tc-command",
          assistant_message_id: commandAssistantId,
          outcome: "rejected",
          checkpoint_decision: {
            checkpoint_id: "chk-1",
            kind: "tool_approval",
            status: "rejected",
          },
        },
        created_at: "2026-07-16T12:05:00Z",
        seq: 6,
        ord: 6,
      },
      {
        id: slotId,
        role: "assistant",
        kind: "draft",
        content: "Coordinator draft after reject",
        draft_version_count: 2,
        draft_status: "committed",
        tool_calls: [{ id: "tc-read", name: "read", args: { path: "." } }],
        created_at: "2026-07-16T12:06:00Z",
        seq: 7,
        ord: 7,
      },
    ]),
  );

  await expect(page.getByTestId("blueprint-card").first()).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByTestId("task-card").first()).toBeVisible();
  await expect(page.getByTestId("checkpoint-decision-chicklet").first()).toBeVisible();
  await expect(page.getByTestId("draft-rail").first()).toBeVisible();
});
