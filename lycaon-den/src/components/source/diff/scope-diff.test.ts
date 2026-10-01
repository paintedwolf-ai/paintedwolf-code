// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { EditorView } from "@codemirror/view";
import { createSourceEditorState } from "../editor/codemirror-theme.ts";
import {
  applyEditorScopeDiff,
  editorScopeDiffChunks,
  editorScopeDiffFolds,
  editorScopeDiffOriginal,
  foldScopeDeletion,
  openScopeDeletion,
  renderScopeDeletedLines,
  setScopeDeletedLines,
  setScopeDeletionPeek,
} from "./scope-diff.ts";

function editor(doc: string, editable = true): EditorView {
  return new EditorView({
    parent: document.createElement("div"),
    state: createSourceEditorState({ surface: "files", doc, editable, language: null }),
  });
}

function type(view: EditorView, from: number, to: number, insert: string): void {
  view.dispatch({ changes: { from, to, insert }, userEvent: "input.type" });
}

describe("scope comparison", () => {
  it("does not rebuild for the same input", () => {
    const view = editor("after\n");
    const dispatch = vi.spyOn(view, "dispatch");
    try {
      applyEditorScopeDiff(view, { original: "before\n" });
      expect(dispatch).toHaveBeenCalledTimes(1);

      applyEditorScopeDiff(view, { original: "before\n" });
      expect(dispatch).toHaveBeenCalledTimes(1);

      applyEditorScopeDiff(view, { original: "older\n" });
      expect(dispatch).toHaveBeenCalledTimes(2);

      applyEditorScopeDiff(view, null);
      applyEditorScopeDiff(view, null);
      expect(dispatch).toHaveBeenCalledTimes(3);
    } finally {
      view.destroy();
    }
  });

  it("reapplies after an editor state replacement", () => {
    const state = () => createSourceEditorState({ surface: "files", doc: "after\n", editable: false, language: null });
    const view = new EditorView({ state: state() });
    const dispatch = vi.spyOn(view, "dispatch");
    try {
      applyEditorScopeDiff(view, { original: "before\n" });
      expect(dispatch).toHaveBeenCalledTimes(1);

      view.setState(state());
      applyEditorScopeDiff(view, { original: "before\n" });
      expect(dispatch).toHaveBeenCalledTimes(2);
    } finally {
      view.destroy();
    }
  });

  it("paints a whole-file addition when the start is empty", () => {
    const view = editor("hello\nworld\n");
    try {
      applyEditorScopeDiff(view, { original: "" });
      expect(view.dom.querySelector(".cm-changedLine")).toBeTruthy();
      // Every word is new, so the line fill alone carries the change.
      expect(view.dom.querySelector(".cm-changedText")).toBeNull();
      expect(view.dom.classList.contains("cm-merge-b")).toBe(true);
    } finally {
      view.destroy();
    }
  });

  it("marks changed words only on lines that keep a word", () => {
    const view = editor("let renamed = value;\nthrow error;\n");
    try {
      applyEditorScopeDiff(view, { original: "let original = value;\nreturn result;\n" });
      const marked = [...view.dom.querySelectorAll(".cm-changedText")];
      expect(marked.length).toBeGreaterThan(0);
      for (const node of marked) expect(node.closest(".cm-line")?.textContent).toBe("let renamed = value;");
    } finally {
      view.destroy();
    }
  });

  it("stays live while the person types", () => {
    const view = editor("a\nb\n");
    try {
      applyEditorScopeDiff(view, { original: "a\nb\n" });
      expect(editorScopeDiffChunks(view.state)).toHaveLength(0);

      type(view, 4, 4, "c\n");
      expect(editorScopeDiffChunks(view.state)).toHaveLength(1);
      expect(view.dom.querySelector(".cm-changedLine")).toBeTruthy();
    } finally {
      view.destroy();
    }
  });
});

