// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { foldService } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { EditorView, showTooltip } from "@codemirror/view";
import { REVEAL_FLASH_MS } from "../../../ui/reveal-flash.ts";
import { ALL_OVERVIEW_TICK_KINDS } from "../annotations/overview-ruler-model.ts";
import {
  LARGE_DOCUMENT_CHAR_THRESHOLD,
  LARGE_DOCUMENT_LINE_THRESHOLD,
  LONG_LINE_THRESHOLD,
  applyEditorDisplayPrefs,
  applySourceEditorEditable,
  clearScopePreview,
  clearSymbolEmphasis,
  clearSymbolPending,
  markSymbolPending,
  createSourceEditorState,
  emphasizeAndScrollToLine,
  emphasizeScopePreview,
  emphasizeSymbolRangeOnly,
  openEditorGotoLine,
  sourceDisplayEffects,
  sourceUsesSimplifiedDisplay,
  textNeedsSimplifiedDisplay,
} from "./codemirror-theme.ts";
import { languageExtensionForPath } from "./codemirror-lang.ts";
import {
  gotoLineController,
  resetGotoLineForTests,
  submitGotoLine,
} from "../../../find/goto-line-controller.ts";

/** Completion sources the editor would consult at `pos`. */
function completionSourcesAt(
  state: EditorState,
  pos: number,
): readonly unknown[] {
  return state.languageDataAt<unknown>("autocomplete", pos);
}

describe("createSourceEditorState shared features", () => {
  it("keeps editor height and containment off the portaled tooltip container", () => {
    const parent = document.createElement("div");
    document.body.append(parent);
    const view = new EditorView({
      parent,
      state: createSourceEditorState({ surface: "files", doc: "text", editable: true, extensions: showTooltip.of(null) }),
    });
    try {
      const tooltipHost = Array.from(document.body.children).find(element => element.className === view.themeClasses);
      expect(tooltipHost).toBeDefined();
      expect(getComputedStyle(view.dom).height).toBe("100%");
      expect(getComputedStyle(view.dom).contain).toContain("layout");
      expect(getComputedStyle(tooltipHost!).height).not.toBe("100%");
      expect(getComputedStyle(tooltipHost!).contain).not.toContain("layout");
    } finally {
      view.destroy();
      parent.remove();
    }
  });

  it("reserves a stable width for the line-number gutter", () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
    });
    try {
      const css = [...document.querySelectorAll("style")]
        .map((el) => el.textContent ?? "")
        .join("\n");
      expect(css).toContain(
        ".cm-gutters {background-color: var(--den-background); color: color-mix(in srgb, var(--den-text-muted) 72%, transparent); border: none; padding-left: 0",
      );
    } finally {
      view.destroy();
    }
  });

  it("gives read-only buffers fold + rectangular selection", () => {
    const state = createSourceEditorState({ surface: "files",
      doc: "function f() {\n  return 1;\n}\n",
      editable: false,
      language: null,
    });
    expect(state.facet(EditorState.allowMultipleSelections)).toBe(true);
    const view = new EditorView({ state });
    try {
      // Folding is mounted even without a language parser.
      expect(view.state.facet(foldService)).toBeDefined();
      // Scroll past end applies to read-only buffers.
      expect(view.contentDOM.style.paddingBottom).not.toBe("");
    } finally {
      view.destroy();
    }
  });

  it("accepts keyboard focus while a buffer is read-only and keeps it when editing becomes available", () => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({ parent, state: createSourceEditorState({ surface: "files", doc: "Loading", editable: false }) });
    try {
      view.focus();
      expect(document.activeElement).toBe(view.contentDOM);
      expect(view.state.facet(EditorState.readOnly)).toBe(true);
      applySourceEditorEditable(view, { editable: true, commandBridge: false });
      expect(document.activeElement).toBe(view.contentDOM);
      expect(view.state.facet(EditorState.readOnly)).toBe(false);
    } finally {
      view.destroy();
      parent.remove();
    }
  });

  it("offers buffer words in a buffer with no grammar", () => {
    const doc = "hello hello\nhel";
    const state = createSourceEditorState({ surface: "files", doc, editable: true });
    expect(state.facet(EditorState.allowMultipleSelections)).toBe(true);
    expect(completionSourcesAt(state, doc.length)).toHaveLength(1);
  });

  it("offers buffer words alongside the grammar's own completions", () => {
    const doc = ".card { colo }";
    const state = createSourceEditorState({ surface: "files",
      doc,
      editable: true,
      language: languageExtensionForPath("app.css"),
    });
    // Language data keeps buffer words beside the grammar's own source.
    expect(completionSourcesAt(state, doc.indexOf("colo") + 4)).toHaveLength(2);
  });

  it("leaves read-only buffers without completion", () => {
    const doc = "hello hello\nhel";
    const state = createSourceEditorState({ surface: "files", doc, editable: false });
    expect(completionSourcesAt(state, doc.length)).toHaveLength(0);
  });

  it("marks and clears a pending symbol without touching the selection", () => {
    const doc = "const resolveProjectRoot = 1;";
    const from = doc.indexOf("resolveProjectRoot");
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc, editable: true }),
    });
    try {
      markSymbolPending(view, from, from + "resolveProjectRoot".length);
      expect(view.state.selection.main.empty).toBe(true);
      expect(
        view.dom.querySelector(".cm-den-symbol-pending")?.textContent,
      ).toBe("resolveProjectRoot");
      clearSymbolPending(view);
      expect(view.dom.querySelector(".cm-den-symbol-pending")).toBeNull();
    } finally {
      view.destroy();
    }
  });

  it("emphasizes and clears an exact symbol without changing selection", () => {
    const doc = "const resolveProjectRoot = 1;";
    const from = doc.indexOf("resolveProjectRoot");
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc, editable: true }),
    });
    try {
      emphasizeSymbolRangeOnly(view, from, from + "resolveProjectRoot".length);
      expect(view.state.selection.main.empty).toBe(true);
      expect(view.dom.querySelector(".cm-den-symbol-target")?.textContent).toBe(
        "resolveProjectRoot",
      );
      clearSymbolEmphasis(view);
      expect(view.dom.querySelector(".cm-den-symbol-target")).toBeNull();
    } finally {
      view.destroy();
    }
  });
});

