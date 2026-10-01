// @vitest-environment jsdom
import { EditorState, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { foldedRanges } from "@codemirror/language";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setLineFactsHandlers } from "./line-facts-gutter.ts";
import {
  focusedRestoreHunk,
  getLineGutterAttribution,
  getLineGutterFindings,
  lineGutterExtension,
  lineGutterNumbers,
  restoreHunkForFold,
  setLineGutterAttribution,
  setLineGutterCutHandlers,
  setLineGutterFindingHandlers,
  setLineGutterFindings,
  setLineGutterProvenanceHandlers,
  setLineGutterRestoreHandler,
} from "./line-gutter.ts";
import {
  applyEditorScopeDiff,
  editorScopeDiffFolds,
  openScopeDeletion,
  scopeDiffExtension,
  setScopeDeletedLines,
} from "../diff/scope-diff.ts";
import { createSourceEditorState } from "../editor/codemirror-theme.ts";
import {
  resetScrollActivityForTests,
  setupScrollActivity,
} from "../../../platform/scrolling/scroll-activity.ts";
import {
  findingGlyph,
  type AttributionMark,
  type FindingLineMark,
} from "./knowing-gutter-model.ts";

function mount(doc: string, original?: string, extra: Extension = []): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  const view = new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [lineGutterExtension, scopeDiffExtension, extra],
    }),
  });
  if (original != null) applyEditorScopeDiff(view, { original });
  return view;
}

function mountEditor(doc: string): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({ parent, state: createSourceEditorState({ surface: "files", doc }) });
}

function mark(
  line: number,
  level: FindingLineMark["level"],
  message = "finding",
): FindingLineMark {
  return {
    line,
    level,
    findings: [
      {
        rule_id: "r1",
        level,
        message,
        locations: [{ uri: "f.ts", start_line: line }],
        fingerprints: {},
        tool: { name: "scanner" },
      } as FindingLineMark["findings"][number],
    ],
  };
}

function attribution(over: Partial<AttributionMark> = {}): AttributionMark {
  return {
    startLine: 2,
    endLine: 3,
    contributors: [{ sessionId: "s", turn: 2, toolCallId: "tc", ts: "2026-07-30T00:00:00Z" }],
    ...over,
  };
}

const columns = (view: EditorView) =>
  view.dom.querySelectorAll(".cm-gutters .cm-gutter");
const bars = (view: EditorView) =>
  view.dom.querySelectorAll(".files-line-gutter__cell--add");
const deletedRows = (view: EditorView) =>
  view.dom.querySelectorAll(".files-line-gutter__cell--del");
const links = (view: EditorView) =>
  view.dom.querySelectorAll<HTMLButtonElement>(
    "[data-testid='files-line-gutter-provenance']",
  );
const glyphs = (view: EditorView) =>
  view.dom.querySelectorAll<HTMLButtonElement>(".files-line-gutter__finding");
const tints = (view: EditorView) =>
  view.dom.querySelectorAll<HTMLButtonElement>(
    ".files-line-gutter__finding-tint",
  );
const restores = (view: EditorView) =>
  view.dom.querySelectorAll<HTMLButtonElement>(
    "[data-testid='files-line-gutter-restore']",
  );
const folds = (view: EditorView) =>
  view.dom.querySelectorAll<HTMLButtonElement>(
    "[data-testid='files-line-gutter-fold']",
  );