describe("authorship through local typing", () => {
  const original = "a\nb\nc x\nd\n", tip = "a\nb\nC x\nd\n";
  const attribution = { before: [{ index: 4, length: 1, selected: true, visible: true, contributors: [] }],
    after: [{ index: 4, length: 1, selected: true, visible: true, contributors: [] }] };

  it.each([0, 5])("keeps the actual saved base and marks only authored text when typing at %i", (position) => {
    const view = editor(tip);
    try {
      applyEditorScopeDiff(view, { original, tip, attribution });
      type(view, position, position, "typed");
      expect(editorScopeDiffOriginal(view.state)).toBe(original);
      expect([...view.dom.querySelectorAll(".cm-changedText")].map((node) => node.textContent).join("")).toBe("C");
    } finally { view.destroy(); }
  });

  it("maps saved authorship over typing that predates the comparison", () => {
    const view = editor("typed" + tip);
    try {
      applyEditorScopeDiff(view, { original, tip, attribution });
      expect(editorScopeDiffOriginal(view.state)).toBe(original);
      expect([...view.dom.querySelectorAll(".cm-changedText")].map((node) => node.textContent).join("")).toBe("C");
    } finally { view.destroy(); }
  });
});

describe("deleted lines", () => {
  const start = "a\nb\nc\n";
  const doc = "a\nc\n";

  it("shows removed lines in place by default", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      expect(view.dom.querySelector(".cm-deletedChunk")?.textContent).toBe("b");
      expect(editorScopeDiffFolds(view.state)).toHaveLength(0);
    } finally {
      view.destroy();
    }
  });

  it("refreshes folded removals when visible authorship changes without a text change", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      setScopeDeletedLines(view, "folded");
      expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
      const chunks = editorScopeDiffChunks(view.state);
      applyEditorScopeDiff(view, { original: start, attribution: { before: [], after: [] } });
      expect(editorScopeDiffChunks(view.state)).toBe(chunks);
      expect(editorScopeDiffFolds(view.state)).toHaveLength(0);
      applyEditorScopeDiff(view, { original: start, attribution: {
        before: [{ index: 2, length: 2, selected: true, visible: true, contributors: [] }], after: [],
      } });
      expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
    } finally { view.destroy(); }
  });

  it("counts removed rows in the document height before they are drawn", () => {
    // An undeclared height arrives on measurement, moving everything below it.
    const view = editor("a\nd\n");
    try {
      const plain = view.contentHeight;
      applyEditorScopeDiff(view, { original: "a\nb\nc\nd\n" });
      // Two rows use a 13px font at 1.5 line height.
      expect(view.contentHeight - plain).toBeCloseTo(2 * 19.5);
    } finally {
      view.destroy();
    }
  });

  it("folds removed lines into a seam and opens one at a time", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      setScopeDeletedLines(view, "folded");
      expect(view.dom.querySelector(".cm-deletedChunk")).toBeNull();
      expect(view.dom.querySelector(".cm-den-cut")).toBeTruthy();

      const [fold] = editorScopeDiffFolds(view.state);
      expect(fold).toMatchObject({ line: 2, fromLine: 2, toLine: 2 });

      setScopeDeletionPeek(view, fold!.at);
      expect(view.dom.querySelector(".cm-den-cut--active")).toBeTruthy();

      openScopeDeletion(view, fold!);
      expect(editorScopeDiffFolds(view.state)).toHaveLength(0);
      const opened = view.dom.querySelector<HTMLElement>(".cm-deletedChunk")!;
      expect(opened.textContent).toBe("b");

      // Clicking the rows selects text; only the delete bar folds them back.
      opened.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      expect(editorScopeDiffFolds(view.state)).toHaveLength(0);

      foldScopeDeletion(view, fold!.at);
      expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
    } finally {
      view.destroy();
    }
  });

  it("keeps the fold list stable while its inputs are", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      setScopeDeletedLines(view, "folded");
      expect(editorScopeDiffFolds(view.state)).toBe(editorScopeDiffFolds(view.state));
    } finally {
      view.destroy();
    }
  });

  it("closes opened removals when the choice changes", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      setScopeDeletedLines(view, "folded");
      openScopeDeletion(view, editorScopeDiffFolds(view.state)[0]!);
      setScopeDeletedLines(view, "inplace");
      setScopeDeletedLines(view, "folded");
      expect(editorScopeDiffFolds(view.state)).toHaveLength(1);
    } finally {
      view.destroy();
    }
  });

  it("keeps a drag that starts in removed text inside it", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      const removed = view.dom.querySelector<HTMLElement>(".cm-deletedChunk")!;
      removed.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      expect(view.dom.classList.contains("cm-den-selecting-removed")).toBe(true);

      view.dom.querySelector(".cm-line")!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      expect(view.dom.classList.contains("cm-den-selecting-removed")).toBe(false);
    } finally {
      view.destroy();
    }
  });

  function clipboardEvent(type: "copy" | "cut") {
    const data = new Map<string, string>();
    const event = new Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", {
      value: {
        clearData: () => data.clear(),
        setData: (format: string, value: string) => data.set(format, value),
      },
    });
    return { event, data };
  }

  /** The selection stub represents a native drag inside removed text. */
  function selectText(node: Node, text: string) {
    return vi.spyOn(document, "getSelection").mockReturnValue({
      isCollapsed: false,
      anchorNode: node,
      focusNode: node,
      toString: () => text,
    } as unknown as Selection);
  }

  for (const type of ["copy", "cut"] as const) {
    it(`puts selected removed text on the clipboard on ${type}, changing nothing`, () => {
      const view = editor(doc);
      try {
        applyEditorScopeDiff(view, { original: start });
        const removed = view.dom.querySelector<HTMLElement>(".cm-deletedLine")!;
        const selection = selectText(removed.firstChild!, "b");

        const { event, data } = clipboardEvent(type);
        view.contentDOM.dispatchEvent(event);
        selection.mockRestore();
        expect(data.get("text/plain")).toBe("b");
        expect(event.defaultPrevented).toBe(true);
        expect(view.state.doc.toString()).toBe(doc);
      } finally {
        view.destroy();
      }
    });
  }

  it("leaves a copy of document text to the editor", () => {
    const view = editor(doc);
    try {
      applyEditorScopeDiff(view, { original: start });
      const line = view.dom.querySelector<HTMLElement>(".cm-line")!;
      const selection = selectText(line.firstChild!, "a");
      const { event, data } = clipboardEvent("copy");
      view.contentDOM.dispatchEvent(event);
      selection.mockRestore();
      expect(data.get("text/plain")).not.toBe("a");
    } finally {
      view.destroy();
    }
  });

  it("renders one preview row per removed line", () => {
    const view = editor("a\nd\n");
    try {
      applyEditorScopeDiff(view, { original: "a\nb\nc\nd\n" });
      setScopeDeletedLines(view, "folded");
      const [fold] = editorScopeDiffFolds(view.state);
      const rows = renderScopeDeletedLines(view.state, fold!.chunk);
      expect(rows.map((row) => row.textContent)).toEqual(["b", "c"]);
      expect(fold).toMatchObject({ fromLine: 2, toLine: 3 });
    } finally {
      view.destroy();
    }
  });
});


describe("large changed ranges", () => {
  it("builds line decorations and gutter cells only for visible source", () => {
    const view = editor(Array.from({ length: 10000 }, (_, i) => `line ${i}`).join("\n"));
    const line = vi.spyOn(view.state.doc, "line");
    try {
      expect(view.visibleRanges.at(-1)!.to).toBeLessThan(view.state.doc.length);
      applyEditorScopeDiff(view, { original: "" });
      expect(view.dom.querySelector(".cm-changedLine")).toBeTruthy();
      expect(line.mock.calls.length).toBeLessThan(1000);
    } finally { line.mockRestore(); view.destroy(); }
  });
});
