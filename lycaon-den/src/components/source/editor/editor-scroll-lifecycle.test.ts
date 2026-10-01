// @vitest-environment jsdom
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import { editorScrollLifecycle } from "./editor-scroll-lifecycle.ts";
import {
  resetScrollActivityForTests,
  setupScrollActivity,
} from "../../../platform/scrolling/scroll-activity.ts";

const recorded = vi.hoisted(() => [] as [string, number][]);
vi.mock("../../../chat/stream/den-main-thread-perf.ts", () => ({
  recordSyncDuration: (label: string, ms: number) => recorded.push([label, ms]),
}));

let view: EditorView | undefined;
afterEach(() => {
  view?.destroy();
  view = undefined;
  resetScrollActivityForTests();
  recorded.length = 0;
  document.body.replaceChildren();
});

describe("editor scroll lifecycle", () => {
  it("tells the editor once when a scroll burst settles", () => {
    setupScrollActivity();
    view = new EditorView({ doc: "one\ntwo", extensions: editorScrollLifecycle("files"), parent: document.body });
    const settled = vi.spyOn(view, "noteScrollSettled");
    view.scrollDOM.dispatchEvent(new Event("scroll"));
    view.scrollDOM.dispatchEvent(new Event("scroll"));
    expect(settled).not.toHaveBeenCalled();
    view.scrollDOM.dispatchEvent(new Event("scrollend"));
    expect(settled).toHaveBeenCalledTimes(1);
  });

  it("reports measures apart from viewport swaps", () => {
    view = new EditorView({ doc: "one\ntwo", extensions: editorScrollLifecycle("files"), parent: document.body });
    (view as unknown as { measure(): void }).measure();
    expect(recorded.at(-1)?.[0]).toBe("files.measure");
  });

  it("reports each surface's cost under its own name", () => {
    // A capture that pooled a reader's swaps with a file's could not say whose
    // frame was slow, which is the only comparison worth making.
    view = new EditorView({ doc: "one\ntwo", extensions: editorScrollLifecycle("diffs"), parent: document.body });
    (view as unknown as { measure(): void }).measure();
    expect(recorded.at(-1)?.[0]).toBe("diffs.measure");
  });
});
