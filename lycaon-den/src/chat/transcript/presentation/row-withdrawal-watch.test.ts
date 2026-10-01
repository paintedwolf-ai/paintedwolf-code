import { describe, expect, it } from "vitest";
import {
  visibleRowContinuity,
  withdrawnRows,
  type RowWithdrawal,
} from "./row-withdrawal-watch.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";

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