describe("go to line", () => {
  it("opens the shared editor command surface", () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
    });
    try {
      openEditorGotoLine(view);
      expect(gotoLineController.isOpen()).toBe(true);
      expect(submitGotoLine("2:2")).toBe(true);
      expect(view.state.selection.main.head).toBe(view.state.doc.line(2).from + 1);
      expect(gotoLineController.isOpen()).toBe(false);
    } finally {
      resetGotoLineForTests();
      view.destroy();
    }
  });
});

describe("simplified source display", () => {
  const long = `${"x".repeat(LONG_LINE_THRESHOLD + 1)}\n`;

  const wraps = (state: EditorState): boolean =>
    state
      .facet(EditorView.contentAttributes)
      .some((v) => typeof v !== "function" && v.class === "cm-lineWrapping");

  it("applies long-line limits in the same transaction as a loaded range", () => {
    const prefs = { wordWrap: true, lineNumbers: true, fontSize: 13, fontFamily: "default", lineHeight: 1.5,
      indentGuides: true, whitespace: true, scrollbarTicks: ALL_OVERVIEW_TICK_KINDS, indent: { style: "spaces" as const, width: 4 } };
    let state = createSourceEditorState({ surface: "files", doc: "", ...prefs, editable: false });
    state = state.update({ changes: { from: 0, insert: long }, effects: sourceDisplayEffects(state, prefs, true) }).state;
    expect(sourceUsesSimplifiedDisplay(state)).toBe(true);
    expect(wraps(state)).toBe(false);
    state = state.update({ changes: { from: 0, to: state.doc.length, insert: "short" }, effects: sourceDisplayEffects(state, prefs, false) }).state;
    expect(sourceUsesSimplifiedDisplay(state)).toBe(false);
    expect(wraps(state)).toBe(true);
  });

  it("detects a long line anywhere in the file", () => {
    expect(textNeedsSimplifiedDisplay("short\nlines\n")).toBe(false);
    // A long final line with no trailing newline counts too.
    expect(
      textNeedsSimplifiedDisplay("x".repeat(LONG_LINE_THRESHOLD + 1)),
    ).toBe(true);
    expect(textNeedsSimplifiedDisplay(long)).toBe(true);
    expect(textNeedsSimplifiedDisplay(`ok\n${long}ok\n`)).toBe(true);
    // Many short lines summing past the threshold are not long lines.
    expect(textNeedsSimplifiedDisplay("abcd\n".repeat(4000))).toBe(false);
  });

  it("simplifies documents whose size or height makes scrubbing costly", () => {
    expect(
      textNeedsSimplifiedDisplay("x".repeat(LARGE_DOCUMENT_CHAR_THRESHOLD + 1)),
    ).toBe(true);
    expect(
      textNeedsSimplifiedDisplay(
        Array(LARGE_DOCUMENT_LINE_THRESHOLD + 1).fill("x").join("\n"),
      ),
    ).toBe(true);
    expect(
      textNeedsSimplifiedDisplay(
        Array(LARGE_DOCUMENT_LINE_THRESHOLD).fill("x").join("\n"),
      ),
    ).toBe(false);
  });

  it("drops wrapping for a long-lined file and keeps it otherwise", () => {
    const simplified = createSourceEditorState({ surface: "files",
      doc: long,
      wordWrap: true,
      editable: true,
    });
    expect(sourceUsesSimplifiedDisplay(simplified)).toBe(true);
    expect(wraps(simplified)).toBe(false);

    const tall = createSourceEditorState({ surface: "files",
      doc: Array(LARGE_DOCUMENT_LINE_THRESHOLD + 1).fill("x").join("\n"),
      wordWrap: true,
      editable: true,
    });
    expect(sourceUsesSimplifiedDisplay(tall)).toBe(true);
    expect(wraps(tall)).toBe(false);

    const normal = createSourceEditorState({ surface: "files",
      doc: "short\nlines\n",
      wordWrap: true,
      editable: true,
    });
    expect(sourceUsesSimplifiedDisplay(normal)).toBe(false);
    expect(wraps(normal)).toBe(true);
  });

  it("remeasures after display prefs change", () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files",
        doc: "short\nlines\n",
        wordWrap: false,
        editable: true,
      }),
    });
    const spy = vi.spyOn(view, "requestMeasure");
    try {
      applyEditorDisplayPrefs(view, {
        wordWrap: true,
        lineNumbers: true,
        fontSize: 13,
        fontFamily: "default",
        lineHeight: 1.5,
        indentGuides: false,
        whitespace: false,
        scrollbarTicks: ALL_OVERVIEW_TICK_KINDS,
        indent: { style: "spaces", width: 2 },
      });
      expect(spy).toHaveBeenCalled();
      expect(wraps(view.state)).toBe(true);
    } finally {
      view.destroy();
    }
  });
});