describe("line gutter — one column", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("is a single column, and stays one as every mark arrives", () => {
    view = mount("a\nb\nc\n", "a\nx\nc\n");
    expect(columns(view)).toHaveLength(1);

    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    expect(columns(view)).toHaveLength(1);

    setLineGutterFindings(view, [
      mark(2, "critical", "boom"),
    ]);
    expect(columns(view)).toHaveLength(1);

    setLineGutterRestoreHandler(view, { onReject: () => undefined });
    expect(columns(view)).toHaveLength(1);
  });

  it("uses one gutter tab stop and arrow navigation across actions", async () => {
    view = mount("one\ntwo\nthree\n");
    const first = mark(1, "critical", "first");
    const second = mark(2, "high", "second");
    const third = mark(3, "medium", "third");
    setLineGutterFindings(view, [first, second, third]);
    await Promise.resolve();
    const controls = view.dom.querySelectorAll<HTMLButtonElement>(".files-line-gutter__facts");
    expect(view.dom.querySelectorAll('.files-line-gutter button[tabindex="0"]')).toHaveLength(1);
    expect(controls[0]!.closest('[aria-hidden="true"]')).toBeNull();
    expect([...controls].map((control) => control.tabIndex)).toEqual([0, -1, -1]);

    controls[0]!.focus();
    controls[0]!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
    );
    expect(document.activeElement).toBe(controls[1]);
    expect([...controls].map((control) => control.tabIndex)).toEqual([-1, 0, -1]);

    setLineGutterFindings(view, [first, third]);
    await Promise.resolve();
    const remaining = view.dom.querySelectorAll<HTMLButtonElement>(".files-line-gutter__facts");
    expect([...remaining].map((control) => control.tabIndex)).toEqual([0, -1]);
    view.dispatch({ selection: { anchor: view.state.doc.line(3).from } });
    await Promise.resolve();
    expect([...remaining].map((control) => control.tabIndex)).toEqual([-1, 0]);
  });

  it("coalesces control installation and leaves unchanged tab stops alone", async () => {
    view = mount("one\ntwo\nthree\n");
    await Promise.resolve();
    const queries = vi.spyOn(view.dom, "querySelectorAll");
    setLineGutterFindings(view, [mark(1, "critical", "one"), mark(2, "high", "two"), mark(3, "medium", "three")]);
    await Promise.resolve();
    await Promise.resolve();
    // Installation and the gutter mutation share one update.
    expect(queries.mock.calls.filter(([selector]) => selector === "[data-line-gutter-control]")).toHaveLength(1);
    const controls = [...view.dom.querySelectorAll<HTMLButtonElement>(".files-line-gutter__facts")];
    const mutations: MutationRecord[] = [];
    const observer = new MutationObserver((records) => mutations.push(...records));
    observer.observe(view.dom, { subtree: true, attributes: true, attributeFilter: ["tabindex", "aria-hidden"] });
    controls[0]!.dispatchEvent(new FocusEvent("focus"));
    controls[0]!.dispatchEvent(new FocusEvent("focus"));
    await Promise.resolve();
    expect(mutations).toEqual([]);
    observer.disconnect();
    queries.mockRestore();
  });

  it("reserves a stable minimum width while allowing longer source labels", () => {
    view = mount("a\n");
    const css = [...document.querySelectorAll("style")]
      .map((el) => el.textContent ?? "")
      .join("\n");
    // Unparseable theme rules are silently omitted.
    expect(css).toContain(
      ".files-line-gutter {min-width: calc(5ch + 18px); background: transparent",
    );
    expect(css).toContain(
      ".files-line-gutter .cm-gutterElement {position: relative",
    );
    expect(css).toMatch(
      /\.files-line-gutter__cell--add::before, \S+ \.files-line-gutter__cell--del::before \{content: ""; position: absolute;.*background: var\(--den-diff-add-mark\)/,
    );
    expect(css).toContain(
      ".files-line-gutter__cell--del::before {background: var(--den-diff-delete-mark)",
    );
    // Both selectors retain their scope prefix after comma expansion.
    expect(css).toMatch(
      /\.files-line-gutter__cell--linked:hover::before, \S+ \.files-line-gutter__cell--linked:focus-within::before \{right: 1px; width: 5px/,
    );
    // Content scrolling under a resting pointer does not light up the rows it passes.
    expect(css).toMatch(
      /\S+ \.cm-scroller:not\(\[data-den-scrolling\]\) \.files-line-gutter__number--foldable:hover \{/,
    );
  });

  it("gives each zone its own pixels — nothing overlaps the digits", () => {
    view = mount("a\n");
    const css = [...document.querySelectorAll("style")]
      .map((el) => el.textContent ?? "")
      .join("\n");
    // Cell padding separates the line number from the trailing diff mark.
    expect(css).toMatch(/\.files-line-gutter__finding \{[^}]*position: absolute; left: 2px/);
    expect(css).toMatch(/\.files-line-gutter__restore \{position: absolute; left: 2px/);
    expect(css).toMatch(
      /\.files-line-gutter__provenance \{[^}]*position: absolute; top: 0; bottom: 0; right: 0; width: 6px/,
    );
    expect(css).toContain("padding-right: 14px");
  });

  it("severity reaches the paint, not just the shape", () => {
    view = mount("a\n");
    const css = [...document.querySelectorAll("style")]
      .map((el) => el.textContent ?? "")
      .join("\n");
    for (const level of ["critical", "high", "medium", "low", "info"]) {
      expect(css).toContain(`color: var(--den-finding-${level})`);
      expect(css).toContain(`background: var(--den-finding-${level}-wash)`);
    }
  });
});

describe("line gutter — diff and provenance", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("marks the lines the comparison changed, and nothing without one", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    expect(bars(view).length).toBeGreaterThan(0);
    view.destroy();
    view = mount("a\nx\nc\n");
    expect(bars(view)).toHaveLength(0);
  });

  it("marks every line of a whole-file addition", () => {
    view = mount("a\nb\nc\n", "");
    expect(bars(view)).toHaveLength(3);
  });

  it("a pure insertion has no deleted row to mark", () => {
    view = mount("a\nb\n", "a\n");
    expect(deletedRows(view)).toHaveLength(0);
  });

  it("attribution paints only where the comparison already marks", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    setLineGutterAttribution(view, [attribution({ startLine: 1, endLine: 3 })]);
    expect(links(view).length).toBe(bars(view).length);
  });

  it("attribution with no comparison paints nothing at all", () => {
    view = mount("a\nb\nc\n");
    setLineGutterAttribution(view, [attribution()]);
    expect(links(view)).toHaveLength(0);
    expect(bars(view)).toHaveLength(0);
  });

  it("shows provenance on focus with the line under the bar, and opens on click", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    const onShow = vi.fn();
    const onActivate = vi.fn();
    setLineGutterProvenanceHandlers(view, {
      onActivate,
    });
    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    const link = links(view)[0]!;
    setLineFactsHandlers(view, { show: onShow, leave: vi.fn(), dismiss: vi.fn(), refresh: vi.fn() });
    link.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    expect(onShow).toHaveBeenCalledWith(
      2,
      link.closest(".cm-gutterElement"),
      "focus",
    );
    link.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(onActivate).toHaveBeenCalledWith(expect.objectContaining({ contributors: [expect.objectContaining({ turn: 2 })] }), link, 2);
  });

  it("activates provenance on Enter and Space, and ignores other keys", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    const onActivate = vi.fn();
    setLineGutterProvenanceHandlers(view, {
      onActivate,
    });
    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    const link = links(view)[0]!;
    for (const key of ["Enter", " ", "a", "Escape"]) {
      link.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    }
    expect(onActivate).toHaveBeenCalledTimes(2);
  });

  it("carries attribution through typing above it", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    view.dispatch({ changes: { from: 0, insert: "new\n" }, userEvent: "input.type" });
    expect(getLineGutterAttribution(view.state)).toEqual([
      expect.objectContaining({ startLine: 3, endLine: 3 }),
    ]);
  });

  it("drops attribution whose lines were removed", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    view.dispatch({ changes: { from: 2, to: 4 }, userEvent: "delete" });
    expect(getLineGutterAttribution(view.state)).toEqual([]);
  });

  it("clearing attribution keeps the bar and drops the link (honesty silence)", () => {
    view = mount("a\nx\nc\n", "a\nb\nc\n");
    setLineGutterAttribution(view, [attribution({ startLine: 2, endLine: 2 })]);
    expect(links(view).length).toBeGreaterThan(0);
    setLineGutterAttribution(view, []);
    expect(links(view)).toHaveLength(0);
    expect(bars(view).length).toBeGreaterThan(0);
  });
});

