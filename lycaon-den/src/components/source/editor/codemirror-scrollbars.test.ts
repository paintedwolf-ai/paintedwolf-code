// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { waitFor } from "@solidjs/testing-library";
import { EditorView } from "@codemirror/view";
import { createSourceEditorState } from "./codemirror-theme.ts";

const detach = vi.fn();
const updateScrollbar = vi.fn();
type AttachOptions = { schedule?: (read: () => unknown, write: (geometry: unknown) => void) => void };
const attach = vi.fn((_host: HTMLElement, _viewport: HTMLElement, _options?: AttachOptions) => detach);

vi.mock("../../../platform/scrolling/themed-scrollbars.ts", () => ({
  verticalScrollbarChromeFor: () => undefined,
  attachThemedViewportScrollbar: (host: HTMLElement, viewport: HTMLElement, options?: AttachOptions) =>
    attach(host, viewport, options),
  readThemedScrollbarGeometry: () => ({ verticalPercent: "1", horizontalPercent: "1", translate: "translate(0px, 0px)" }),
  updateThemedViewportScrollbar: (host: HTMLElement, signature: string) =>
    updateScrollbar(host, signature),
}));

afterEach(() => {
  attach.mockClear();
  detach.mockClear();
  updateScrollbar.mockClear();
});

describe("source editor scrollbars", () => {
  it("hands the CodeMirror scroller to OverlayScrollbars on mount", async () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
    });
    try {
      expect(attach).not.toHaveBeenCalled();
      await waitFor(() => expect(attach).toHaveBeenCalledOnce());
      expect(attach.mock.calls[0]?.[0]).toBe(view.dom);
      expect(attach.mock.calls[0]?.[1]).toBe(view.scrollDOM);
    } finally {
      view.destroy();
    }
  });

  it("names the scroller's own parent as the host, mounted or not", async () => {
    const parent = document.createElement("div");
    parent.className = "den-files-editor__host";
    document.body.appendChild(parent);
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
      parent,
    });
    try {
      await waitFor(() => expect(attach).toHaveBeenCalledOnce());
      const [host, viewport] = attach.mock.calls[0]!;
      expect(viewport.parentElement).toBe(host);
      expect(host).toBe(view.dom);
    } finally {
      view.destroy();
      parent.remove();
    }
  });

  it("detaches when the editor goes away", async () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\n", editable: false }),
    });
    await waitFor(() => expect(attach).toHaveBeenCalledOnce());
    expect(detach).not.toHaveBeenCalled();
    view.destroy();
    await waitFor(() => expect(detach).toHaveBeenCalledTimes(1));
  });

  it("does not attach a scrollbar for an editor destroyed before measurement", async () => {
    const view = new EditorView({ state: createSourceEditorState({ surface: "files", doc: "cancelled" }) });
    expect(attach).not.toHaveBeenCalled();
    view.destroy();
    await new Promise(resolve => requestAnimationFrame(resolve));
    expect(attach).not.toHaveBeenCalled();
    expect(detach).not.toHaveBeenCalled();
  });

  it("places scroll-driven chrome through the editor's measure pass", async () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
    });
    try {
      await waitFor(() => expect(attach).toHaveBeenCalledOnce());
      const schedule = attach.mock.calls[0]?.[2]?.schedule;
      expect(schedule).toBeTypeOf("function");
      const geometry = { verticalPercent: "0.5" };
      const read = vi.fn(() => geometry);
      const write = vi.fn();
      schedule!(read, write);
      // The read lands in the editor's next measure pass and the write in its write phase.
      await waitFor(() => expect(write).toHaveBeenCalled());
      expect(write.mock.calls[0]?.[0]).toBe(geometry);
      expect(read).toHaveBeenCalledOnce();
    } finally {
      view.destroy();
    }
  });

  it("does not request a scrollbar update for selection-only transactions", () => {
    const view = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "one\ntwo\n", editable: true }),
    });
    try {
      updateScrollbar.mockClear();
      view.dispatch({ selection: { anchor: 1 } });
      expect(updateScrollbar).not.toHaveBeenCalled();
    } finally {
      view.destroy();
    }
  });

});
