// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { javascript } from "@codemirror/lang-javascript";
import { syntaxTreeAvailable } from "@codemirror/language";
import { highlightBeforePaint } from "./codemirror-highlight-ready.ts";

const views: EditorView[] = [];
afterEach(() => { for (const view of views.splice(0)) view.destroy(); vi.useRealTimers(); });

const doc = Array.from({ length: 6000 }, (_, i) => `const value${i} = compute(${i}) + "text ${i}"; // ${i}`).join("\n");

function open(extensions: unknown[]): EditorView {
  const parent = document.createElement("div");
  document.body.append(parent);
  const view = new EditorView({ parent, state: EditorState.create({ doc, extensions: extensions as never }) });
  views.push(view);
  return view;
}

describe("highlight before paint", () => {
  it("parses the visible range before the frame paints and the rest in idle slices", async () => {
    vi.useFakeTimers();
    const view = open([javascript(), highlightBeforePaint()]);
    await Promise.resolve();
    expect(syntaxTreeAvailable(view.state, view.viewport.to)).toBe(true);
    // The background worker alone stops well ahead of the viewport; the idle slices finish the file.
    for (let i = 0; i < 200 && !syntaxTreeAvailable(view.state, view.state.doc.length); i++) await vi.advanceTimersByTimeAsync(60);
    expect(syntaxTreeAvailable(view.state, view.state.doc.length)).toBe(true);
  });

  it("does nothing without a language", async () => {
    vi.useFakeTimers();
    const view = open([highlightBeforePaint()]);
    const dispatch = vi.spyOn(view, "dispatch");
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(500);
    expect(dispatch).not.toHaveBeenCalled();
  });
});