describe("line gutter — findings", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("maps existing marks across a document edit", () => {
    view = mount("a\nb\nc\n");
    setLineGutterFindings(view, [
      mark(2, "high", "m"),
    ]);
    expect(getLineGutterFindings(view.state)).toHaveLength(1);
    view.dispatch({ changes: { from: 0, insert: "zz\n" } });
    expect(getLineGutterFindings(view.state)).toHaveLength(1);
  });

  it("is shape-coded and aria-labelled", () => {
    view = mount("x\ny\n");
    setLineGutterFindings(view, [
      mark(1, "critical", "bad"),
    ]);
    const glyph = glyphs(view)[0]!;
    expect(glyph.textContent).toBe(findingGlyph("critical"));
    expect(glyph.dataset.level).toBe("critical");
    expect(glyph.getAttribute("aria-label")).toContain("Critical");
  });

  it("activates on Enter and Space, and ignores other keys", () => {
    view = mount("x\ny\n");
    const onActivate = vi.fn();
    setLineGutterFindingHandlers(view, {
      onActivate,
    });
    setLineGutterFindings(view, [
      mark(1, "medium", "m"),
    ]);
    const glyph = glyphs(view)[0]!;
    for (const key of ["Enter", " ", "x", "Tab"]) {
      glyph.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    }
    expect(onActivate).toHaveBeenCalledTimes(2);
  });

  it("falls back to the wash when the numerals crowd the glyph out", () => {
    // Four digits leaves under the 17px a glyph needs beside them.
    view = mount(`${"x\n".repeat(1200)}`);
    setLineGutterFindings(view, [
      mark(1, "critical", "bad"),
    ]);
    expect(glyphs(view)).toHaveLength(0);
    const tint = tints(view)[0]!;
    expect(tint).toBeTruthy();
    // The compact marker retains keyboard access.
    expect(tint.getAttribute("aria-label")).toContain("Critical");
    expect(tint.hasAttribute("data-line-gutter-control")).toBe(true);
  });
});

