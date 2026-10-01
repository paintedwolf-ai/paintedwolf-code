import { describe, expect, it } from "vitest";
import type { CheckpointStatus, Message } from "../../../api/types.ts";
import { resolvedCheckpointStatuses } from "../../../api/enum-registries.ts";
import { type ActivitySpanEntry, type TranscriptItem } from "./transcript-item-model.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { taskCardStatus } from "../../task/task-card-model.ts";
import { isTaskToolName } from "../../tool/tool-part-model.ts";
import { LONG_RUNNING_COORDINATOR_TOOLS } from "../../tool/tool-presentation.generated.ts";

function taskJobMessages(status: string): Message[] {
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
      content: '{"agent_type":"implementer","job_id":"job-1","status":"canceled"}',
      tool_result: {
        content:
          '{"agent_type":"implementer","job_id":"job-1","status":"canceled"}',
        tool: "task",
        dispatch: { worker_id: "job-1", child_session_id: "child-1" },
        tool_call_id: "tc1",
        assistant_message_id: "a1",
      },
      worker_summary: {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: status as never,
        envelope: `<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="${status}"></task>`,
      },
      created_at: "t",
    },
  ];
}

function toolWithCheckpointDecision(
  status: Exclude<CheckpointStatus, "pending">,
): Message[] {
  return [
    {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [{ id: "tc-command", name: "command", args: { command: "rm -rf /" } }],
      created_at: "t",
    },
    {
      id: "tr-command",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "denied",
      tool_result: {
        content: "denied",
        tool: "command",
        tool_call_id: "tc-command",
        assistant_message_id: "a1",
        outcome: "rejected",
        checkpoint_decision: {
          checkpoint_id: "chk-1",
          kind: "tool_approval",
          status,
        },
      },
      created_at: "t",
    },
  ];
}

function toolCards(
  items: readonly TranscriptItem[],
  tool?: string,
): Array<Extract<ActivitySpanEntry, { kind: "tool" }>> {
  return items.flatMap((item) =>
    item.kind === "tool" && (!tool || item.part.tool === tool)
      ? [{ kind: "tool" as const, part: item.part }]
      : item.kind === "worker_group"
        ? item.parts
            .filter((part) => !tool || part.tool === tool)
            .map((part) => ({ kind: "tool" as const, part }))
      : item.kind === "activity_span"
      ? item.entries.filter(
          (
            entry,
          ): entry is Extract<ActivitySpanEntry, { kind: "tool" }> =>
            entry.kind === "tool" && (!tool || entry.part.tool === tool),
        )
      : [],
  );
}

function taskCards(items: readonly TranscriptItem[]) {
  return toolCards(items, "task");
}

function inFlightVisibleToolArgs(tool: string) {
  return isTaskToolName(tool)
    ? {
        agent_type: "implementer",
        brief: { goal: "build", done_when: ["Return results."] },
      }
    : { path: "lycaon/internal/summarize", task: "How does the summarize tool work?" };
}

function inFlightVisibleToolRejected(tool: string): Message[] {
  return [
    {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [
        {
          id: "tc1",
          name: tool,
          args: inFlightVisibleToolArgs(tool),
        },
      ],
      created_at: "t",
    },
    {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
      tool_result: {
        content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
        outcome: "rejected",
        codes: ["TOOL_ARGS_INVALID"],
        tool,
        tool_call_id: "tc1",
        assistant_message_id: "a1",
        // Long-running tools keep the chicklet when the settle is benign.
        ui_visibility: "benign",
      },
      created_at: "t",
    },
  ];
}

/** A repeated summarize call settled with DOOM_LOOP_REPEAT_WARN and benign visibility. */
function inFlightVisibleToolCompletedBenign(tool: string): Message[] {
  return [
    {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [
        {
          id: "tc1",
          name: tool,
          args: inFlightVisibleToolArgs(tool),
        },
      ],
      created_at: "t",
    },
    {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"brief":["partial"],"completeness":"partial"}',
      tool_result: {
        content: '{"brief":["partial"],"completeness":"partial"}',
        outcome: "completed",
        codes: ["DOOM_LOOP_REPEAT_WARN"],
        tool,
        tool_call_id: "tc1",
        assistant_message_id: "a1",
        ui_visibility: "benign",
      },
      created_at: "t",
    },
  ];
}

describe("action completeness invariant", () => {
  it("workflow-stop canceled worker keeps one durable card", () => {
    const items = createTranscriptDisplayProjector()(taskJobMessages("canceled"), undefined)[0]!;
    const cards = taskCards(items);
    expect(cards).toHaveLength(1);
    expect(taskCardStatus(cards[0]!.part)).toBe("canceled");
  });

  it("in-flight-visible tool rejects stay as durable error cards (verbose off)", () => {
    for (const tool of LONG_RUNNING_COORDINATOR_TOOLS) {
      const items = createTranscriptDisplayProjector()(inFlightVisibleToolRejected(tool), undefined, {
        verboseMode: false,
      })[0]!;
		const cards = toolCards(items, tool);
      expect(cards, tool).toHaveLength(1);
      expect(cards[0]!.part.status, tool).toBe("error");
    }
  });

  it("failed worker dispatch rejects hide when verbose is off", () => {
    const items = createTranscriptDisplayProjector()(inFlightVisibleToolRejected("task"), undefined, {
      verboseMode: false,
    })[0]!;
    expect(
		toolCards(items, "task"),
    ).toHaveLength(0);
  });

  it("failed worker dispatch rejects stay as durable error cards when verbose is on", () => {
    const items = createTranscriptDisplayProjector()(inFlightVisibleToolRejected("task"), undefined, {
      verboseMode: true,
    })[0]!;
	const cards = toolCards(items, "task");
    expect(cards).toHaveLength(1);
    expect(cards[0]!.part.status).toBe("error");
  });

  it("in-flight-visible completed+benign (doom-loop warn) never vanishes (verbose off)", () => {
    for (const tool of LONG_RUNNING_COORDINATOR_TOOLS) {
      const items = createTranscriptDisplayProjector()(inFlightVisibleToolCompletedBenign(tool), undefined, { verboseMode: false })[0]!;
		const cards = toolCards(items, tool);
      expect(cards, tool).toHaveLength(1);
      expect(cards[0]!.part.status, tool).toBe("completed");
    }
  });

  it("resolved checkpoint retains the checkpoint lifecycle key", () => {
    for (const status of resolvedCheckpointStatuses()) {
      const msgs = toolWithCheckpointDecision(status);
      const items = createTranscriptDisplayProjector()(msgs, undefined, { verboseMode: true })[0]!;
      const checkpoints = items.filter((i) => i.kind === "checkpoint");
      expect(checkpoints, status).toHaveLength(1);
      expect(checkpoints[0]!.key).toBe("checkpoint:chk-1");
      const toolRows = msgs.filter((m) => m.role === "tool");
      expect(toolRows[0]!.tool_result?.checkpoint_decision?.status).toBe(status);
    }
  });
});
