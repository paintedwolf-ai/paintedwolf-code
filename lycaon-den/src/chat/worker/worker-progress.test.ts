import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import {
  formatWorkerBudget,
  isWorkerBudgetLow,
  workerBudgetAriaLabel,
  workerBudgetRemaining,
  workerCardProgress,
  workerRunway,
  formatWorkerToolCalls,
  workerToolCallsAriaLabel,
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

describe("worker budget", () => {
  it("sizes runway as a quarter of the ceiling between 2 and 10 rounds, like the host", () => {
    expect(workerRunway(2)).toBe(1);
    expect(workerRunway(3)).toBe(2);
    expect(workerRunway(12)).toBe(3);
    expect(workerRunway(20)).toBe(5);
    expect(workerRunway(40)).toBe(10);
    expect(workerRunway(120)).toBe(10);
  });

  it("returns null when max_tool_loops is missing or zero", () => {
    expect(formatWorkerBudget(worker())).toBeNull();
    expect(formatWorkerBudget(worker({ max_tool_loops: 0 }))).toBeNull();
    expect(workerBudgetRemaining(worker())).toBeNull();
  });

  it("formats running workers as used/max from wire fields", () => {
    expect(
      formatWorkerBudget(worker({ max_tool_loops: 40, tool_loops_used: 34 })),
    ).toBe("34/40");
  });

  it("flags low runway once remaining rounds reach the runway", () => {
    const row = worker({ max_tool_loops: 20, tool_loops_used: 15 });
    expect(workerBudgetRemaining(row)).toBe(5);
    expect(isWorkerBudgetLow(row)).toBe(true);
  });

  it("does not flag low runway while more rounds remain than the runway", () => {
    const row = worker({ max_tool_loops: 20, tool_loops_used: 14 });
    expect(workerBudgetRemaining(row)).toBe(6);
    expect(isWorkerBudgetLow(row)).toBe(false);
  });

  it("shows a running worker's unanswered budget request", () => {
    const row = worker({
      max_tool_loops: 20,
      tool_loops_used: 16,
      budget_request: {
        rounds: 12,
        requested_max: 32,
        remaining_work: ["read the remaining spawn sites"],
        tool_loops_used: 16,
        requested_at: "2026-01-01T00:00:00Z",
      },
    });
    expect(formatWorkerBudget(row)).toBe("16/20 · asked for 32");
    expect(workerBudgetAriaLabel(row)).toBe(
      "Worker budget: 16 of 20 tool rounds used; asked for 32",
    );
  });

  it("drops the request from a finished worker's budget", () => {
    const row = worker({
      status: "complete",
      max_tool_loops: 20,
      tool_loops_used: 20,
      result: { status: "partial", summary: "iteration cap" },
      budget_request: {
        rounds: 12,
        requested_max: 32,
        remaining_work: ["read the remaining spawn sites"],
        tool_loops_used: 16,
        requested_at: "2026-01-01T00:00:00Z",
      },
    });
    expect(formatWorkerBudget(row)).toBe("20/20 · cap");
  });

  it("suffixes cap on terminal partial exhaust", () => {
    expect(
      formatWorkerBudget(
        worker({
          status: "complete",
          max_tool_loops: 40,
          tool_loops_used: 40,
          result: { status: "partial", summary: "iteration cap" },
        }),
      ),
    ).toBe("40/40 · cap");
  });
});

describe("worker tool counters", () => {
  // Only the round pair is bounded, so the unbounded tool count gets its own chip.
  it("keeps the budget chip on rounds alone", () => {
    expect(
      formatWorkerBudget(
        worker({ max_tool_loops: 40, tool_loops_used: 34, tool_calls_used: 128 }),
      ),
    ).toBe("34/40");
    expect(
      workerBudgetAriaLabel(
        worker({ max_tool_loops: 40, tool_loops_used: 34, tool_calls_used: 128 }),
      ),
    ).toBe("Worker budget: 34 of 40 tool rounds used");
  });

  it("counts tool calls in its own chip", () => {
    expect(
      formatWorkerToolCalls(worker({ max_tool_loops: 40, tool_calls_used: 128 })),
    ).toBe("128 tools");
  });

  it("singularizes a lone tool call", () => {
    expect(
      formatWorkerToolCalls(worker({ max_tool_loops: 40, tool_calls_used: 1 })),
    ).toBe("1 tool");
  });

  // Both numbers on this chip count tool calls, so the chip stays in one unit.
  it("appends the in-flight batch while a round is being worked", () => {
    const row = worker({
      max_tool_loops: 40,
      tool_loops_used: 34,
      tool_calls_used: 128,
      turn_tool_calls: 6,
      turn_tools_done: 2,
    });
    expect(formatWorkerToolCalls(row)).toBe("128 tools · 2/6");
    expect(workerToolCallsAriaLabel(row)).toBe(
      "128 tool calls run, 2 of 6 in this round",
    );
  });

  it("names the lifetime count alone for assistive tech between rounds", () => {
    expect(
      workerToolCallsAriaLabel(worker({ max_tool_loops: 40, tool_calls_used: 128 })),
    ).toBe("128 tool calls run");
  });

  it("is absent entirely before the first tool call settles", () => {
    expect(formatWorkerToolCalls(worker({ max_tool_loops: 40 }))).toBeNull();
    expect(workerToolCallsAriaLabel(worker({ max_tool_loops: 40 }))).toBeUndefined();
    expect(formatWorkerBudget(worker({ max_tool_loops: 40 }))).toBe("0/40");
  });
});

/** The batch is only ever read through the card selector. */
function batchOf(row: WorkerTask) {
  return workerCardProgress(row)?.batch ?? null;
}

describe("in-flight tool batch", () => {
  it("reports the batch a running worker is executing", () => {
    expect(
      batchOf(worker({ max_tool_loops: 40, turn_tool_calls: 6, turn_tools_done: 3 })),
    ).toEqual({ done: 3, total: 6, ratio: 0.5 });
  });

  it("is absent between rounds", () => {
    expect(batchOf(worker({ max_tool_loops: 40, turn_tool_calls: 0 }))).toBeNull();
    expect(batchOf(worker({ max_tool_loops: 40 }))).toBeNull();
  });

  // Terminal rows can retain their last published batch.
  it("is absent once the worker is no longer running", () => {
    expect(
      batchOf(
        worker({
          status: "complete",
          max_tool_loops: 40,
          turn_tool_calls: 6,
          turn_tools_done: 3,
          result: { status: "complete", summary: "done" },
        }),
      ),
    ).toBeNull();
  });

  it("clamps a done count past the opened batch size", () => {
    expect(
      batchOf(worker({ max_tool_loops: 40, turn_tool_calls: 2, turn_tools_done: 5 })),
    ).toEqual({ done: 2, total: 2, ratio: 1 });
  });
});

describe("worker card progress", () => {
  it("derives the budget ratio from wire fields only", () => {
    expect(
      workerCardProgress(worker({ max_tool_loops: 10, tool_loops_used: 2 })),
    ).toEqual({ used: 2, max: 10, ratio: 0.2, batch: null });
  });

  it("fills progress to 100% when the worker finishes", () => {
    expect(
      workerCardProgress(
        worker({
          status: "complete",
          max_tool_loops: 40,
          tool_loops_used: 12,
          result: { status: "complete", summary: "done" },
        }),
      ),
    ).toEqual({ used: 12, max: 40, ratio: 1, batch: null });
  });

  it("carries the in-flight batch alongside the budget", () => {
    expect(
      workerCardProgress(
        worker({
          max_tool_loops: 40,
          tool_loops_used: 34,
          turn_tool_calls: 4,
          turn_tools_done: 1,
        }),
      ),
    ).toEqual({
      used: 34,
      max: 40,
      ratio: 34 / 40,
      batch: { done: 1, total: 4, ratio: 0.25 },
    });
  });

  it("is absent without a budget to measure against", () => {
    expect(workerCardProgress(worker())).toBeUndefined();
    expect(workerCardProgress(undefined)).toBeUndefined();
  });
});
