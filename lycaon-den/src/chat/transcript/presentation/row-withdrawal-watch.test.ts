import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  noteActivitySpanRowWithdrawals,
  visibleActivitySpanRows,
  visibleRowContinuity,
  withdrawnRows,
  type RowWithdrawal,
} from "./row-withdrawal-watch.ts";
import { denScrollDebugLog, isStreamScrollDebugEnabled } from "../../stream/den-scroll-debug.ts";
import { foldActionEntries } from "../../action/part-action.ts";
import { activityDecision, activityFact, activityTool } from "../../../test/command-activity-fixtures.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";

vi.mock("../../stream/den-scroll-debug.ts", async importOriginal => ({
  ...await importOriginal<typeof import("../../stream/den-scroll-debug.ts")>(),
  isStreamScrollDebugEnabled: vi.fn(),
  denScrollDebugLog: vi.fn(),
}));

function draftRow(id: string): TranscriptItem {
  return { kind: "draft", key: id, text: "", live: true } as TranscriptItem;
}

function toolRow(assistantId: string, callId: string): TranscriptItem {
  return {
    kind: "tool",
    key: `${assistantId}:${callId}`,
    part: { id: `${assistantId}:${callId}` },
  } as unknown as TranscriptItem;
}

function checkpointRow(id: string, messageId: string): TranscriptItem {
  return {
    kind: "checkpoint",
    key: `checkpoint:${id}`,
    parentMessageId: messageId,
  } as unknown as TranscriptItem;
}

const continuity = (items: TranscriptItem[]) => visibleRowContinuity(items);
const retained = (...ids: string[]) => new Set(ids);

describe("transcript row withdrawal", () => {
  it("treats a rail replaced by its chicklets as a replacement", () => {
    const before = continuity([draftRow("m1")]);
    const after = continuity([toolRow("m1", "call_1")]);
    expect(withdrawnRows(before, after, retained("m1"))).toEqual(
      [] as RowWithdrawal[],
    );
  });

  it("reports a step that lost its last row", () => {
    const before = continuity([draftRow("m1"), toolRow("m2", "call_1")]);
    const after = continuity([toolRow("m2", "call_1")]);
    expect(withdrawnRows(before, after, retained("m1", "m2"))).toEqual([
      { key: "m1", kind: "draft" },
    ]);
  });

  it("does not let one approval card cover another disappearing", () => {
    const before = continuity([
      checkpointRow("c1", "m1"),
      checkpointRow("c2", "m1"),
    ]);
    const after = continuity([checkpointRow("c2", "m1")]);
    expect(
      withdrawnRows(before, after, retained("checkpoint:c1", "checkpoint:c2")),
    ).toEqual([{ key: "checkpoint:c1", kind: "checkpoint" }]);
  });

  it("reports a settled checkpoint card disappearing", () => {
    const before = continuity([checkpointRow("c1", "m1")]);
    const after = continuity([]);
    expect(withdrawnRows(before, after, retained("checkpoint:c1"))).toEqual([
      { key: "checkpoint:c1", kind: "checkpoint" },
    ]);
  });

  it("does not report rows whose message left the window", () => {
    // Paging out history and switching sessions drop ranges.
    const before = continuity([draftRow("m1"), toolRow("m2", "call_1")]);
    const after = continuity([toolRow("m2", "call_1")]);
    expect(withdrawnRows(before, after, retained("m2"))).toEqual([]);
  });

  it("flattens spans so regrouping is not a withdrawal", () => {
    const span = {
      kind: "activity_span",
      key: "m1",
      entries: [toolRow("m1", "call_1"), toolRow("m2", "call_2")],
    } as unknown as TranscriptItem;
    const before = continuity([span]);
    const after = continuity([
      toolRow("m1", "call_1"),
      toolRow("m2", "call_2"),
    ]);
    expect(withdrawnRows(before, after, retained("m1", "m2"))).toEqual([]);
  });
});

describe("activity span row withdrawal", () => {
  beforeEach(() => {
    vi.mocked(isStreamScrollDebugEnabled).mockReturnValue(false);
    noteActivitySpanRowWithdrawals("span", [], []);
    vi.mocked(isStreamScrollDebugEnabled).mockReturnValue(true);
    vi.mocked(denScrollDebugLog).mockClear();
  });

  it("logs a missing child row while its source stays in the span exactly once", () => {
    const source = activityTool("assistant:call", "command");
    const fact = activityFact("late");
    const key = `${source.part.id}:activity:${fact.id}`;
    const child = {
      ...source, part: {
        ...source.part, id: key,
        commandActivity: { fact, members: [fact], calls: [source.part], receiptScopes: [] },
      },
    };
    expect(visibleActivitySpanRows([source, child]).get(key)).toEqual({ kind: "tool", source: source.part.id });
    expect(noteActivitySpanRowWithdrawals("span", [source], [source, child])).toEqual([]);
    expect(noteActivitySpanRowWithdrawals("span", [source], [source])).toEqual([{ key, kind: "tool", source: source.part.id }]);
    expect(noteActivitySpanRowWithdrawals("span", [source], [source])).toEqual([]);
    expect(denScrollDebugLog).toHaveBeenCalledTimes(1);
    expect(denScrollDebugLog).toHaveBeenCalledWith("scroll", "row-withdrawn", expect.objectContaining({
      row_key: key, kind: "tool", source_key: source.part.id,
    }));
  });

  it("lets a row move to another span or leave the transcript", () => {
    const a = activityTool("assistant:a", "command");
    const b = activityDecision("decision");
    noteActivitySpanRowWithdrawals("span", [a, b], [a, b]);
    expect(noteActivitySpanRowWithdrawals("span", [a], [a])).toEqual([]);
    expect(noteActivitySpanRowWithdrawals("span", [b], [b])).toEqual([]);
    expect(noteActivitySpanRowWithdrawals("span", [], [])).toEqual([]);
    expect(denScrollDebugLog).not.toHaveBeenCalled();
  });

  it("reports rows absorbed by the action fold after first sight only", () => {
    const a = activityTool("assistant:a", "terminal_send");
    const b = activityTool("assistant:b", "terminal_send");
    const foldKey = { label: "Terminal", status: "done", ran: { text: "Read", noun: "command" }, facts: [], row: [], links: [], fold_key: "terminal" } as const;
    const folded = foldActionEntries([a, b].map(entry => ({
      ...entry, part: { ...entry.part, action: { ...foldKey, facts: [], row: [], links: [] } },
    })));
    expect(folded).toHaveLength(1);
    noteActivitySpanRowWithdrawals("visible-then-folded", [a, b], [a, b]);
    expect(noteActivitySpanRowWithdrawals("visible-then-folded", [a, b], folded)).toEqual([
      { key: b.part.id, kind: "tool", source: b.part.id },
    ]);
    expect(noteActivitySpanRowWithdrawals("folded-on-arrival", [a, b], folded)).toEqual([]);
    expect(noteActivitySpanRowWithdrawals("folded-on-arrival", [a, b], folded)).toEqual([]);
    expect(denScrollDebugLog).toHaveBeenCalledTimes(1);
  });
});
