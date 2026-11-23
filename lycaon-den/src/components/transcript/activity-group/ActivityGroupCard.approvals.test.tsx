// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { Message } from "../../../api/types.ts";
import { approvalReviewTarget, consumeApprovalReview } from "../../../chat/checkpoint/approval-review.ts";
import { revealApprovalOrigin } from "../../../chat/checkpoint/approval-reveal.ts";
import { toolApprovalFixture } from "../../../chat/checkpoint/approval-test-fixtures.ts";
import { buildChatTranscriptBlocks } from "../../../chat/workflow/workflow-spans.ts";
import {
  invalidateApprovalGrantsCache,
  loadApprovalGrantsCache,
} from "../../../settings/security/approval-grants-cache.ts";
import { SessionTranscript } from "../transcript-viewport-test-harness.tsx";
import { resetSessionTranscriptChatTest } from "../session-transcript-chat-test-harness.ts";

describe("ActivityGroupCard approvals section", () => {
  afterEach(() => {
    cleanup();
    const pending = approvalReviewTarget();
    if (pending) consumeApprovalReview(pending);
    invalidateApprovalGrantsCache();
    return resetSessionTranscriptChatTest();
  });

  it("keeps Allowed approval status independent of Refused execution status, supports Revoke, and links to the activity", async () => {
    const revokeApprovalGrants = vi.fn(async () => ({
      results: [{ id: "grant-122", revoked: true }],
    }));
    const client = {
      listApprovalGrants: vi.fn(async () => ({
        grants: [{ id: "grant-122", scope: "chat", tool: "command", title: "Allow for this chat", created_at: "t" }],
        quiet: [],
      })),
      revokeApprovalGrants,
    } as unknown as LycaonClient;
    await loadApprovalGrantsCache(client);

    const messages: Message[] = [
      {
        id: "a325",
        ord: 325,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "cmd-122", name: "command", args: { command: "grep -rn secret ../outside" } }],
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "r327",
        ord: 327,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "Refused: path outside worktree boundary",
        tool_result: {
          assistant_message_id: "a325",
          tool_call_id: "cmd-122",
          tool: "command",
          content: "Refused: path outside worktree boundary",
          outcome: "rejected",
          invocation: { owner: "coordinator", lifecycle: "effect_attempt", status: "rejected" },
          checkpoint_decision: {
            checkpoint_id: "cp-122",
            kind: "tool_approval",
            status: "approved",
            tool: "grep",
            subject: "../outside",
            grant_title: "Allow for this chat",
            grant_scope: "chat",
            grant_ids: ["grant-122"],
          },
        },
        created_at: "2026-11-23T00:00:01Z",
      },
    ];

    const { container, getByTestId } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-122"
        projectId="p1"
        checkpointClient={client}
        messages={messages}
      />
    ));

    expect(container.querySelector('[data-testid="tool-approval-card"]')).toBeNull();
    const chicklet = getByTestId("checkpoint-decision-chicklet");
    expect(chicklet.getAttribute("data-status")).toBe("approved");

    const span = getByTestId("activity-span-card");
    expect(span.getAttribute("data-status")).toBe("error");

    const revokeBtn = await waitFor(() => getByTestId("checkpoint-decision-revoke"));
    fireEvent.click(revokeBtn);
    await waitFor(() => expect(revokeApprovalGrants).toHaveBeenCalledWith({ ids: ["grant-122"] }));

    fireEvent.click(chicklet.querySelector<HTMLElement>("button.den-transcript-disclosure-toggle")!);
    const showActBtn = getByTestId("checkpoint-decision-show-activity");
    fireEvent.click(showActBtn);

    await waitFor(() => {
      expect(span.getAttribute("data-expanded")).toBe("true");
      const cmdCard = container.querySelector<HTMLElement>('[data-tool-call-id="cmd-122"]');
      expect(cmdCard?.getAttribute("data-status")).toBe("error");
    });
  });

  it("promotes pending requests onto the single-line multi-approval header even when closed, and routes Review in composer", async () => {
    const baseMessages: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [
          { id: "c-done", name: "command", args: { command: "grep ok" } },
          { id: "c-pend", name: "write", args: { path: "deploy.sh" } },
        ],
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "r1",
        ord: 2,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "ok",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-done",
          tool: "command",
          content: "ok",
          outcome: "completed",
          checkpoint_decision: {
            checkpoint_id: "cp-done",
            kind: "tool_approval",
            status: "approved",
            tool: "grep",
          },
        },
        created_at: "2026-11-23T00:00:01Z",
      },
    ];

    const [pending, setPending] = createSignal([
      {
        checkpointId: "cp-pend",
        sessionId: "s-multi-cp",
        kind: "tool_approval" as const,
        status: "pending" as const,
        issuedAt: "2026-11-23T00:00:02Z",
        tool_approval: {
          ...toolApprovalFixture({ command: "write deploy.sh" }),
          tool_call_id: "c-pend",
        },
      },
    ]);
    const [extraMessages, setExtraMessages] = createSignal<Message[]>([]);

    const { container, getByTestId } = render(() => {
      const msgs = [...baseMessages, ...extraMessages()];
      const spans = buildChatTranscriptBlocks(msgs, [], undefined, {
        sessionId: "s-multi-cp",
        pendingCheckpoints: pending(),
      });
      return (
        <SessionTranscript
          layout="chat"
          sessionId="s-multi-cp"
          projectId="p1"
          messages={msgs}
          transcriptSpans={spans}
        />
      );
    });

    const approvalsShell = getByTestId("activity-group-approvals");
    expect(approvalsShell.getAttribute("data-expanded")).toBe("false");
    expect(approvalsShell.getAttribute("data-status")).toBe("pending");
    expect(getByTestId("approvals-pending-more").textContent).toBe("+1 more");
    expect(container.querySelector('[data-testid="tool-approval-card"]')).toBeNull();

    const reviewBtn = getByTestId("approvals-review-dock");
    fireEvent.click(reviewBtn);
    expect(approvalReviewTarget()).toEqual({
      sessionId: "s-multi-cp",
      checkpointId: "cp-pend",
    });

    // Show in chat for a pending result-only tool focuses the approval row cleanly.
    revealApprovalOrigin({ sessionId: "s-multi-cp", toolCallId: "c-pend" });

    // Resolving transitions the collapsed summary in place without remounting the group.
    const groupBefore = getByTestId("activity-group");
    setPending([]);
    setExtraMessages([
      {
        id: "r2",
        ord: 3,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "denied",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-pend",
          tool: "write",
          content: "denied",
          outcome: "rejected",
          checkpoint_decision: {
            checkpoint_id: "cp-pend",
            kind: "tool_approval",
            status: "rejected",
            tool: "write",
          },
        },
        created_at: "2026-11-23T00:00:03Z",
      },
    ]);
    await waitFor(() => {
      expect(getByTestId("activity-group")).toBe(groupBefore);
      expect(approvalsShell.getAttribute("data-status")).toBe("settled");
      expect(getByTestId("approvals-count").textContent).toBe("2 approvals");
    });
  });
});
