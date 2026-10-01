// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { EditorView } from "@codemirror/view";
import { editorScrollPosition, readEditorScrollPosition, setEditorScrollTop } from "./editor-scroll-position.ts";

it("records native offsets and explicit restorations without reads during snapshot access", () => {
  const view = new EditorView({ doc: "first\nsecond\n", extensions: editorScrollPosition });
  try {
    view.scrollDOM.scrollTop = 123;
    view.scrollDOM.scrollLeft = 45;
    view.scrollDOM.dispatchEvent(new Event("scroll"));
    const top = vi.spyOn(view.scrollDOM, "scrollTop", "get");
    const left = vi.spyOn(view.scrollDOM, "scrollLeft", "get");
    expect(readEditorScrollPosition(view)).toEqual({ top: 123, left: 45 });
    expect(top).not.toHaveBeenCalled();
    expect(left).not.toHaveBeenCalled();
    setEditorScrollTop(view, 321);
    expect(readEditorScrollPosition(view).top).toBe(321);
    expect(top).not.toHaveBeenCalled();
    top.mockRestore();
    left.mockRestore();
  } finally { view.destroy(); }
});
