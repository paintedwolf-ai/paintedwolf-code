import { afterEach, describe, expect, it } from "vitest";
import type { ActivityEvent, Message } from "../../api/types.ts";
import {
  applyBackgroundProcessEvent,
  applyBackgroundProcessOutputs,
  resetBackgroundProcessStoreForTests,
} from "../tool/background-process-store.ts";
import { resolveThinkingActivityLabel } from "./thinking-activity-label.ts";
import {
  RUNNING_COMMAND_WAIT_LABEL,
  TERMINAL_WAIT_DELIVERY_LABEL,
  extractWaitConditionSpec,
  hasImmediateWaitTransition,
  isPendingWaitPart,
  observeBackgroundHandleState,
  resolveBlockingHostPhaseLabel,
  resolveLiveProcessCardPresentation,
  resolveWaitTargetLabel,
} from "./wait-target-activity.ts";

afterEach(() => resetBackgroundProcessStoreForTests());

function commandResultMessage(id: string, handle: string, running: boolean): Message {
  return {
    id,
    role: "tool",
    origin: "tool",
    authority: "none",
    trust_tier: "untrusted",
    content: "",
    created_at: "2026-11-23T17:00:39Z",
    tool_result: {
      tool: "command",
      assistant_message_id: "cmd-assistant",
      tool_call_id: "cmd-call",
      content: "",
      outcome: "completed",
      process: { handle, running },
    },
  };
}

describe("wait-target-activity", () => {
  it("correlates exact process_done handles by session and preserves neutral fallback on unknown or mixed handles", () => {
    const spec = extractWaitConditionSpec({
      conditions: [
        { kind: "process_done", handles: [" h1 ", "h1"] },
        { kind: "http_ready", url: "http://localhost:8080" },
      ],
    });
    expect(spec).toEqual({
      triggers: ["process_done", "http_ready"],
      processDoneHandles: ["h1"],
      hasUnscopedProcessDone: false,
      otherTriggers: ["http_ready"],
    });

    const msgs: Message[] = [commandResultMessage("r1", "h1", true)];
    expect(observeBackgroundHandleState("s1", "h1", msgs)).toBe("running");
    expect(observeBackgroundHandleState("s2", "h1", [])).toBe("unknown");
    expect(observeBackgroundHandleState(undefined, "h1", msgs)).toBe("unknown");

    expect(
      resolveWaitTargetLabel({ conditions: [{ kind: "process_done", handles: ["h1"] }] }, "s1", msgs),
    ).toEqual({ state: "running_target", label: RUNNING_COMMAND_WAIT_LABEL });

    applyBackgroundProcessEvent({
      session_id: "s2",
      process_id: "h1",
      stream: "exit",
      end_offset: 0,
      running: false,
      exit_code: 0,
    });
    expect(
      resolveWaitTargetLabel({ conditions: [{ kind: "process_done", handles: ["h1"] }] }, "s1", msgs),
    ).toEqual({ state: "running_target", label: RUNNING_COMMAND_WAIT_LABEL });

    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "h1",
      stream: "exit",
      end_offset: 0,
      running: false,
      exit_code: 0,
    });
    expect(
      resolveWaitTargetLabel({ conditions: [{ kind: "process_done", handles: ["h1"] }] }, "s1", msgs),
    ).toEqual({ state: "pending_delivery", label: TERMINAL_WAIT_DELIVERY_LABEL });

    // Mixed running + terminal handles stay neutral.
    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "h2",
      stream: "stdout",
      end_offset: 1,
      running: true,
    });
    expect(
      resolveWaitTargetLabel({ conditions: [{ kind: "process_done", handles: ["h1", "h2"] }] }, "s1", msgs),
    ).toEqual({ state: "neutral", label: "Waiting" });
    expect(
      resolveWaitTargetLabel({ conditions: [{ kind: "process_done", handles: ["unknown"] }] }, "s1", msgs),
    ).toEqual({ state: "neutral", label: "Waiting" });
  });

  it("prioritizes blocking host phases over pending wait delivery and returns on completion", () => {
    applyBackgroundProcessOutputs("s1", [
      { process_id: "h1", running: false, exit_code: 0, termination_reason: "exited", chunks: [] },
    ]);
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "run", created_at: "t" },
      {
        id: "a1",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        created_at: "t",
        tool_calls: [{ id: "wait-1", name: "wait", args: { conditions: [{ kind: "process_done", handles: ["h1"] }] } }],
      },
    ];
    const waitRunning: ActivityEvent = {
      activity_id: "act-wait",
      session_id: "s1",
      kind: "running_tool",
      status: "active",
      started_at: "2026-11-23T17:01:24Z",
      tool_name: "wait",
      tool_call_id: "wait-1",
    };
    const indexing: ActivityEvent = {
      activity_id: "act-cred",
      session_id: "s1",
      kind: "indexing_credentials",
      status: "active",
      started_at: "2026-11-23T17:01:20Z",
      indexing: { root_id: "r1", root_label: "web_app", directories_visited: 12, credential_files_read: 1 },
    };

    expect(hasImmediateWaitTransition("s1", messages, [waitRunning])).toBe(true);
    expect(resolveThinkingActivityLabel({ sessionId: "s1", messages, activities: [waitRunning, indexing] }))
      .toBe("Checking credential files in web_app · 12 folders · 1 file...");
    expect(resolveBlockingHostPhaseLabel([waitRunning, indexing], "s1"))
      .toBe("Checking credential files in web_app · 12 folders · 1 file");

    expect(
      resolveThinkingActivityLabel({
        sessionId: "s1",
        messages,
        activities: [waitRunning, { ...indexing, status: "done" }],
      }),
    ).toBe(`${TERMINAL_WAIT_DELIVERY_LABEL}...`);

    expect(isPendingWaitPart({ tool: "wait", toolCallId: "wait-1", status: "completed", completion: { operation: "wait", state: "parked" } }, [
      { ...waitRunning, kind: "awaiting_wake" },
    ])).toBe(true);
    expect(isPendingWaitPart({ tool: "wait", toolCallId: "wait-1", status: "completed", completion: null }, [waitRunning])).toBe(false);
  });

  it("resolves live background command card outcomes across exit codes and cancellation reasons", () => {
    const runningCard = {
      label: "Run command",
      status: "running" as const,
      ran: { text: "make build", noun: "command" as const },
      facts: [],
      row: [],
      links: [],
    };
    applyBackgroundProcessEvent({ session_id: "s1", process_id: "p1", stream: "exit", end_offset: 0, running: false, exit_code: 2 });
    expect(resolveLiveProcessCardPresentation("s1", "p1", runningCard)).toEqual({
      status: "error",
      outcome: "Failed · exit 2",
    });

    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "p2",
      stream: "exit",
      end_offset: 0,
      running: false,
      termination_reason: "stopped_by_user",
    });
    expect(resolveLiveProcessCardPresentation("s1", "p2", runningCard)).toEqual({
      status: "completed",
      outcome: "Stopped by you",
    });

    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "p3",
      stream: "exit",
      end_offset: 0,
      running: false,
      termination_reason: "timed_out",
    });
    expect(resolveLiveProcessCardPresentation("s1", "p3", runningCard)).toEqual({
      status: "error",
      outcome: "Timed out",
    });
  });
});
