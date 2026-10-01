import { render } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import { ToolPartCard } from "../../components/tool/ToolPartCard.tsx";
import {
  finalizeTranscriptItems,
  messagesToTranscriptItems,
} from "../transcript/projection/transcript-items.ts";
import { toolPartFromCall } from "../tool/tool-part-model.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";

const task83Assistant: Message = {
  id: "98734ad1-0eed-43b0-a562-6fa6a4a96006",
  role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
  content: "",
  tool_calls: [
    {
      id: "functions.task:83",
      name: "task",
      args: {
        agent_type: "implementer",
        brief: {
          goal: "Fix the remaining 7 test failures.",
          done_when: ["Return results."],
        },
      },
    },
  ],
  created_at: "2026-06-20T13:10:52Z",
};

const task83Tool: Message = {
  id: "c4a766af-c9d1-461e-8a5b-c745821d854f",
  role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
  content:
    '<task job_id="ac6733a3-bc5c-467e-a05b-18ff00d0e4c1" child_session_id="d87974d8-d54d-4ca7-b9b1-08d6e6f57ea4" agent_type="implementer" state="open"/>',
  tool_result: {
    content:
      '<task job_id="ac6733a3-bc5c-467e-a05b-18ff00d0e4c1" child_session_id="d87974d8-d54d-4ca7-b9b1-08d6e6f57ea4" agent_type="implementer" state="open"/>',
    outcome: "completed",
    codes: ["BANNER_TASK_QUEUED"],
    dispatch: {
      worker_id: "ac6733a3-bc5c-467e-a05b-18ff00d0e4c1",
      child_session_id: "d87974d8-d54d-4ca7-b9b1-08d6e6f57ea4",
    },
    tool: "task",
    tool_call_id: "functions.task:83",
    assistant_message_id: task83Assistant.id,
  },
  worker_summary: {
    worker_id: "ac6733a3-bc5c-467e-a05b-18ff00d0e4c1",
    child_session_id: "4195f804-a7c8-4313-90bf-5f08353afcf9",
    agent_type: "implementer",
    status: "open",
    envelope: '<task job_id="ac6733a3-bc5c-467e-a05b-18ff00d0e4c1" child_session_id="4195f804-a7c8-4313-90bf-5f08353afcf9" agent_type="implementer" state="open"></task>',
  },
  created_at: "2026-06-20T13:10:53Z",
};

describe("session 4195f804 task card regression", () => {
  beforeEach(async () => {
    await saveVerboseMode(true);
  });

  it("routes finalized XML task tool results through TaskCard", () => {
    const part = toolPartFromCall(
      task83Assistant.tool_calls![0]!,
      task83Tool,
      task83Assistant.id,
    );
    const { container } = render(() => (
      <ToolPartCard part={part} layout="chat" />
    ));
    expect(container.querySelector('[data-testid="task-card"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeNull();
    expect(container.textContent).toContain("Worker · implementer");
  });

  it("keeps one canonical task card when worker status is patched", () => {
    const items = finalizeTranscriptItems(
      messagesToTranscriptItems(
        [task83Assistant, task83Tool],
        { verboseMode: true, layout: "chat" },
      ),
    );
    const groups = items.filter((item) => item.kind === "worker_group");
    expect(groups).toHaveLength(1);
    expect(groups[0]?.parts.map((part) => part.tool)).toEqual(["task"]);
    expect(items.some((item) => item.kind === "activity_span")).toBe(false);
  });
});
