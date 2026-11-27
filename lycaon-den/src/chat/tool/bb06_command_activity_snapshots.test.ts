import { beforeEach, describe, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { clearCommandActivities, observeCommandActivity } from "./command-activity-live.ts";
import { useCommandActivitySnapshots } from "./command-activity-snapshots.ts";
import { useActionCoverage } from "../action/action-source-coverage.ts";
import type { CardAction } from "../action/action-merge.ts";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";

vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

async function flushMicrotasks(rounds = 8): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve();
  }
}

function settledProgram(id: string, revision = 10): CommandProgramReceipt {
  return {
    id,
    state: "completed",
    activity_revision: revision,
  } as CommandProgramReceipt;
}

function toolEntry(program: CommandProgramReceipt, callId: string): Extract<ActivitySpanEntry, { kind: "tool" }> {
  return {
    kind: "tool",
    part: {
      id: `row-${callId}`,
      toolCallId: callId,
      tool: "command",
      kind: "command",
      status: program.state === "running" ? "running" : "completed",
      assistantMessageId: "assistant-1",
      messageId: `msg-${callId}`,
      commandProgram: program,
    },
  };
}

function receiptFact(id: string, state: string, count = 3) {
  return {
    id,
    label: "Run process",
    subject: `cmd-${id}`,
    group: "Running programs",
    state,
    outcome: state === "completed" ? "Finished" : "Running",
    execution_ids: [id],
    spans: [],
    steps: [],
    execution_count: count,
  };
}

