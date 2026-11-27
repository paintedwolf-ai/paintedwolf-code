import { createRoot, createSignal } from "solid-js";
import { render, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type {
  CommandProgramReceipt,
  CommandReceiptDocumentPage,
  CommandReceiptRow,
} from "../../api/types.ts";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { CardAction } from "../action/action-merge.ts";
import { useActionCoverage } from "../action/action-source-coverage.ts";
import { openCommandReceiptAccess } from "../transcript/content/command-receipt-access.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { clearCommandActivities } from "./command-activity-live.ts";
import { useCommandActivitySnapshots } from "./command-activity-snapshots.ts";

vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

type ProjectedRow = { index: number; text: string; revision: string };
type CompletenessVerdict = { ok: true } | { ok: false; reason: "duplicate_row" | "stale_page" | "missing_row"; detail: string };

/** Independent I16 oracle comparing raw receipt rows against projected UI rows. */
function checkProjectionCompleteness(
  rawRows: readonly CommandReceiptRow[],
  expectedRevision: string,
  projected: readonly ProjectedRow[],
): CompletenessVerdict {
  const seen = new Set<number>();
  for (const row of projected) {
    if (row.revision !== expectedRevision) {
      return { ok: false, reason: "stale_page", detail: `row ${row.index} carried ${row.revision} instead of ${expectedRevision}` };
    }
    if (seen.has(row.index)) {
      return { ok: false, reason: "duplicate_row", detail: `row ${row.index} appeared more than once` };
    }
    seen.add(row.index);
  }
  for (const raw of rawRows) {
    if (!seen.has(raw.index)) {
      return { ok: false, reason: "missing_row", detail: `raw row ${raw.index} absent from projection` };
    }
    const matched = projected.find((row) => row.index === raw.index);
    if (matched?.text !== `${raw.text}\n`) {
      return { ok: false, reason: "missing_row", detail: `row ${raw.index} content diverged from raw receipt` };
    }
  }
  return { ok: true };
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

function activityFact(id: string, state: CommandProgramReceipt["state"]) {
  return {
    id,
    label: "Run command",
    subject: id,
    group: "Running programs",
    state,
    outcome: state === "completed" ? "Finished" : state,
    execution_ids: [id],
    spans: [],
    steps: [],
  };
}

function actionWithWindows(windows: string[]): CardAction {
  return {
    label: "Run command",
    status: "done",
    ran: { text: "ls", noun: "command" },
    facts: [{ kind: "source_observation", source_windows: windows }],
    row: [],
    links: [{ kind: "input" }],
  };
}

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  vi.clearAllMocks();
  clearCommandActivities("sess-i16");
});

