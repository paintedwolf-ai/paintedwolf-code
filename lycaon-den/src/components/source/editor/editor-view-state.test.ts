// @vitest-environment jsdom
import { required } from "../../../test/at.ts";
import { editorScrollPosition, setEditorScrollTop } from "./editor-scroll-position.ts";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { codeFolding, foldEffect, foldedRanges } from "@codemirror/language";
import { beforeEach, describe, expect, it } from "vitest";
import {
  EditorViewStateMap,
  EDITOR_VIEW_STATE_LRU,
  captureEditorViewState,
  editorViewStateKey,
  restoreEditorViewState,
  parseEditorViewStateStore,
} from "./editor-view-state.ts";

function mountView(doc: string): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({
    state: EditorState.create({
      doc,
      extensions: [codeFolding(), editorScrollPosition],
    }),
    parent,
  });
}

describe("editor-view-state", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("round-trips cursor, scroll, and folds", async () => {
    const view = mountView("aaa\nbbb\nccc\nddd\n");
    view.dispatch({
      selection: { anchor: 4, head: 7 },
      effects: foldEffect.of({ from: 8, to: 12 }),
    });
    setEditorScrollTop(view, 42);
    const captured = captureEditorViewState(view, "sha-abc");
    expect(captured).not.toBeNull();
    expect(captured!.cursor).toEqual({ anchor: 4, head: 7 });
    expect(captured!.scrollTop).toBe(42);
    expect(captured!.folds).toEqual([{ from: 8, to: 12 }]);

    const next = mountView("aaa\nbbb\nccc\nddd\n");
    expect(restoreEditorViewState(next, captured!, "sha-abc")).toBe(true);
    expect(next.state.selection.main.anchor).toBe(4);
    expect(next.state.selection.main.head).toBe(7);
    // Restoration waits for the first measured scroll range.
    expect(next.scrollDOM.scrollTop).toBe(0);
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => resolve());
    });
    expect(next.scrollDOM.scrollTop).toBe(42);
    const folds: { from: number; to: number }[] = [];
    foldedRanges(next.state).between(0, next.state.doc.length, (from, to) => {
      folds.push({ from, to });
    });
    expect(folds).toEqual([{ from: 8, to: 12 }]);
    view.destroy();
    next.destroy();
  });

  it("restores the top of a retained editor that is currently scrolled", async () => {
    const view = mountView("aaa\nbbb\nccc\n");
    try {
      const record = captureEditorViewState(view, "sha-current")!;
      setEditorScrollTop(view, 42);
      expect(restoreEditorViewState(view, record, "sha-current")).toBe(true);
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(view.scrollDOM.scrollTop).toBe(0);
      expect(captureEditorViewState(view, "sha-current")?.scrollTop).toBe(0);
    } finally {
      view.destroy();
    }
  });

  it("sha mismatch restores nothing and throws nothing", () => {
    const view = mountView("hello\n");
    const record = captureEditorViewState(view, "sha-old")!;
    expect(() =>
      restoreEditorViewState(view, record, "sha-new"),
    ).not.toThrow();
    expect(restoreEditorViewState(view, record, "sha-new")).toBe(false);
    view.destroy();
  });

  it("LRU keeps 200 newest by capturedAt", () => {
    const map = new EditorViewStateMap();
    for (let i = 0; i < EDITOR_VIEW_STATE_LRU + 1; i++) {
      map.set(editorViewStateKey("root", `f${i}.ts`), {
        sha: `s${i}`,
        cursor: { anchor: 0, head: 0 },
        scrollTop: 0,
        folds: [],
        capturedAt: i,
      });
    }
    const store = map.toStore();
    expect(Object.keys(store.byKey)).toHaveLength(EDITOR_VIEW_STATE_LRU);
    expect(store.byKey[editorViewStateKey("root", "f0.ts")]).toBeUndefined();
    expect(
      store.byKey[editorViewStateKey("root", `f${EDITOR_VIEW_STATE_LRU}.ts`)],
    ).toBeTruthy();
  });

  it("retains presentation state for every open tab across restart", () => {
    const map = new EditorViewStateMap();
    const open = new Set<string>();
    for (let index = 0; index < 750; index++) {
      const key = editorViewStateKey("root", `${index}.ts`);
      if (index < 500) open.add(key);
      map.set(key, { sha: "revision", cursor: { anchor: index, head: index },
        selections: [{ anchor: index, head: index }, { anchor: 1000, head: 1001 }], mainSelection: 1,
        scrollTop: index * 10, folds: [], capturedAt: index });
    }
    const restored = required(parseEditorViewStateStore(JSON.parse(JSON.stringify(map.toStore(undefined, open)))));
    expect(Object.keys(restored.byKey)).toHaveLength(700);
    for (const key of open) expect(restored.byKey[key]?.mainSelection).toBe(1);
    expect(restored.byKey[editorViewStateKey("root", "0.ts")]?.scrollTop).toBe(0);
  });

  it("rekeys under a folder move", () => {
    const map = new EditorViewStateMap();
    const key = editorViewStateKey("r1", "src/a.ts");
    map.set(key, {
      sha: "x",
      cursor: { anchor: 1, head: 1 },
      scrollTop: 3,
      folds: [],
      capturedAt: 1,
    });
    map.rekeyUnderPath("r1", "src", "lib");
    expect(map.get(key)).toBeUndefined();
    expect(map.get(editorViewStateKey("r1", "lib/a.ts"))?.scrollTop).toBe(3);
  });
});

it("round trips paged reader coordinates and rejects malformed coordinates", () => {
  const base = { sha: "snapshot", cursor: { anchor: 0, head: 0 }, folds: [], scrollTop: 0, capturedAt: 1 };
  const reader = { viewport: { rank: 400000, fraction: 0.5, atEnd: true }, selection: { main: 0, ranges: [
    { anchor: { row: 400001, offset: 4, side: "after" }, head: { row: 400003, offset: 8, side: "after" } },
  ] } };
  const store = parseEditorViewStateStore({ byKey: { "root\0large.txt": { ...base, reader } } });
  expect(store?.byKey["root\0large.txt"]?.reader).toEqual(reader);
  const invalid = parseEditorViewStateStore({ byKey: { "root\0large.txt": { ...base, reader: { viewport: { rank: -1, fraction: 0 } } } } });
  expect(invalid?.byKey["root\0large.txt"]?.reader).toBeUndefined();
  const badAffinity = parseEditorViewStateStore({ byKey: { "root\0large.txt": { ...base, reader: { viewport: { rank: 0, fraction: 0, atEnd: "true" } } } } });
  expect(badAffinity?.byKey["root\0large.txt"]?.reader).toBeUndefined();
});
