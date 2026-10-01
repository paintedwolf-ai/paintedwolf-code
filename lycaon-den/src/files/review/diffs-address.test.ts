import { describe, expect, it } from "vitest";
import {
  diffsAddressKey,
  diffsPageCopy,
  type DiffsLensState,
} from "./diffs-address.ts";

const TURN = { kind: "turn", sessionId: "s1", turn: 12, messageId: "s1-user-12" } as const;
const LENS = { kind: "lens" } as const;
const GIT = {
  kind: "git", rootId: "r1", spec: "a1b2c3d", label: "a1b2c3d Fix the loader",
  beforeCommit: "0".repeat(40), afterCommit: "a1b2c3d".padEnd(40, "0"),
} as const;

function lens(scope: DiffsLensState["scope"], comparisonOff = false): DiffsLensState {
  return { scope, comparisonOff };
}

describe("what a diffs page is reading", () => {
  it("keeps one page per project for the eye and one per turn otherwise", () => {
    expect(diffsAddressKey(LENS)).toBe("lens");
    expect(diffsAddressKey(TURN)).not.toBe(diffsAddressKey({ ...TURN, messageId: "other" }));
    // The same turn from two cards is one page.
    expect(diffsAddressKey({ ...TURN, turn: 12 })).toBe(diffsAddressKey(TURN));
  });

  it("names a lens page with the comparison's own words, so the panel and page agree", () => {
    expect(diffsPageCopy(LENS, lens({ kind: "new" }))).toMatchObject({
      title: "New since you looked", tab: "New since you looked",
    });
    expect(diffsPageCopy(LENS, lens({ kind: "commit" })).title).toBe("Changes since last commit");
    expect(diffsPageCopy(LENS, { ...lens({ kind: "session" }), subjectTitle: "Fix the loader" }).title)
      .toBe("Changes in “Fix the loader”");
    expect(diffsPageCopy(LENS, lens({ kind: "pin", pinId: "p", label: "before the refactor" })).tab)
      .toBe("before the refactor");
  });

  it("names a turn page apart from the eye's turn scope, because the ranges differ", () => {
    const turn = diffsPageCopy(TURN, lens({ kind: "turn" }));
    const eye = diffsPageCopy(LENS, lens({ kind: "turn" }));
    expect(turn.title).toBe("What this turn did");
    expect(eye.title).toBe("Changes in this turn");
    expect(turn.tab).toBe("All diffs · turn 12");
    expect(turn.note).toContain("A later turn's work never appears here.");
    expect(eye.note).toContain("as they are now");
  });

  it("says a lens page has nothing marked through the lens's own copy", () => {
    expect(diffsPageCopy(LENS, lens({ kind: "new" }, true)).title).toBe("Nothing marked");
  });

  it("keys a Git page on its root and resolved commits, not on what was typed", () => {
    expect(diffsAddressKey({ ...GIT, spec: "HEAD~2", label: "other words" })).toBe(diffsAddressKey(GIT));
    expect(diffsAddressKey({ ...GIT, rootId: "r2" })).not.toBe(diffsAddressKey(GIT));
    expect(diffsAddressKey({ ...GIT, beforeCommit: "" })).not.toBe(diffsAddressKey(GIT));
    expect(diffsAddressKey(GIT)).not.toBe(diffsAddressKey(TURN));
  });

  it("names a Git page with the host's label and states that it reads commits", () => {
    const copy = diffsPageCopy(GIT, lens({ kind: "new" }, true));
    expect(copy.title).toBe("a1b2c3d Fix the loader");
    expect(copy.tab).toBe("All diffs · a1b2c3d Fix the loader");
    expect(copy.note).toContain("From commit 0000000 to a1b2c3d");
    expect(diffsPageCopy({ ...GIT, beforeCommit: "" }, lens({ kind: "new" })).note).toContain("has no parent");
  });
});
