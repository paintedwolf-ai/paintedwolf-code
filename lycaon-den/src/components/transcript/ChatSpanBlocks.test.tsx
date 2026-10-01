import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import type { Message, WorkflowRun } from "../../api/types.ts";
import {
  buildChatTranscriptBlocks,
} from "../../chat/workflow/workflow-spans.ts";
import { ChatSpanBlocks } from "./transcript-viewport-test-harness.tsx";
import {
  findController,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "../../find/find-controller.ts";

const ambientRun: WorkflowRun = {
  id: "run-ambient",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "boot",
  created_at: "t",
  updated_at: "t",
};

const catalogRun: WorkflowRun = {
  id: "run-plan",
  session_id: "s1",
  workflow_id: "plan",
  workflow_version: "1.0.0",
	revision: 1,
  status: "running",
  current_phase: "research",
  start_message_id: "b1",
  created_at: "t",
  updated_at: "t",
};

describe("ChatSpanBlocks", () => {
  afterEach(() => {
    resetFindControllerForTests();
    document.body.replaceChildren();
  });

  it("finds rendered text across workflow spans through one transcript root", () => {
    const messages: Message[] = [
      {
        id: "first",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "first shared needle",
        workflow_run_id: "run-one",
        created_at: "t",
      },
      {
        id: "second",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "second shared needle",
        workflow_run_id: "run-two",
        created_at: "t",
      },
    ];
    const blocks = [
      {
        kind: "run" as const,
        runId: "run-one",
        ambientSpan: true,
        items: [{ kind: "user" as const, key: "first", text: messages[0]!.content }],
      },
      {
        kind: "run" as const,
        runId: "run-two",
        items: [{ kind: "user" as const, key: "second", text: messages[1]!.content }],
      },
    ];
    render(() => (
      <ChatSpanBlocks
        blocks={blocks}
        messages={messages}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));

    openFind();
    setFindQuery("shared needle");

    expect(findController.matches()).toHaveLength(2);
  });

  it("renders one task card per span from block items", () => {
    const messages: Message[] = [
      {
        id: "m0",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "ambient",
        workflow_run_id: "run-ambient",
        created_at: "t",
      },
      {
        id: "tc-a",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: "run-ambient",
        tool_calls: [
          {
            id: "call-ambient",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "fix", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr-a",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-a","status":"enqueued"}',
        workflow_run_id: "run-ambient",
        tool_result: {
          assistant_message_id: "tc-a",
          tool_call_id: "call-ambient",
          tool: "task",
          tool_args: { agent_type: "implementer", brief: { goal: "fix", done_when: ["Return results."] } },
          content: '{"job_id":"job-a","status":"enqueued"}',
          dispatch: { worker_id: "job-a" },
        },
        created_at: "t",
      },
      {
        id: "b1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: "run-plan",
        created_at: "t",
      },
      {
        id: "m-plan",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "in plan",
        workflow_run_id: "run-plan",
        created_at: "t",
      },
      {
        id: "tc-p",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: "run-plan",
        tool_calls: [
          {
            id: "call-plan",
            name: "task",
            args: { agent_type: "repo-researcher", brief: { goal: "scan", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr-p",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-p","status":"enqueued"}',
        workflow_run_id: "run-plan",
        tool_result: {
          assistant_message_id: "tc-p",
          tool_call_id: "call-plan",
          tool: "task",
          tool_args: { agent_type: "repo-researcher", brief: { goal: "scan", done_when: ["Return results."] } },
          content: '{"job_id":"job-p","status":"enqueued"}',
          dispatch: { worker_id: "job-p" },
        },
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(
      messages,
      [ambientRun, catalogRun],
      catalogRun,
    );
    const planBlock = blocks.find(
      (b) => b.kind === "run" && b.runId === "run-plan",
    );
    expect(planBlock?.kind).toBe("run");
    if (planBlock?.kind === "run") {
      expect(planBlock.items.length).toBeLessThan(
        messages.filter((m) => m.workflow_run_id === "run-plan").length,
      );
    }

    const { container } = render(() => (
      <ChatSpanBlocks
        blocks={blocks}
        messages={messages}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));

    expect(container.querySelectorAll(".den-chat-stream-inner")).toHaveLength(1);

    const planSection = container.querySelector(
      '[data-workflow-run-id="run-plan"]',
    );
    expect(
      planSection?.querySelectorAll('[data-testid="task-card"]').length,
    ).toBe(1);
    const ambientSection = container.querySelector(
      '[data-workflow-run-id="run-ambient"]',
    );
    expect(
      ambientSection?.querySelectorAll('[data-testid="task-card"]').length,
    ).toBe(1);
  });

  it("renders workflow boundaries inline by ord, not above span messages", () => {
    const messages: Message[] = [
      {
        id: "b-start",
        ord: 1,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        workflow_run_id: "run-plan",
        created_at: "t",
      },
      {
        id: "m-plan",
        ord: 2,
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "in plan",
        workflow_run_id: "run-plan",
        created_at: "t",
      },
      {
        id: "b-cancel",
        ord: 3,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "canceled", workflow_id: "plan" },
        workflow_run_id: "run-plan",
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [catalogRun], catalogRun);
    const { container } = render(() => (
      <ChatSpanBlocks
        blocks={blocks}
        messages={messages}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));
    const planSection = container.querySelector(
      '[data-workflow-run-id="run-plan"]',
    );
    const timeline = [
      ...(planSection?.querySelectorAll(
        '[data-testid="workflow-boundary"], .bubble--user',
      ) ?? []),
    ].map((el) => el.textContent?.trim());
    // The cancelled boundary follows its user turn.
    expect(timeline).toEqual([
      "Plan started",
      "in plan",
      "Plan canceled",
    ]);
  });

  it("preserves user bubble DOM when span block reference changes during streaming", async () => {
    const messagesA: Message[] = [
      { id: "u1", ord: 1, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: ambientRun.id, created_at: "t" },
      { id: "a1", ord: 2, role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "hel", workflow_run_id: ambientRun.id, created_at: "t" },
    ];
    const messagesB: Message[] = [
      { id: "u1", ord: 1, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", workflow_run_id: ambientRun.id, created_at: "t" },
      { id: "a1", ord: 2, role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "hello", workflow_run_id: ambientRun.id, created_at: "t" },
    ];
    const first = buildChatTranscriptBlocks(messagesA, [ambientRun], ambientRun);
    const second = buildChatTranscriptBlocks(messagesB, [ambientRun], ambientRun);
    // Stable item keys preserve the user bubble across rebuilt blocks.
    expect(second[0]).not.toBe(first[0]);

    const [blocks, setBlocks] = createSignal(first);
    const [messages, setMessages] = createSignal(messagesA);
    const { container } = render(() => (
      <ChatSpanBlocks
        blocks={blocks()}
        messages={messages()}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));

    const userBubble = container.querySelector(".bubble--user");
    expect(userBubble?.textContent).toContain("hi");

    setBlocks(second);
    setMessages(messagesB);
    await Promise.resolve();

    expect(container.querySelector(".bubble--user")).toBe(userBubble);
  });
});
