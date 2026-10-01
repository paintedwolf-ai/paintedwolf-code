// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { startCompletion } from "@codemirror/autocomplete";
import { undo } from "@codemirror/commands";
import { EditorView } from "@codemirror/view";
import { createSourceEditorState } from "./codemirror-theme.ts";
import { acquireEditorViewport, clearEditorViewportPool, releaseEditorViewport } from "./editor-viewport-pool.ts";

afterEach(clearEditorViewportPool);

it("reuses a viewport across many documents without retaining their callbacks or text", () => {
  const host = document.createElement("div");
  const identities = new Set<EditorView>();
  const stale = vi.fn();
  for (let index = 0; index < 40; index++) {
    const view = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: `file ${index}`, handlers: { onDocChange: stale } }), host);
    identities.add(view);
    releaseEditorViewport(view);
    expect(view.state.doc.length).toBe(0);
    expect(host.children.length).toBe(0);
  }
  expect(identities.size).toBe(1);
  expect(stale).not.toHaveBeenCalled();
});

it("preserves per-document undo when parked state returns to a reused viewport", () => {
  const host = document.createElement("div");
  const first = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "first", editable: true }), host);
  first.dispatch({ changes: { from: 5, insert: " edited" }, selection: { anchor: 12 } });
  const parked = first.state;
  releaseEditorViewport(first);
  const second = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "second", editable: true }), host);
  second.dispatch({ changes: { from: 6, insert: " unrelated" } });
  releaseEditorViewport(second);
  const restored = acquireEditorViewport(parked, host);
  expect(restored).toBe(first);
  expect(restored.state.selection.main.head).toBe(12);
  expect(undo(restored)).toBe(true);
  expect(restored.state.doc.toString()).toBe("first");
  releaseEditorViewport(restored);
});

it("bounds idle retention and destroys excess viewports", () => {
  const views = Array.from({ length: 8 }, () => acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "large document" }), document.createElement("div")));
  const destroyed = views.map((view) => vi.spyOn(view, "destroy"));
  for (const view of views) releaseEditorViewport(view);
  expect(destroyed.filter((spy) => spy.mock.calls.length > 0)).toHaveLength(6);
  clearEditorViewportPool();
  expect(destroyed.every((spy) => spy.mock.calls.length === 1)).toBe(true);
});


it("keeps the page style nonce while a viewport is idle", () => {
  const style = document.createElement("style");
  style.nonce = "editor-pool-test";
  document.head.appendChild(style);
  try {
    const view = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "document" }), document.createElement("div"));
    releaseEditorViewport(view);
    expect(view.state.facet(EditorView.cspNonce)).toBe("editor-pool-test");
  } finally {
    clearEditorViewportPool();
    style.remove();
  }
});


it("cancels pending completion before releasing the document state", async () => {
  vi.useFakeTimers();
  try {
    const view = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "pending", editable: true }), document.createElement("div"));
    expect(startCompletion(view)).toBe(true);
    releaseEditorViewport(view);
    await vi.advanceTimersByTimeAsync(250);
    expect(view.state.doc.length).toBe(0);
  } finally { clearEditorViewportPool(); vi.useRealTimers(); }
});

it("hands a released reader viewport to the next reader", () => {
  const host = document.createElement("div");
  document.body.append(host);
  const first = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "" }), host);
  releaseEditorViewport(first);
  const second = acquireEditorViewport(createSourceEditorState({ surface: "files", doc: "" }), host);
  expect(second).toBe(first);
  releaseEditorViewport(second);
  host.remove();
});
