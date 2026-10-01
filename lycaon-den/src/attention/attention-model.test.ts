import { describe, expect, it } from "vitest";
import type { AttentionRow } from "../api/types.ts";
import {
  ATTENTION_CLASS_LABEL,
  attentionAgeLabel,
  attentionFor,
  attentionRank,
  attentionReasonLabel,
  blockingRows,
  compareAttention,
  indexAttention,
  isBlocking,
  marksHiddenPane,
  withoutReadFinish,
} from "./attention-model.ts";

const T0 = Date.parse("2026-07-27T12:00:00Z");

function row(over: Partial<AttentionRow> = {}): AttentionRow {
  return {
    session_id: "s1",
    project_id: "p1",
    class: "needs_you",
    reason: "checkpoint",
    since_at: new Date(T0).toISOString(),
    ...over,
  };
}

describe("attention ranking", () => {
  it("puts a blocked session ahead of a failed or running one", () => {
    expect(attentionRank("needs_you")).toBeLessThan(attentionRank("error"));
    expect(attentionRank("error")).toBeLessThan(attentionRank("running"));
  });

  it("puts a finished session behind a failure and ahead of a running turn", () => {
    expect(attentionRank("error")).toBeLessThan(attentionRank("finished"));
    expect(attentionRank("finished")).toBeLessThan(attentionRank("running"));
  });

  it("ranks sessions the host never listed behind every listed class", () => {
    expect(attentionRank(undefined)).toBeGreaterThan(attentionRank("running"));
  });

  it("treats only needs_you as an obligation", () => {
    expect(isBlocking("needs_you")).toBe(true);
    expect(isBlocking("finished")).toBe(false);
    expect(isBlocking("running")).toBe(false);
    expect(isBlocking(undefined)).toBe(false);
  });

  it("marks a hidden pane for an obligation or an unread result only", () => {
    expect(marksHiddenPane("needs_you")).toBe(true);
    expect(marksHiddenPane("finished")).toBe(true);
    expect(marksHiddenPane("running")).toBe(false);
    expect(marksHiddenPane("error")).toBe(false);
    expect(marksHiddenPane(undefined)).toBe(false);
  });
});

describe("compareAttention", () => {
  it("sorts by class before age", () => {
    const blockedNew = row({ class: "needs_you", since_at: new Date(T0 + 60_000).toISOString() });
    const runningOld = row({ class: "running", since_at: new Date(T0).toISOString() });
    expect(compareAttention(blockedNew, runningOld)).toBeLessThan(0);
  });

  it("puts the longest-waiting first inside a class", () => {
    const older = row({ since_at: new Date(T0).toISOString() });
    const newer = row({ since_at: new Date(T0 + 60_000).toISOString() });
    expect(compareAttention(older, newer)).toBeLessThan(0);
  });

  it("sorts an unlisted session behind every listed one", () => {
    expect(compareAttention(undefined, row({ class: "running" }))).toBeGreaterThan(0);
  });

  it("orders a mixed list worst-first when used as a sort comparator", () => {
    const rows = [
      row({ session_id: "running", class: "running" }),
      row({ session_id: "done", class: "finished" }),
      row({ session_id: "failed", class: "error" }),
      row({ session_id: "blocked", class: "needs_you" }),
    ];
    const sorted = [...rows].sort(compareAttention).map((r) => r.session_id);
    expect(sorted).toEqual(["blocked", "failed", "done", "running"]);
  });
});

describe("indexing", () => {
  it("looks a session up by id", () => {
    const index = indexAttention([row({ session_id: "a" }), row({ session_id: "b" })]);
    expect(attentionFor(index, "b")?.session_id).toBe("b");
    expect(attentionFor(index, "missing")).toBeUndefined();
  });

  it("returns undefined for a missing index rather than throwing", () => {
    expect(attentionFor(undefined, "a")).toBeUndefined();
  });

  it("keeps only the rows that are asking something of the human", () => {
    const rows = [row({ class: "needs_you" }), row({ class: "running" })];
    expect(blockingRows(rows)).toHaveLength(1);
  });
});

describe("withoutReadFinish", () => {
  const index = (...rows: AttentionRow[]) => indexAttention(rows);

  it("drops the finished row for the conversation on screen", () => {
    const source = index(row({ session_id: "reading", class: "finished" }));
    expect(attentionFor(withoutReadFinish(source, "reading"), "reading")).toBeUndefined();
  });

  it("leaves other sessions' finished rows alone", () => {
    const source = index(
      row({ session_id: "reading", class: "finished" }),
      row({ session_id: "elsewhere", class: "finished" }),
    );
    const shown = withoutReadFinish(source, "reading");
    expect(attentionFor(shown, "elsewhere")?.class).toBe("finished");
  });

  it("still shows a question or a failure in the chat being read", () => {
    const asking = index(row({ session_id: "reading", class: "needs_you" }));
    expect(attentionFor(withoutReadFinish(asking, "reading"), "reading")?.class).toBe(
      "needs_you",
    );
    const failed = index(row({ session_id: "reading", class: "error" }));
    expect(attentionFor(withoutReadFinish(failed, "reading"), "reading")?.class).toBe(
      "error",
    );
  });

  it("returns the same index when nothing is readable or nothing is dropped", () => {
    const source = index(row({ session_id: "a", class: "finished" }));
    expect(withoutReadFinish(source, null)).toBe(source);
    expect(withoutReadFinish(source, "b")).toBe(source);
  });
});

describe("labels", () => {
  it("names each class in the second person", () => {
    expect(ATTENTION_CLASS_LABEL.needs_you).toBe("Needs you");
  });

  it("prefers the specific reason over the class name", () => {
    expect(attentionReasonLabel(row({ reason: "ask" }))).toBe("Waiting on your answer");
    expect(attentionReasonLabel(row({ reason: "checkpoint" }))).toBe("Waiting on approval");
  });

  it("renders a coarse age instead of a ticking clock", () => {
    expect(attentionAgeLabel(row(), T0 + 30_000)).toBe("just now");
    expect(attentionAgeLabel(row(), T0 + 12 * 60_000)).toBe("12m");
    expect(attentionAgeLabel(row(), T0 + 3 * 3_600_000)).toBe("3h");
    expect(attentionAgeLabel(row(), T0 + 50 * 3_600_000)).toBe("2d");
  });

  it("renders nothing for an unparseable timestamp", () => {
    expect(attentionAgeLabel(row({ since_at: "not-a-date" }), T0)).toBe("");
  });
});
