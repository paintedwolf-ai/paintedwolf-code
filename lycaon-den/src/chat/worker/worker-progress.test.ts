import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import {
  formatWorkerToolCalls,
  formatWorkerTurns,
  workerCardProgress,
  workerToolCallsAriaLabel,
  workerTurnsAriaLabel,
} from "./worker-progress.ts";

function worker(overrides: Partial<WorkerTask> = {}): WorkerTask {
  return {
    id: "job-1",
    agent_type: "implementer",
    status: "running",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("worker turns", () => {
  it("counts the model turns the round has taken", () => {
    expect(formatWorkerTurns(worker({ tool_loops_used: 34 }))).toBe("34 turns");
    expect(workerTurnsAriaLabel(worker({ tool_loops_used: 34 }))).toBe("34 model turns taken");
  });

  it("singularizes a lone turn", () => {
    expect(formatWorkerTurns(worker({ tool_loops_used: 1 }))).toBe("1 turn");
    expect(workerTurnsAriaLabel(worker({ tool_loops_used: 1 }))).toBe("1 model turn taken");
  });

  it("is absent before the first turn settles", () => {
    expect(formatWorkerTurns(worker())).toBeNull();
    expect(workerTurnsAriaLabel(worker())).toBeUndefined();
  });
});

describe("worker tool counters", () => {
  it("counts tool calls in its own chip", () => {
    expect(formatWorkerToolCalls(worker({ tool_calls_used: 128 }))).toBe("128 tools");
  });

  it("singularizes a lone tool call", () => {
    expect(formatWorkerToolCalls(worker({ tool_calls_used: 1 }))).toBe("1 tool");
  });

  // Both numbers on this chip count tool calls, so the chip stays in one unit.
  it("appends the in-flight batch while a round is being worked", () => {
    const row = worker({ tool_loops_used: 34, tool_calls_used: 128, turn_tool_calls: 6, turn_tools_done: 2 });
    expect(formatWorkerToolCalls(row)).toBe("128 tools · 2/6");
    expect(workerToolCallsAriaLabel(row)).toBe("128 tool calls run, 2 of 6 in this round");
  });

  it("names the lifetime count alone for assistive tech between batches", () => {
    expect(workerToolCallsAriaLabel(worker({ tool_calls_used: 128 }))).toBe("128 tool calls run");
  });

  it("is absent entirely before the first tool call settles", () => {
    expect(formatWorkerToolCalls(worker())).toBeNull();
    expect(workerToolCallsAriaLabel(worker())).toBeUndefined();
  });
});

describe("worker card progress", () => {
  it("reports the batch a running worker is executing", () => {
    expect(workerCardProgress(worker({ turn_tool_calls: 6, turn_tools_done: 3 }))).toEqual({
      batch: { done: 3, total: 6, ratio: 0.5 },
    });
  });

  it("is absent between batches", () => {
    expect(workerCardProgress(worker({ turn_tool_calls: 0 }))).toBeUndefined();
    expect(workerCardProgress(worker({ tool_loops_used: 12 }))).toBeUndefined();
  });

  // Terminal rows can retain their last published batch.
  it("is absent once the worker is no longer running", () => {
    expect(
      workerCardProgress(
        worker({
          status: "complete",
          turn_tool_calls: 6,
          turn_tools_done: 3,
          result: { status: "complete", summary: "done" },
        }),
      ),
    ).toBeUndefined();
  });

  it("clamps a done count past the opened batch size", () => {
    expect(workerCardProgress(worker({ turn_tool_calls: 2, turn_tools_done: 5 }))).toEqual({
      batch: { done: 2, total: 2, ratio: 1 },
    });
  });

  it("is absent without a worker", () => {
    expect(workerCardProgress(undefined)).toBeUndefined();
  });
});