describe("line gutter — fold on the number", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("makes the number the control on a foldable line, and folds on click", () => {
    view = mountEditor("function f() {\n  return 1\n}\n");
    const control = folds(view)[0];
    expect(control).toBeTruthy();
    expect(control!.getAttribute("aria-expanded")).toBe("true");
    control!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    let folded = 0;
    foldedRanges(view.state).between(0, view.state.doc.length, () => {
      folded += 1;
      return undefined;
    });
    expect(folded).toBe(1);
    expect(folds(view)[0]!.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps keyboard focus on the line's control when Enter folds and unfolds it", () => {
    view = mountEditor("function f() {\n  return 1\n}\n");
    folds(view)[0]!.focus();
    for (const expanded of ["false", "true"]) {
      (document.activeElement as HTMLElement).dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
      const control = folds(view)[0]!;
      expect(control.getAttribute("aria-expanded")).toBe(expanded);
      expect(document.activeElement).toBe(control);
      expect(control.tabIndex).toBe(0);
    }
  });

  it("leaves a non-foldable line as plain digits", () => {
    view = mountEditor("const a = 1\n");
    expect(folds(view)).toHaveLength(0);
    expect(
      view.dom.querySelectorAll(".files-line-gutter__number"),
    ).not.toHaveLength(0);
  });
});

