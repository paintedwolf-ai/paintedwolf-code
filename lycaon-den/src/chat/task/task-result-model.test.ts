import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import {
  isEnqueuedWorkerDispatchResult,
  taskJobIdFromToolMessage,
} from "./task-result-model.ts";

describe("task-result-model", () => {
  it("isEnqueuedWorkerDispatchResult keys off typed dispatch identity", () => {
    const enqueued: Message = {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"job_id":"job-1"}',
      tool_result: {
        content: '{"job_id":"job-1"}',
        dispatch: { worker_id: "job-1" },
      },
      created_at: "t",
    };
    expect(isEnqueuedWorkerDispatchResult("task", enqueued)).toBe(true);
    expect(isEnqueuedWorkerDispatchResult("read", enqueued)).toBe(false);
    expect(isEnqueuedWorkerDispatchResult("task", undefined)).toBe(false);
  });

  it("isEnqueuedWorkerDispatchResult accepts benign XML with typed dispatch", () => {
    const xml =
      '<task job_id="job-xml" child_session_id="child-1" agent_type="implementer" state="open"/>';
    const enqueued: Message = {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: xml,
      tool_result: {
        content: xml,
        outcome: "completed",
        codes: ["BANNER_TASK_QUEUED"],
        ui_visibility: "benign",
        dispatch: { worker_id: "job-xml", child_session_id: "child-1", agent_type: "implementer" },
      },
      created_at: "t",
    };
    expect(isEnqueuedWorkerDispatchResult("task", enqueued)).toBe(true);
  });

  it("does not infer job identity from result content", () => {
    const result: Message = {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"job_id":"content-only"}',
      tool_result: { content: '{"job_id":"content-only"}' },
      created_at: "t",
    };
    expect(taskJobIdFromToolMessage(result)).toBeUndefined();
  });
});
