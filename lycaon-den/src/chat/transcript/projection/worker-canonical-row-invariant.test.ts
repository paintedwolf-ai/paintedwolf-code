import { describe, expect, it } from "vitest";
import type { Message, WorkerSummaryStatus } from "../../../api/types.ts";
import { allWorkerSummaryStatuses } from "../../../api/enum-registries.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { taskCardStatus } from "../../task/task-card-model.ts";

/** Each persisted worker status renders on one task row. */

type ToolItem = Extract<TranscriptItem, { kind: "tool" }>;

function taskJobMessages(status: WorkerSummaryStatus): Message[] {
  return [
    { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "ship it", created_at: "t" },
    {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [
        {
          id: "tc1",
          name: "task",
          args: {
            agent_type: "implementer",
            brief: { goal: "wire parser", done_when: ["Return results."] },
          },
        },
      ],
      created_at: "t",
    },
    {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content:
        '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
      tool_result: {
        content:
          '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
        dispatch: { worker_id: "job-1", child_session_id: "child-1" },
        tool: "task",
        tool_call_id: "tc1",
        assistant_message_id: "a1",
        tool_args: {
          agent_type: "implementer",
          brief: { goal: "wire parser", done_when: ["Return results."] },
        },
      },
      worker_summary: {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status,
        envelope: `<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="${status}"></task>`,
      },
      created_at: "t",
    },
  ];
}

function taskCards(items: readonly TranscriptItem[]): ToolItem[] {
  return items.flatMap((item) =>
    item.kind === "tool" && item.part.tool === "task"
      ? [item]
      : item.kind === "worker_group"
        ? item.parts.map((part) => ({ kind: "tool" as const, key: part.id, part }))
      : item.kind === "activity_span"
      ? item.entries.flatMap((entry) =>
          entry.kind === "tool"
            ? [{ kind: "tool" as const, key: entry.part.id, part: entry.part }]
            : [],
        )
      : [],
  );
}

function render(messages: Message[]): TranscriptItem[] {
  return createTranscriptDisplayProjector()(messages, undefined)[0]!;
}

describe("worker canonical row invariant", () => {
  it("renders exactly one card per WorkerSummaryStatus", () => {
    for (const status of allWorkerSummaryStatuses()) {
      const cards = taskCards(render(taskJobMessages(status)));
      expect(cards, `status=${status}`).toHaveLength(1);
    }
  });

  it("needs_decision is a status on the one canonical card, never a second card", () => {
    const messages = taskJobMessages("needs_decision");
    const items = render(messages);
    expect(taskCards(items)).toHaveLength(1);
    expect(taskCardStatus(taskCards(items)[0]!.part)).toBe("needs_decision");
  });

  it("a terminal-canceled row still renders one card — it does not disappear", () => {
    const messages = taskJobMessages("canceled");
    const items = render(messages);
    const cards = taskCards(items);
    expect(cards).toHaveLength(1);
    expect(taskCardStatus(cards[0]!.part)).toBe("canceled");
  });
});