describe("stable extension identity", () => {
  // Stylesheet reuse avoids document-wide recalculation.
  it("mounts the editor theme stylesheet once across editors", () => {
    const themeCopies = () =>
      [...document.querySelectorAll("style")]
        .map((el) => el.textContent ?? "")
        .join("\n")
        .split("cm-den-arrival-flash-alt").length - 1;
    const first = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\n", editable: true }),
    });
    const mounted = themeCopies();
    const second = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "two\n", editable: true }),
    });
    try {
      expect(mounted).toBeGreaterThan(0);
      expect(themeCopies()).toBe(mounted);
    } finally {
      first.destroy();
      second.destroy();
    }
  });

  it("re-applies identical display prefs as a no-op", () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files",
        doc: "short\nlines\n",
        editable: true,
        wordWrap: false,
        lineNumbers: true,
        fontSize: 13,
        fontFamily: "default",
        lineHeight: 1.5,
        indentGuides: true,
        whitespace: false,
        indent: { style: "spaces", width: 4 },
      }),
    });
    const spy = vi.spyOn(view, "requestMeasure");
    const held = view.state;
    const prefs = {
      wordWrap: false,
      lineNumbers: true,
      fontSize: 13,
      fontFamily: "default",
      lineHeight: 1.5,
      indentGuides: true,
      whitespace: false,
      scrollbarTicks: ALL_OVERVIEW_TICK_KINDS,
      indent: { style: "spaces", width: 4 },
    } as const;
    try {
      applyEditorDisplayPrefs(view, prefs);
      expect(view.state).toBe(held);
      expect(spy).not.toHaveBeenCalled();
      applyEditorDisplayPrefs(view, { ...prefs, fontSize: 14 });
      expect(view.state).not.toBe(held);
      expect(spy).toHaveBeenCalled();
    } finally {
      view.destroy();
    }
  });
});