describe("I16 · projection preserves completeness", () => {
  it("negative controls: completeness oracle rejects duplicate row, stale page, and missing row", () => {
    const raw: CommandReceiptRow[] = [
      { index: 0, text: '{"type":"program","id":"p1"}' },
      { index: 1, text: '{"type":"execution","id":"e1"}' },
      { index: 2, text: '{"type":"node","id":"n1"}' },
    ];
    const valid: ProjectedRow[] = raw.map((r) => ({ index: r.index, text: `${r.text}\n`, revision: "rev-2" }));
    expect(checkProjectionCompleteness(raw, "rev-2", valid)).toEqual({ ok: true });

    const duplicate = [...valid, valid[1]!];
    expect(checkProjectionCompleteness(raw, "rev-2", duplicate)).toMatchObject({ ok: false, reason: "duplicate_row" });

    const stale = [valid[0]!, { ...valid[1]!, revision: "rev-1" }, valid[2]!];
    expect(checkProjectionCompleteness(raw, "rev-2", stale)).toMatchObject({ ok: false, reason: "stale_page" });

    const missing = [valid[0]!, valid[2]!];
    expect(checkProjectionCompleteness(raw, "rev-2", missing)).toMatchObject({ ok: false, reason: "missing_row" });
  });

  it("snapshots preserve exact IDs across live→changed-revision→settled pagination, reconnect, and host failure without fabricating output", async () => {
    const progALive = { id: "prog-a", state: "running", activity_revision: 1 } as CommandProgramReceipt;
    const progASettled = { id: "prog-a", state: "completed", activity_revision: 2 } as CommandProgramReceipt;
    const progBSettled = { id: "prog-b", state: "completed", activity_revision: 3 } as CommandProgramReceipt;

    const get1 = vi.fn(async (_session: string, programId: string) => {
      if (programId === "prog-a") {
        const current = get1.mock.calls.filter((c) => c[1] === "prog-a").length === 1 ? progALive : progASettled;
        return { program: current, activities: [activityFact(`${programId}-r${current.activity_revision}`, current.state)], executions: [], nodes: [] };
      }
      return { program: progBSettled, activities: [activityFact("prog-b-r3", "completed")], executions: [], nodes: [] };
    });
    const client1 = stubClient({ getCommandProgramReceipt: get1 });
    vi.mocked(getLycaonClient).mockReturnValue(client1);

    const [entries, setEntries] = createSignal<readonly ActivitySpanEntry[]>([toolEntry(progALive, "call-a")]);
    const view = render(() => {
      const snapshot = useCommandActivitySnapshots(entries, () => "sess-i16");
      const summary = () => entries().map((entry) => {
        if (entry.kind !== "tool") return "";
        const resolved = snapshot(entry.part);
        return `${entry.part.commandProgram?.id}:${resolved?.activities?.[0]?.id ?? "pending"}`;
      }).join(",");
      return <span>{summary()}</span>;
    });

    await waitFor(() => expect(view.container.textContent).toBe("prog-a:prog-a-r1"));

    // Changed revision on same ID triggers fresh fetch.
    setEntries([toolEntry(progASettled, "call-a")]);
    await waitFor(() => expect(view.container.textContent).toBe("prog-a:prog-a-r2"));
    expect(get1).toHaveBeenCalledTimes(2);

    // Overlapping page append preserves settled prog-a from cache without re-fetching it.
    setEntries([toolEntry(progASettled, "call-a"), toolEntry(progBSettled, "call-b")]);
    await waitFor(() => expect(view.container.textContent).toBe("prog-a:prog-a-r2,prog-b:prog-b-r3"));
    expect(get1).toHaveBeenCalledTimes(3);
    view.unmount();

    // Reconnect (new LycaonClient instance) re-establishes fresh settled state cleanly.
    const get2 = vi.fn(async (_session: string, programId: string) => ({
      program: programId === "prog-a" ? progASettled : progBSettled,
      activities: [activityFact(`${programId}-reconnected`, "completed")],
      executions: [],
      nodes: [],
    }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get2 }));
    const reconnected = render(() => {
      const snapshot = useCommandActivitySnapshots(() => [toolEntry(progASettled, "call-a")], () => "sess-i16");
      return <span>{snapshot(toolEntry(progASettled, "call-a").part)?.activities?.[0]?.id ?? "pending"}</span>;
    });
    await waitFor(() => expect(reconnected.container.textContent).toBe("prog-a-reconnected"));
    expect(get2).toHaveBeenCalledTimes(1);
    reconnected.unmount();

    // Host unavailable error must never fabricate success or synthetic activities.
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      getCommandProgramReceipt: vi.fn().mockRejectedValue(new LycaonApiError("unavailable", 503, "internal_error")),
    }));
    const failed = render(() => {
      const snapshot = useCommandActivitySnapshots(() => [toolEntry(progBSettled, "call-err")], () => "sess-i16");
      const resolved = () => snapshot(toolEntry(progBSettled, "call-err").part);
      return <span>{resolved()?.activities === undefined ? "no-fabricated-output" : "unexpected"}</span>;
    });
    await waitFor(() => expect(failed.container.textContent).toBe("no-fabricated-output"));
    failed.unmount();
  });

  it("receipt document access preserves cursor progression, resets on changed revision, and honors abort", async () => {
    const rev1Rows: CommandReceiptRow[] = Array.from({ length: 70 }, (_, i) => ({
      index: i,
      text: JSON.stringify({ type: "execution", rev: 1, index: i }),
    }));
    const rev2Rows: CommandReceiptRow[] = Array.from({ length: 70 }, (_, i) => ({
      index: i,
      text: JSON.stringify({ type: "execution", rev: 2, index: i }),
    }));
    let serverRevision = "rev-1";
    const observedWindows: Array<{ offset: number; revision?: string }> = [];

    const getDoc = vi.fn(async (
      _sessionId: string,
      _scopes: readonly string[],
      window: { offset: number; limit: number; revision?: string },
    ): Promise<CommandReceiptDocumentPage> => {
      observedWindows.push({ offset: window.offset, revision: window.revision });
      if (window.revision && window.revision !== serverRevision) {
        throw new LycaonApiError("changed", 409, "chat_content_changed");
      }
      const source = serverRevision === "rev-1" ? rev1Rows : rev2Rows;
      const slice = source.slice(window.offset, window.offset + window.limit);
      return {
        revision: serverRevision,
        total_rows: source.length,
        settled: serverRevision === "rev-2",
        offset: window.offset,
        end_offset: window.offset + slice.length,
        complete: window.offset + slice.length >= source.length,
        rows: slice,
      };
    });

    const controller = new AbortController();
    const access = await createRoot(async () => openCommandReceiptAccess({
      client: stubClient({ getCommandReceiptDocument: getDoc }),
      sessionId: "sess-i16",
      scopes: [{ programId: "prog-doc" }],
      signal: controller.signal,
    }));
    expect(access.revision()).toBe("rev-1");
    expect(access.settled()).toBe(false);

    // Advance revision on the host before reading page 2 (offset 64): pinned read gets 409 and resets unpinned.
    serverRevision = "rev-2";
    const page2 = await access.read(64, controller.signal);
    expect(access.revision()).toBe("rev-2");
    expect(access.settled()).toBe(true);
    expect(observedWindows).toEqual([
      { offset: 0, revision: undefined },
      { offset: 64, revision: "rev-1" },
      { offset: 64, revision: undefined },
    ]);

    // Read page 1 under the adopted rev-2 and verify full raw→projected completeness with the oracle.
    const page1 = await access.read(0, controller.signal);
    const projected: ProjectedRow[] = [...page1, ...page2].map((r) => ({
      index: r.index,
      text: r.text,
      revision: access.revision(),
    }));
    expect(checkProjectionCompleteness(rev2Rows, "rev-2", projected)).toEqual({ ok: true });

    // Abort signal stops further reads cleanly.
    controller.abort();
    await expect(access.read(0, controller.signal)).rejects.toThrow(/Canceled|Abort/);
  });

  it("bounds requests after terminal 404 source_history_not_found across repeated invalidations while settling live windows", async () => {
    let liveCalls = 0;
    let prunedCalls = 0;
    const getObs = vi.fn(async (_projectId: string, _sessionId: string, windowId: string) => {
      if (windowId === "win-expired") {
        prunedCalls += 1;
        throw new LycaonApiError("pruned", 404, "source_history_not_found");
      }
      liveCalls += 1;
      if (liveCalls === 1) return { settling: true, coverage: [] };
      return {
        settling: false,
        coverage: [{ folder: "/srv/shared", coverage: "not_observed" as const, reason: "time_budget" as const }],
      };
    });

    const client = stubClient({ getProjectSourceCommandObservation: getObs });
    let resolved!: () => CardAction;
    const dispose = createRoot((d) => {
      resolved = useActionCoverage(() => actionWithWindows(["win-live", "win-expired"]), {
        client,
        projectId: "proj-i16",
        sessionId: "sess-i16",
      });
      return d;
    });

    try {
      await vi.waitFor(() => expect(liveCalls).toBe(1));
      await vi.waitFor(() => expect(prunedCalls).toBe(1));
      await flush();

      invalidateSourceQueries({ projectId: "proj-i16" });
      await vi.waitFor(() => expect(liveCalls).toBe(2));
      await flush();

      invalidateSourceQueries({ projectId: "proj-i16" });
      invalidateSourceQueries({ projectId: "proj-i16" });
      await flush();

      expect(prunedCalls).toBe(1);
      expect(liveCalls).toBe(2);
      expect(resolved().facts.find((f) => f.kind === "source_observation")?.source_coverage).toEqual([
        { folder: "/srv/shared", coverage: "not_observed", reason: "time_budget" },
      ]);
    } finally {
      dispose();
    }
  });
});
