import { describe, expect, it } from "vitest";
import {
  applyHunkReject,
  changeHunkAtAfterLine,
  computeDiffHunks,
} from "./line-diff.ts";

describe("line-diff", () => {
  it("rejects an early hunk and leaves later edits intact", () => {
    const before = "a\nb\nc\n";
    const after = "A\nb\nC\n";
    const hunks = computeDiffHunks(before, after).filter((h) => h.kind === "change");
    expect(hunks.length).toBeGreaterThanOrEqual(1);
    const next = applyHunkReject(after, before, hunks[0]!);
    expect(next.startsWith("a\n")).toBe(true);
    expect(next).toContain("C");
  });

  it("preserves the absence of a final newline when rejecting a hunk", () => {
    const before = "one\ntwo";
    const after = "one\nTWO";
    const hunk = computeDiffHunks(before, after).find((row) => row.kind === "change")!;

    expect(applyHunkReject(after, before, hunk)).toBe(before);
  });

  it("preserves a final newline when rejecting a hunk", () => {
    const before = "one\ntwo\n";
    const after = "one\nTWO\n";
    const hunk = computeDiffHunks(before, after).find((row) => row.kind === "change")!;

    expect(applyHunkReject(after, before, hunk)).toBe(before);
  });

  it.each([
    ["one", "one\n"],
    ["one\n", "one"],
    ["one\ntwo", "one"],
    ["one", "one\ntwo"],
  ])("restores exact EOF structure for %j → %j", (before, after) => {
    const hunk = computeDiffHunks(before, after).find((row) => row.kind === "change")!;
    expect(applyHunkReject(after, before, hunk)).toBe(before);
  });

  it("bounds diff work for large unrelated files", () => {
    const before = Array.from({ length: 10_000 }, (_, i) => `before ${i}`).join("\n");
    const after = Array.from({ length: 10_000 }, (_, i) => `after ${i}`).join("\n");

    expect(computeDiffHunks(before, after).some((row) => row.kind === "change")).toBe(true);
  });

  it("finds a change hunk by after line, including a pure deletion", () => {
    const hunks = computeDiffHunks("a\ngone\nc\n", "a\nc\n").filter(
      (h) => h.kind === "change",
    );
    expect(changeHunkAtAfterLine(hunks, 1)?.kind).toBe("change");
    expect(changeHunkAtAfterLine(hunks, 0)).toBeNull();
  });

  it("does not treat a CRLF/LF line-ending change as a content change", () => {
    const before = "line one\r\nline two\r\nline three\r\n";
    const after = "line one\nline two\nline three\n";
    expect(computeDiffHunks(before, after).some((h) => h.kind === "change")).toBe(false);
  });

  it("still finds a real content change alongside a CRLF/LF difference", () => {
    const before = "line one\r\nline two\r\nline three\r\n";
    const after = "line one\nCHANGED\nline three\n";
    const hunks = computeDiffHunks(before, after).filter((h) => h.kind === "change");
    expect(hunks).toEqual([
      { kind: "change", beforeStart: 1, beforeEnd: 2, afterStart: 1, afterEnd: 2 },
    ]);
  });
});