describe("editability as a compartment", () => {
  const build = (editable: boolean) =>
    new EditorView({
      state: createSourceEditorState({ surface: "files",
        doc: "alpha\nbravo\ncharlie\n",
        editable,
        commandBridge: editable,
        language: null,
      }),
    });

  it("flips a live view without disturbing the document or caret", () => {
    // Editability changes preserve the current selection.
    const view = build(true);
    try {
      view.dispatch({ selection: { anchor: 8, head: 8 } });
      const doc = view.state.doc.toString();

      applySourceEditorEditable(view, { editable: false, commandBridge: false });
      expect(view.state.readOnly).toBe(true);
      expect(view.state.doc.toString()).toBe(doc);
      expect(view.state.selection.main.head).toBe(8);

      applySourceEditorEditable(view, { editable: true, commandBridge: true });
      expect(view.state.readOnly).toBe(false);
      expect(view.state.selection.main.head).toBe(8);
    } finally {
      view.destroy();
    }
  });

  it("carries the editing affordances across the flip", () => {
    const view = build(false);
    try {
      expect(completionSourcesAt(view.state, 0)).toHaveLength(0);
      applySourceEditorEditable(view, { editable: true, commandBridge: true });
      expect(completionSourcesAt(view.state, 0).length).toBeGreaterThan(0);
      applySourceEditorEditable(view, { editable: false, commandBridge: false });
      expect(completionSourcesAt(view.state, 0)).toHaveLength(0);
    } finally {
      view.destroy();
    }
  });

  it("dispatches nothing when the view already has the requested mode", () => {
    const view = build(true);
    const spy = vi.spyOn(view, "dispatch");
    try {
      applySourceEditorEditable(view, { editable: true, commandBridge: true });
      expect(spy).not.toHaveBeenCalled();
    } finally {
      spy.mockRestore();
      view.destroy();
    }
  });
});

describe("arrival flash vs scope preview", () => {
  const DOC = "one\ntwo\nthree\nfour\nfive\n";

  function mountedView(): EditorView {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    return new EditorView({
      parent,
      state: createSourceEditorState({ surface: "files", doc: DOC, editable: true }),
    });
  }

  afterEach(() => {
    vi.useRealTimers();
    document.body.replaceChildren();
  });

  it("lands a single line as a caret and flashes it", () => {
    const view = mountedView();
    try {
      emphasizeAndScrollToLine(view, 2);
      const sel = view.state.selection.main;
      expect(sel.empty).toBe(true);
      expect(view.state.doc.lineAt(sel.head).number).toBe(2);
      expect(view.dom.querySelector(".cm-den-arrival-flash")).toBeTruthy();
    } finally {
      view.destroy();
    }
  });

  it("keeps a range citation selected after the flash expires", () => {
    const view = mountedView();
    vi.useFakeTimers();
    try {
      emphasizeAndScrollToLine(view, 2, 4);
      expect(view.dom.querySelectorAll(".cm-den-arrival-flash").length).toBe(3);
      vi.advanceTimersByTime(REVEAL_FLASH_MS);
      expect(view.dom.querySelector(".cm-den-arrival-flash")).toBeNull();
      const sel = view.state.selection.main;
      expect(view.state.doc.lineAt(sel.from).number).toBe(2);
      expect(view.state.doc.lineAt(sel.to - 1).number).toBe(4);
    } finally {
      view.destroy();
    }
  });

  it("scope preview does not steal or clear an arrival flash", () => {
    const view = mountedView();
    try {
      emphasizeAndScrollToLine(view, 1);
      emphasizeScopePreview(view, 3, 4);
      expect(view.dom.querySelector(".cm-den-arrival-flash")).toBeTruthy();
      expect(view.dom.querySelector(".cm-den-scope-preview")).toBeTruthy();
      clearScopePreview(view);
      expect(view.dom.querySelector(".cm-den-arrival-flash")).toBeTruthy();
      expect(view.dom.querySelector(".cm-den-scope-preview")).toBeNull();
    } finally {
      view.destroy();
    }
  });
});

describe("createSourceEditorState under the packaged CSP", () => {
  afterEach(() => {
    document.head.querySelectorAll("style[data-test-nonce]").forEach((el) => el.remove());
  });

  it("hands the page style nonce to CodeMirror's own stylesheets", () => {
    const stamped = document.createElement("style");
    stamped.nonce = "dGVzdA==";
    stamped.dataset.testNonce = "1";
    document.head.append(stamped);
    const state = createSourceEditorState({ surface: "files", doc: "one\n" });
    expect(state.facet(EditorView.cspNonce)).toBe("dGVzdA==");
  });

  it("adds no nonce when the page has none", () => {
    const state = createSourceEditorState({ surface: "files", doc: "one\n" });
    expect(state.facet(EditorView.cspNonce)).toBe("");
  });
});
