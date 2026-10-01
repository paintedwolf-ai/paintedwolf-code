import { describe, expect, it } from "vitest";
import type { SecurityFinding, SourceAttributionInterval } from "../../../api/types.ts";
import {
  attributionForLine,
  findingGlyph,
  findingsByLine,
  mergeAttributionIntervals,
} from "./knowing-gutter-model.ts";

function iv(
  partial: Partial<SourceAttributionInterval> &
    Pick<SourceAttributionInterval, "start_line" | "end_line">,
): SourceAttributionInterval {
  return {
    turn: 1,
    recorded_at: "2026-07-30T12:00:00Z",
    session_id: "s1",
    tool_call_id: "t1",
    ...partial,
  };
}

function finding(
  level: SecurityFinding["level"],
  path: string,
  line: number,
  message: string,
): SecurityFinding {
  return {
    rule_id: "r",
    level,
    message,
    locations: [{ uri: path, start_line: line }],
    fingerprints: { primary: `fp-${level}-${line}-${message}` },
    tool: { driver_id: "d", name: "scanner" },
  };
}

describe("mergeAttributionIntervals", () => {
  it("retains both chats where their text shares a line", () => {
    const marks = mergeAttributionIntervals([
      iv({ start_line: 1, end_line: 3 }),
      iv({ start_line: 2, end_line: 4, session_id: "s2" }),
      iv({ start_line: 2, end_line: 2 }),
    ]);
    expect(marks.map((mark) => [mark.startLine, mark.endLine, mark.contributors.map((author) => author.sessionId)]))
      .toEqual([[1, 1, ["s1"]], [2, 3, ["s1", "s2"]], [4, 4, ["s2"]]]);
    expect(attributionForLine(marks, 3)?.contributors).toHaveLength(2);
  });
  it("merges adjacent intervals with the same provenance", () => {
    const merged = mergeAttributionIntervals([
      iv({ start_line: 1, end_line: 2 }),
      iv({ start_line: 3, end_line: 4 }),
      iv({ start_line: 10, end_line: 10, session_id: "s2", tool_call_id: "t2" }),
    ]);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toMatchObject({
      startLine: 1,
      endLine: 4,
    });
    expect(merged[1]).toMatchObject({ startLine: 10, endLine: 10, contributors: [{ sessionId: "s2" }] });
  });
});

describe("attributionForLine", () => {
  const marks = mergeAttributionIntervals([
    iv({ start_line: 2, end_line: 4 }),
    iv({ start_line: 9, end_line: 9, tool_call_id: "t2" }),
  ]);

  it("finds the interval covering a line, at either edge", () => {
    expect(attributionForLine(marks, 2)).toMatchObject({ startLine: 2 });
    expect(attributionForLine(marks, 4)).toMatchObject({ startLine: 2 });
    expect(attributionForLine(marks, 9)).toMatchObject({ contributors: [{ toolCallId: "t2" }] });
  });

  it("is null between, before, and after the intervals", () => {
    expect(attributionForLine(marks, 1)).toBeNull();
    expect(attributionForLine(marks, 5)).toBeNull();
    expect(attributionForLine(marks, 99)).toBeNull();
    expect(attributionForLine([], 1)).toBeNull();
  });
});

describe("findingsByLine", () => {
  it("retains unscored findings and lets scored findings determine the glyph", () => {
    const unscored = finding("unknown", "src/a.go", 4, "unscored");
    expect(findingsByLine("src/a.go", [unscored])).toEqual([
      { line: 4, level: "unknown", findings: [unscored] },
    ]);
    const scored = finding("info", "src/a.go", 4, "scored");
    const marks = findingsByLine("src/a.go", [unscored, scored]);
    expect(marks[0]?.level).toBe("info");
    expect(marks[0]?.findings).toEqual([unscored, scored]);
  });

  it.each([
    "other/src/a.go", "prefix-src/a.go", "a.go", "/outside/src/a.go", "https://example.com/src/a.go",
  ])("does not attach %s to a file sharing its suffix", (uri) => {
    expect(findingsByLine("src/a.go", [finding("high", uri, 4, "other")])).toEqual([]);
  });

  it("highest severity wins the glyph and keeps all findings", () => {
    const marks = findingsByLine("src/a.go", [
      finding("low", "src/a.go", 4, "low"),
      finding("critical", "src/a.go", 4, "crit"),
      finding("high", "pkg/b.go", 4, "other"),
    ]);
    expect(marks).toHaveLength(1);
    expect(marks[0]!.level).toBe("critical");
    expect(marks[0]!.findings).toHaveLength(2);
    expect(findingGlyph("critical")).toBe("◆");
    expect(findingGlyph("high")).toBe("▲");
    expect(findingGlyph("medium")).toBe("●");
    expect(findingGlyph("low")).toBe("○");
    expect(findingGlyph("info")).toBe("◌");
  });
});