describe("line gutter — restore", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("offers nothing until a comparison is painted", () => {
    view = mountEditor("a\nb\n");
    setLineGutterRestoreHandler(view, { onReject: () => undefined });
    expect(restores(view)).toHaveLength(0);
    expect(focusedRestoreHunk(view)).toBeNull();
  });

  it("names only the change hunk under the caret", () => {
    view = mountEditor("a\nz\nc\n");
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    view.dispatch({ selection: { anchor: view.state.doc.line(2).from } });
    expect(focusedRestoreHunk(view)).not.toBeNull();
  });

  it("puts the chip on the hovered hunk regardless of caret position", () => {
    view = mountEditor("a\nz\nc\n");
    const onReject = vi.fn();
    setLineGutterRestoreHandler(view, { onReject });
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    view.dispatch({ selection: { anchor: view.state.doc.line(3).from } });
    const coordinates = vi.spyOn(view, "posAtCoords");
    view.contentDOM.querySelectorAll(".cm-line")[1]!.dispatchEvent(
      new MouseEvent("mousemove", { bubbles: true }),
    );
    expect(coordinates).not.toHaveBeenCalled();
    const chip = restores(view)[0]!;
    expect(chip).toBeTruthy();
    const pointerDown = new MouseEvent("pointerdown", {
      bubbles: true,
      cancelable: true,
    });
    expect(chip.dispatchEvent(pointerDown)).toBe(false);
    chip.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(onReject).toHaveBeenCalledTimes(1);
  });

  it("removes the chip when the pointer leaves the changed hunk", () => {
    view = mountEditor("a\nz\nc\n");
    setLineGutterRestoreHandler(view, { onReject: vi.fn() });
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    view.contentDOM.querySelectorAll(".cm-line")[1]!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    expect(restores(view)).toHaveLength(1);

    view.contentDOM.querySelectorAll(".cm-line")[2]!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    expect(restores(view)).toHaveLength(0);
  });

  it("holds the chip while hunks scroll under a resting pointer, then follows the pointer", () => {
    setupScrollActivity();
    try {
      view = mountEditor("a\nz\nc\n");
      setLineGutterRestoreHandler(view, { onReject: vi.fn() });
      applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
      const changed = view.contentDOM.querySelectorAll(".cm-line")[1]!;
      view.scrollDOM.dispatchEvent(new Event("scroll"));
      changed.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
      expect(restores(view)).toHaveLength(0);

      window.dispatchEvent(new MouseEvent("pointermove", { clientX: 5, clientY: 5 }));
      Object.defineProperty(document, "elementFromPoint", { configurable: true, value: () => changed });
      view.scrollDOM.dispatchEvent(new Event("scrollend"));
      expect(restores(view)).toHaveLength(1);
    } finally {
      Reflect.deleteProperty(document, "elementFromPoint");
      resetScrollActivityForTests();
    }
  });

  it("keeps restore reachable through gutter cells when line numbers are hidden", () => {
    view = mount("a\nz\nc\n", "a\nb\nc\n", lineGutterNumbers(false));
    setLineGutterRestoreHandler(view, { onReject: vi.fn() });
    const coordinates = vi.spyOn(view, "posAtCoords");
    const number = view.dom.querySelector<HTMLElement>('.files-line-gutter__number[data-line="2"]')!;
    expect(number.textContent).toBe("");
    number.parentElement!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    expect(restores(view)).toHaveLength(1);
    restores(view)[0]!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    expect(restores(view)).toHaveLength(1);
    expect(coordinates).not.toHaveBeenCalled();
  });

  it("clears a hovered hunk when the comparison handler is reinstalled", () => {
    view = mountEditor("a\nz\nc\n");
    setLineGutterRestoreHandler(view, { onReject: vi.fn() });
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    view.contentDOM.querySelectorAll(".cm-line")[1]!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    expect(restores(view)).toHaveLength(1);

    setLineGutterRestoreHandler(view, { onReject: vi.fn() });
    expect(restores(view)).toHaveLength(0);
  });

  it("crowds a finding on its own line down to the wash", () => {
    view = mountEditor("a\nz\nc\n");
    setLineGutterRestoreHandler(view, { onReject: vi.fn() });
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    view.contentDOM.querySelectorAll(".cm-line")[1]!.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
    setLineGutterFindings(view, [
      mark(2, "high", "m"),
    ]);
    expect(restores(view).length).toBe(1);
    // The finding keeps its tint when the restore control occupies its slot.
    expect(glyphs(view)).toHaveLength(0);
    expect(tints(view)).toHaveLength(1);
  });
});

