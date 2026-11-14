import type { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { readReadingAnchor } from "./editor-reading-anchor.ts";

const PORT = { top: 100, bottom: 700, left: 0, right: 800 };

/** A view scrolled to `scrollTop` whose top line block is `blockHeight` tall; position p sits at 10 * p. */
function view(blockHeight: number, scrollTop: number, contentLeft = 40): EditorView {
  return {
    scrollDOM: { clientHeight: 600, scrollTop, getBoundingClientRect: () => PORT },
    contentDOM: { getBoundingClientRect: () => ({ left: contentLeft }) },
    lineBlockAtHeight: () => ({ top: 0, height: blockHeight }),
    documentTop: PORT.top - scrollTop,
    viewport: { from: 0, to: 1_000_000 },
    posAtCoords: ({ x, y }: { x: number; y: number }) => (x < PORT.left ? -1 : Math.floor((y - PORT.top + scrollTop) / 10)),
    coordsAtPos: (pos: number) => ({ top: PORT.top + 10 * pos - scrollTop, bottom: PORT.top + 10 * pos - scrollTop + 10 }),
  } as unknown as EditorView;
}

describe("reading anchor", () => {
  it("takes the first line, not the padding above it, at the top of the document", () => {
    // Scrolled to the top, the line starts 12px below the viewport's top edge.
    const at = view(50_000, 0);
    (at as unknown as { documentTop: number }).documentTop = PORT.top + 12;
    (at as unknown as { posAtCoords: (p: { x: number; y: number }) => number }).posAtCoords = ({ y }) => (y < PORT.top + 12 ? 0 : 7);
    expect(readReadingAnchor(at)?.pos).toBe(7);
  });

  it("has none while the line at the top fits in the viewport", () => {
    expect(readReadingAnchor(view(600, 5_000))).toBeNull();
  });

  it("takes the text at the top edge of a line taller than the viewport", () => {
    expect(readReadingAnchor(view(50_000, 5_005))).toEqual({ pos: 500, offset: -5 });
  });

  it("probes inside the viewport when the content is scrolled sideways", () => {
    expect(readReadingAnchor(view(50_000, 5_000, -400_000))?.pos).toBe(500);
  });
});
