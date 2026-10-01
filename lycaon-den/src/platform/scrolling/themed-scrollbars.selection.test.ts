// @vitest-environment jsdom
import { makeFrame } from "./themed-scrollbars-test-harness.ts";
import { OverlayScrollbars } from "overlayscrollbars";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  attachThemedViewportScrollbar,
  resetScrollMeasureQuietForTests,
  syncThemedScrollbar,
} from "./themed-scrollbars.ts";

/** Simulates selection loss triggered when viewport attachment moves focus. */
function dropSelectionWhileAttaching(): () => void {
  const factory = vi.mocked(OverlayScrollbars);
  const library = factory.getMockImplementation();
  factory.mockImplementation(((...args: Parameters<typeof OverlayScrollbars>) => {
    // Only construction wraps the viewport; a lookup call moves no focus.
    if (args.length > 1) window.getSelection()?.removeAllRanges();
    return library?.(...args);
  }) as typeof OverlayScrollbars);
  return () => factory.mockImplementation(library as typeof OverlayScrollbars);
}

/** jsdom's Selection stringifier is empty; read the range instead. */
function selectedText(): string {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) return "";
  return selection.getRangeAt(0).toString();
}

function selectProse(text: string): Text {
  const prose = document.createElement("p");
  const node = document.createTextNode(text);
  prose.append(node);
  document.body.append(prose);
  const range = document.createRange();
  range.setStart(node, 0);
  range.setEnd(node, text.length);
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
  return node;
}

describe("scrollport frames and the reader's selection", () => {
  afterEach(() => {
    resetScrollMeasureQuietForTests();
    document.body.innerHTML = "";
    window.getSelection()?.removeAllRanges();
  });

  it("keeps a document selection alive when a frame attaches behind it", () => {
    selectProse("keep me selected");
    expect(selectedText()).toBe("keep me selected");
    const restore = dropSelectionWhileAttaching();
    const { frame } = makeFrame();
    document.body.append(frame);

    syncThemedScrollbar(frame);
    restore();

    expect(selectedText()).toBe("keep me selected");
  });

  it("keeps a document selection alive when a retained frame detaches", () => {
    const { frame, viewport } = makeFrame();
    document.body.append(frame);
    const release = attachThemedViewportScrollbar(frame, viewport);
    selectProse("still selected");

    const instance = OverlayScrollbars(frame) as unknown as { destroy: () => void };
    const unwrap = vi.spyOn(instance, "destroy").mockImplementation(() => {
      window.getSelection()?.removeAllRanges();
    });
    release();
    unwrap.mockRestore();

    expect(selectedText()).toBe("still selected");
  });
});
