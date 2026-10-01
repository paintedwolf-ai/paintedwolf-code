// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  applyFindHighlights,
  clearFindHighlights,
  findLiteralOffsets,
  FIND_MARK_ATTR,
  FIND_MARK_ACTIVE_CLASS,
  FIND_MARK_INDEX_ATTR,
  FIND_MAX_INACTIVE_PAINT,
  matchInRoot,
  setActiveFindMark,
} from "./find-match.ts";
import {
  registerFindRevealHost,
  resetFindRevealHostsForTests,
} from "./find-reveal.ts";

describe("findLiteralOffsets", () => {
  it("finds case-insensitive non-overlapping matches", () => {
    expect(findLiteralOffsets("Aaa a Aa", "a", false)).toEqual([
      { start: 0, length: 1 },
      { start: 1, length: 1 },
      { start: 2, length: 1 },
      { start: 4, length: 1 },
      { start: 6, length: 1 },
      { start: 7, length: 1 },
    ]);
  });

  it("respects case-sensitive mode", () => {
    expect(findLiteralOffsets("Foo foo FOO", "foo", true)).toEqual([
      { start: 4, length: 3 },
    ]);
  });

  it("returns empty for empty query", () => {
    expect(findLiteralOffsets("abc", "", false)).toEqual([]);
  });

  it("keeps offsets in the original UTF-16 corpus during case folding", () => {
    expect(findLiteralOffsets("İX", "x", false)).toEqual([
      { start: 1, length: 1 },
    ]);
    const root = document.createElement("div");
    root.textContent = "İX";
    document.body.appendChild(root);
    expect(matchInRoot(root, "x", false)[0]?.range.toString()).toBe("X");
    root.remove();
  });

  it("stops literal matching at the requested work cap", () => {
    expect(findLiteralOffsets("a ".repeat(20), "a", false, 5)).toHaveLength(5);
  });
});

describe("matchInRoot + highlights", () => {
  afterEach(() => {
    document.body.replaceChildren();
    resetFindRevealHostsForTests();
  });

  it("matches across text nodes in document order", () => {
    const root = document.createElement("div");
    root.innerHTML = "<p>hello </p><p>world</p>";
    document.body.appendChild(root);
    const matches = matchInRoot(root, "lo wo", false);
    expect(matches).toHaveLength(1);
    expect(matches[0]!.startOffset).toBe(3);
    expect(matches[0]!.length).toBe(5);
  });

  it("excludes hidden and aria-hidden text from the DOM corpus", () => {
    const root = document.createElement("div");
    root.innerHTML = [
      "<p>visible needle</p>",
      "<p hidden>hidden needle</p>",
      '<p aria-hidden="true">decorative needle</p>',
      '<p style="display: none">display needle</p>',
    ].join("");
    document.body.appendChild(root);

    expect(matchInRoot(root, "needle", false)).toHaveLength(1);
  });

  it("orders a collapsed synthetic corpus after its visible header", () => {
    const root = document.createElement("div");
    const card = document.createElement("article");
    const header = document.createElement("header");
    header.textContent = "first needle";
    card.appendChild(header);
    const following = document.createElement("p");
    following.textContent = "third needle";
    root.append(card, following);
    document.body.appendChild(root);
    registerFindRevealHost({
      id: "collapsed-card",
      hostEl: () => card,
      isCollapsed: () => true,
      revealForFind: () => () => {},
      collapsedCorpus: () => "second needle",
      collapsedCorpusAnchor: () => header,
    });

    const matches = matchInRoot(root, "needle", false);

    expect(matches).toHaveLength(3);
    expect(
      matches.map(
        (match) => `${match.range.toString()}:${match.revealHostId ?? "visible"}`,
      ),
    ).toEqual([
      "needle:collapsed-card",
      ":collapsed-card",
      "needle:visible",
    ]);
  });

  it("applies and clears overlay highlights with active class", () => {
    const root = document.createElement("div");
    root.textContent = "one two one";
    document.body.appendChild(root);
    const matches = matchInRoot(root, "one", false);
    expect(matches).toHaveLength(2);
    applyFindHighlights(root, matches, 1);
    const marks = root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`);
    expect(marks.length).toBeGreaterThanOrEqual(2);
    expect(
      [...marks].some((m) => m.classList.contains(FIND_MARK_ACTIVE_CLASS)),
    ).toBe(true);
    clearFindHighlights(root);
    expect(root.querySelectorAll(`[${FIND_MARK_ATTR}]`)).toHaveLength(0);
    expect(root.textContent).toBe("one two one");
  });

  it("setActiveFindMark swaps active without rebuilding marks", () => {
    const root = document.createElement("div");
    root.textContent = "one two one";
    document.body.appendChild(root);
    const matches = matchInRoot(root, "one", false);
    applyFindHighlights(root, matches, 0);
    const before = root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length;
    setActiveFindMark(root, 1);
    const after = root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length;
    expect(after).toBe(before);
    const active = root.querySelector(`.${FIND_MARK_ACTIVE_CLASS}`);
    expect(active?.getAttribute(FIND_MARK_INDEX_ATTR)).toBe("1");
  });

  it("caps inactive overlay marks when many matches share the viewport", () => {
    const root = document.createElement("div");
    root.textContent = "a ".repeat(FIND_MAX_INACTIVE_PAINT + 50);
    document.body.appendChild(root);
    const matches = matchInRoot(root, "a", false);
    expect(matches.length).toBeGreaterThan(FIND_MAX_INACTIVE_PAINT);
    applyFindHighlights(root, matches, 0);
    const marks = root.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`);
    // Paint the active match plus the capped inactive set.
    expect(marks.length).toBeLessThanOrEqual(FIND_MAX_INACTIVE_PAINT + 1);
    expect(
      [...marks].some((m) => m.classList.contains(FIND_MARK_ACTIVE_CLASS)),
    ).toBe(true);
  });

  it("matches without calling window.find", () => {
    const win = window as Window & { find?: (...args: unknown[]) => boolean };
    const spy =
      typeof win.find === "function" ? vi.spyOn(win, "find") : null;
    const root = document.createElement("div");
    root.textContent = "needle here";
    expect(matchInRoot(root, "needle", false)).toHaveLength(1);
    expect(spy?.mock.calls.length ?? 0).toBe(0);
    spy?.mockRestore();
  });

  it("bounds DOM ranges at the shared match-count cap", () => {
    const root = document.createElement("div");
    root.textContent = "x ".repeat(25);
    document.body.appendChild(root);
    expect(matchInRoot(root, "x", false, 10)).toHaveLength(10);
  });
});