describe("BB06 Den command activity snapshots & leads L01/L07/M10", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    clearCommandActivities("session-bb06");
  });

  it("M10: handles live-to-terminal transition, ignores stale live events, and agrees after reload", async () => {
    const running = { id: "prog-live", state: "running", activity_revision: 5, activities: [receiptFact("act-1", "running", 1)] } as unknown as CommandProgramReceipt;
    const terminal = { id: "prog-live", state: "completed", activity_revision: 8, activities: [receiptFact("act-1", "completed", 4)] } as unknown as CommandProgramReceipt;
    const stale = { id: "prog-live", state: "running", activity_revision: 3, activities: [receiptFact("act-1", "running", 1)] } as unknown as CommandProgramReceipt;

    const get = vi.fn().mockResolvedValue({ program: terminal, activities: terminal.activities, executions: [], nodes: [] });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));

    // 1. Live SSE running -> terminal -> stale event ignored.
    observeCommandActivity({
      session_id: "session-bb06", tool_call_id: "call-live", tool_name: "command",
      activity_id: "lease-1", kind: "running_tool", status: "active", started_at: "2026-11-26T10:00:00Z",
      progress: { phase: "executing", files_searched: 0, files_skipped: 0, matches: 0, command_program: running },
    });
    observeCommandActivity({
      session_id: "session-bb06", tool_call_id: "call-live", tool_name: "command",
      activity_id: "lease-1", kind: "running_tool", status: "done", started_at: "2026-11-26T10:00:00Z",
      progress: { phase: "executing", files_searched: 0, files_skipped: 0, matches: 0, command_program: terminal },
    });
    observeCommandActivity({
      session_id: "session-bb06", tool_call_id: "call-live", tool_name: "command",
      activity_id: "lease-1", kind: "running_tool", status: "active", started_at: "2026-11-26T10:00:00Z",
      progress: { phase: "executing", files_searched: 0, files_skipped: 0, matches: 0, command_program: stale },
    });

    const entry = toolEntry({ id: "prog-live", state: "completed", activity_revision: 8 } as CommandProgramReceipt, "call-live");
    let liveView: CommandProgramReceipt | undefined;
    const disposeLive = createRoot((dispose) => {
      const resolve = useCommandActivitySnapshots(() => [entry], () => "session-bb06");
      liveView = resolve(entry.part);
      return dispose;
    });
    expect(liveView?.state).toBe("completed");
    expect(liveView?.activity_revision).toBe(8);
    expect(liveView?.activities?.[0]?.execution_count).toBe(4);
    expect(get).not.toHaveBeenCalled();
    disposeLive();

    // 2. Reload clears live store; retained snapshot fetches from host and agrees.
    clearCommandActivities("session-bb06");
    let reloadedResolve!: ReturnType<typeof useCommandActivitySnapshots>;
    const disposeReload = createRoot((dispose) => {
      reloadedResolve = useCommandActivitySnapshots(() => [entry], () => "session-bb06");
      return dispose;
    });
    await flushMicrotasks();
    const reloadedView = reloadedResolve(entry.part);
    expect(reloadedView?.state).toBe(liveView?.state);
    expect(reloadedView?.activity_revision).toBe(liveView?.activity_revision);
    expect(reloadedView?.activities?.[0]?.execution_count).toBe(liveView?.activities?.[0]?.execution_count);
    expect(get).toHaveBeenCalledTimes(1);
    disposeReload();
  });

  it("M10: expired or unavailable receipt does not synthesize terminal success or exit 0", async () => {
    const missing = { id: "prog-expired", state: "failed", activity_revision: 4 } as CommandProgramReceipt;
    const entry = toolEntry(missing, "call-expired");
    const get = vi.fn().mockRejectedValue(new LycaonApiError("command program not found", 404, "command_program_not_found"));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));

    let resolve!: ReturnType<typeof useCommandActivitySnapshots>;
    const dispose = createRoot((d) => {
      resolve = useCommandActivitySnapshots(() => [entry], () => "session-bb06");
      return d;
    });
    await flushMicrotasks();
    const shown = resolve(entry.part);
    expect(shown?.activities).toBeUndefined();
    expect(shown?.state).toBe("failed");
    expect(shown?.status_code).toBeUndefined();
    dispose();
  });

  it("L01: pagination append and remount must not re-fetch unchanged settled programs (?limit=1 storm)", async () => {
    const counts = new Map<string, number>();
    const get = vi.fn().mockImplementation(async (_session: string, programId: string) => {
      counts.set(programId, (counts.get(programId) ?? 0) + 1);
      const program = settledProgram(programId, 10);
      return { program, activities: [receiptFact(`act-${programId}`, "completed", 2)], executions: [], nodes: [] };
    });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));

    const initialEntries = [
      toolEntry(settledProgram("settled-1"), "call-1"),
      toolEntry(settledProgram("settled-2"), "call-2"),
      toolEntry(settledProgram("settled-3"), "call-3"),
      toolEntry(settledProgram("settled-4"), "call-4"),
    ];
    const [entries, setEntries] = createSignal<readonly ActivitySpanEntry[]>(initialEntries);

    const disposeFirst = createRoot((dispose) => {
      useCommandActivitySnapshots(entries, () => "session-bb06");
      return dispose;
    });
    await flushMicrotasks(12);
    expect(counts.get("settled-1")).toBe(1);
    expect(counts.get("settled-4")).toBe(1);

    // Simulate /messages pagination loading an older settled program.
    setEntries([...initialEntries, toolEntry(settledProgram("settled-5"), "call-5")]);
    await flushMicrotasks(12);

    // Simulate remount/scroll cycle for the same settled transcript window.
    disposeFirst();
    const disposeRemount = createRoot((dispose) => {
      useCommandActivitySnapshots(entries, () => "session-bb06");
      return dispose;
    });
    await flushMicrotasks(12);
    disposeRemount();

    // DEFECT L01: On 899b51c5, key = programs.map(p => p.id).join(":") causes
    // createResource to re-fetch ALL 4 unchanged settled programs on pagination
    // append AND again on remount (counts.get("settled-1") === 3 instead of 1).
    for (const id of ["settled-1", "settled-2", "settled-3", "settled-4"]) {
      expect(counts.get(id), `DEFECT L01 REPRODUCED: settled program ${id} fetched ${counts.get(id)}x (want 1)`).toBe(1);
    }
  });

  it("L07: pruned command-window 404 (source_history_not_found) must stop re-polling on source invalidation", async () => {
    const getObservation = vi.fn().mockRejectedValue(
      new LycaonApiError("Command observation not found.", 404, "source_history_not_found"),
    );
    const client = stubClient({ getProjectSourceCommandObservation: getObservation });
    const action: CardAction = {
      label: "Run command",
      status: "done",
      ran: { text: "ls", noun: "command" },
      row: [],
      links: [],
      facts: [{ kind: "source_observation", source_windows: ["pruned-win-1"] }],
    };

    const dispose = createRoot((d) => {
      useActionCoverage(() => action, { client, projectId: "proj-bb06", sessionId: "session-bb06" });
      return d;
    });
    await flushMicrotasks();

    for (let i = 0; i < 4; i++) {
      invalidateSourceQueries({ projectId: "proj-bb06" });
      await flushMicrotasks();
    }
    dispose();

    // DEFECT L07: On 899b51c5, action-source-coverage.ts:47 checks
    // isApiErrorCode(error, ["not_found", "session_not_found"]), omitting
    // "source_history_not_found", so pruned-win-1 stays in pending and re-polls
    // on every source invalidation (5x instead of 1x).
    expect(
      getObservation.mock.calls.length,
      `DEFECT L07 REPRODUCED: pruned command-window polled ${getObservation.mock.calls.length}x after source_history_not_found (want 1)`,
    ).toBe(1);
  });
});