describe("line gutter — removed lines", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  const cuts = (v: EditorView) =>
    v.dom.querySelectorAll<HTMLButtonElement>("[data-testid='files-line-gutter-cut']");

  it("marks the row beside removed lines shown in place", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    expect(deletedRows(view)).toHaveLength(1);
    expect(cuts(view)).toHaveLength(0);
  });

  it("puts a mark on the edge where folded lines were removed", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    setScopeDeletedLines(view, "folded");
    expect(deletedRows(view)).toHaveLength(0);
    expect(cuts(view)[0]?.getAttribute("aria-label")).toBe(
      "1 line removed. Press Enter to show it in place.",
    );
  });

  it("previews on focus and opens in place on click or Enter", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    setScopeDeletedLines(view, "folded");
    const onShow = vi.fn();
    const onOpen = vi.fn();
    setLineGutterCutHandlers(view, { onShow, onHide: vi.fn(), onOpen });
    const cut = cuts(view)[0]!;
    cut.dispatchEvent(new FocusEvent("focus"));
    expect(onShow).toHaveBeenCalledWith(
      expect.objectContaining({ fromLine: 2, toLine: 2 }),
      expect.anything(),
      "focus",
    );
    cut.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    cut.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(onOpen).toHaveBeenCalledTimes(2);
  });

  it("previews on pointer entry only while the content rests, and closes when scrolling starts", () => {
    setupScrollActivity();
    try {
      view = mount("a\nc\n", "a\nb\nc\n");
      setScopeDeletedLines(view, "folded");
      const onShow = vi.fn();
      const onHide = vi.fn();
      setLineGutterCutHandlers(view, { onShow, onHide, onOpen: vi.fn() });
      const cut = cuts(view)[0]!;
      cut.dispatchEvent(new MouseEvent("mouseenter"));
      expect(onShow).toHaveBeenLastCalledWith(expect.anything(), expect.anything(), "pointer");

      view.scrollDOM.dispatchEvent(new Event("scroll"));
      expect(onHide).toHaveBeenCalledTimes(1);
      cut.dispatchEvent(new MouseEvent("mouseleave"));
      cut.dispatchEvent(new MouseEvent("mouseenter"));
      expect(onShow).toHaveBeenCalledTimes(1);
      expect(onHide).toHaveBeenCalledTimes(1);

      view.scrollDOM.dispatchEvent(new Event("scrollend"));
      cut.dispatchEvent(new MouseEvent("mouseenter"));
      expect(onShow).toHaveBeenCalledTimes(2);
    } finally {
      resetScrollActivityForTests();
    }
  });

  const foldBacks = (v: EditorView) =>
    v.dom.querySelectorAll<HTMLButtonElement>("[data-testid='files-line-gutter-foldback']");

  it("keeps removals shown in place without a way to fold them", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    expect(deletedRows(view)).toHaveLength(1);
    expect(foldBacks(view)).toHaveLength(0);
  });

  it("makes the delete bar beside opened lines fold them back", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    setScopeDeletedLines(view, "folded");
    openScopeDeletion(view, editorScopeDiffFolds(view.state)[0]!);
    const bar = foldBacks(view)[0]!;
    expect(bar.getAttribute("aria-label")).toBe("Fold these lines");
    expect(bar.closest(".files-line-gutter__cell--del")).toBeTruthy();

    bar.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
    expect(foldBacks(view)).toHaveLength(0);
  });

  it("folds opened lines back from the keyboard", () => {
    view = mount("a\nc\n", "a\nb\nc\n");
    setScopeDeletedLines(view, "folded");
    openScopeDeletion(view, editorScopeDiffFolds(view.state)[0]!);
    foldBacks(view)[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
  });

  it("finds the change a folded removal belongs to, for Reject", () => {
    view = mountEditor("a\nc\n");
    applyEditorScopeDiff(view, { original: "a\nb\nc\n" });
    setScopeDeletedLines(view, "folded");
    const [fold] = editorScopeDiffFolds(view.state);
    expect(restoreHunkForFold(view, fold!)).toMatchObject({
      kind: "change",
      beforeStart: 1,
      beforeEnd: 2,
    });
  });
});

describe("line gutter — numbers off", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("keeps the marks and drops only the digits", () => {
    view = mount("a\nb\n", undefined, lineGutterNumbers(false));
    setLineGutterFindings(view, [
      mark(1, "low", "m"),
    ]);
    expect(columns(view)).toHaveLength(1);
    expect(glyphs(view)).toHaveLength(1);
    const number = view.dom.querySelector(
      ".files-line-gutter__number:not(.files-line-gutter__number--spacer)",
    );
    expect(number?.textContent).toBe("");
  });
});
