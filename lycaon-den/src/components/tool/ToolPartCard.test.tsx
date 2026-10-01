import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { ToolPartCard } from "./ToolPartCard.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";

const rejectedTask: ToolPartView = {
  id: "tc-reject",
  toolCallId: "tc-reject",
  assistantMessageId: "assistant-message",
  messageId: "m1",
  tool: "task",
  kind: "task",
  status: "error",
  args: {
    agent_type: "repo-researcher",
    brief: { goal: "verify chmod", done_when: ["Return findings."] },
  },
  output:
    "Rejected: Reply to the user from the worker summary before calling any tools\nCode: IMPLEMENT_REPLY_BEFORE_ORIENTATION",
  error:
    "Rejected: Reply to the user from the worker summary before calling any tools\nCode: IMPLEMENT_REPLY_BEFORE_ORIENTATION",
};

const enqueuedTask: ToolPartView = {
  id: "tc-ok",
  toolCallId: "tc-ok",
  assistantMessageId: "assistant-message",
  messageId: "m2",
  tool: "task",
  kind: "task",
  status: "completed",
  args: {
    agent_type: "repo-researcher",
    brief: { goal: "verify chmod", done_when: ["Return findings."] },
  },
  output: JSON.stringify({ job_id: "job-1", status: "enqueued" }),
  error: null,
};

describe("ToolPartCard", () => {
  it("renders task tool parts (including pre-enqueue rejects) as worker cards", () => {
    const { container } = render(() => (
      <ToolPartCard part={rejectedTask} layout="chat" />
    ));
    expect(container.querySelector(".den-tool-part-chicklet")).toBeNull();
    const card = container.querySelector('[data-testid="task-card"]');
    expect(card).toBeTruthy();
    expect(card?.getAttribute("data-task-status")).toBe("error");
    expect(container.textContent).toContain("verify chmod");
  });

  it("renders enqueued task dispatches as worker task cards", () => {
    const { container } = render(() => (
      <ToolPartCard
        part={enqueuedTask}
        layout="chat"
        taskWorker={() => ({
          id: "job-1",
          parent_session_id: "sess-1",
          agent_type: "repo-researcher",
          status: "running",
          created_at: "2026-01-01T00:00:00Z",
        })}
      />
    ));
    expect(container.querySelector('[data-testid="task-card"]')).toBeTruthy();
    expect(container.querySelector(".den-tool-part-chicklet")).toBeNull();
    expect(container.textContent).toContain("repo-researcher");
    expect(container.textContent).toContain("verify chmod");
  });

  it("uses matched worker agent type when dispatch args are missing", () => {
    const envelope =
      '<task job_id="job-cv" agent_type="implementer" state="complete"><summary>ok</summary></task>';
    const part: ToolPartView = {
      id: "tc-cv",
      toolCallId: "tc-cv",
      assistantMessageId: "assistant-message",
      messageId: "m3",
      tool: "task",
      kind: "task",
      status: "completed",
      args: {},
      output: envelope,
      error: null,
      jobId: "job-cv",
    };
    const { container } = render(() => (
      <ToolPartCard
        part={part}
        layout="chat"
        taskWorker={() => ({
          id: "job-cv",
          parent_session_id: "sess-1",
          agent_type: "implementer",
          status: "complete",
          created_at: "2026-01-01T00:00:00Z",
        })}
      />
    ));
    expect(container.textContent).toContain("Worker · implementer");
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeNull();
  });

  it("renders in-flight summarize as a generic tool card, not a worker card", () => {
    const part: ToolPartView = {
      id: "tc-sum",
      toolCallId: "call_e4badb27",
      assistantMessageId: "assistant-message",
      messageId: "a1",
      tool: "summarize",
      kind: "generic",
      status: "running",
      title: "What is this repo's purpose…",
      args: { task: "What is this repo's purpose…", paths: ["README.md"] },
    };
    const { container } = render(() => (
      <ToolPartCard part={part} layout="chat" onOpenWorker={() => {}} />
    ));
    expect(container.querySelector('[data-testid="task-card"]')).toBeNull();
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeTruthy();
  });
});
