// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { ReaderEditor } from "./source-reader-editor.ts";
import { ReaderSplit } from "./source-reader-split.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "../../../platform/scrolling/scrollport-motion-types.ts";
import { readerGap, type ReaderSlot, MAIN_SECTION } from "./source-reader-document.ts";
import { editorDisplayPrefs } from "../editor/editor-display-prefs.ts";
import { clearEditorViewportPool } from "../editor/editor-viewport-pool.ts";

vi.mock("../../../utils/clipboard.ts", () => ({ copyTextToClipboard: vi.fn() }));
const cleanup: (() => void)[] = [];
afterEach(() => { for (const close of cleanup.splice(0)) close(); clearEditorViewportPool(); });
const row = (index: number, text = "source line\n"): ReaderSlot => ({ index, end: index + 1, text, kind: "equal", before_line: index + 1, after_line: index + 1, changed: [] });
function fixture(viewportInput?: (editor: ReaderEditor) => void) {
  const parent = document.createElement("div"); document.body.append(parent);
  const selection = vi.fn(async () => "selected source"), content = vi.fn(async () => "full source");
  const editor = new ReaderEditor({ parent, surface: "reader", prefs: editorDisplayPrefs(), access: () => ({ selection, content }),
    load: vi.fn(), error: vi.fn(), fullSide: () => "after", changes: () => false,
    facts: vi.fn(), hideFacts: vi.fn(), viewportInput });
  cleanup.push(() => { editor.destroy(); parent.remove(); });
  const copy = () => editor.view.contentDOM.dispatchEvent(new Event("copy", { bubbles: true, cancelable: true }));
  return { editor, selection, copy };
}

it.each([false, true])("keeps exact source selection through eviction and reload (reversed=%s)", reversed => {
  const { editor, selection, copy } = fixture();
  const visible = [readerGap(0, 100, true), row(100, "hello 🙂 world\n"), row(101), readerGap(102, 10_000_000, true)];
  editor.setRows(visible, true);
  const from = editor.document.rowAt(MAIN_SECTION, 100)!.from + 6, to = editor.document.rowAt(MAIN_SECTION, 101)!.from + 4;
  editor.view.dispatch({ selection: { anchor: reversed ? to : from, head: reversed ? from : to } });
  editor.setRows([readerGap(0, 8_000_000, true), row(8_000_000), readerGap(8_000_001, 10_000_000, true)]);
  expect(editor.document.text.length).toBeLessThan(30);
  copy();
  expect(selection).toHaveBeenLastCalledWith({ section: MAIN_SECTION, row: 100, offset: 6, side: "after" }, { section: MAIN_SECTION, row: 101, offset: 4, side: "after" });
  editor.setRows(visible);
  expect(editor.view.state.selection.main.anchor).toBe(reversed ? to : from);
  expect(editor.view.state.selection.main.head).toBe(reversed ? from : to);
});

it("extends a selection into a distant page while preserving its evicted anchor", () => {
  const { editor, selection, copy } = fixture();
  editor.setRows([row(0), readerGap(1, 10_000_000, true)], true);
  editor.view.dispatch({ selection: { anchor: 2, head: 6 } });
  editor.setRows([readerGap(0, 9_000_000, true), row(9_000_000), readerGap(9_000_001, 10_000_000, true)]);
  editor.view.dispatch({ selection: { anchor: editor.view.state.selection.main.anchor, head: editor.document.rowAt(MAIN_SECTION, 9_000_000)!.from + 3 } });
  copy();
  expect(selection).toHaveBeenLastCalledWith({ section: MAIN_SECTION, row: 0, offset: 2, side: "after" }, { section: MAIN_SECTION, row: 9_000_000, offset: 3, side: "after" });
});

it("does not turn a caret at a page boundary into a source selection", () => {
  const { editor, selection, copy } = fixture();
  editor.setRows([row(0), row(1)], true);
  editor.view.dispatch({ selection: { anchor: editor.document.rowAt(MAIN_SECTION, 1)!.from } });
  editor.setRows([readerGap(0, 100, true), row(100)]);
  copy();
  expect(selection).not.toHaveBeenCalled();
});

it("restores semantic selection when replacing an editor presentation", () => {
  const previous = fixture(), next = fixture();
  previous.editor.setRows([row(100, "selected source\n")], true);
  previous.editor.view.dispatch({ selection: { anchor: 2, head: 8 } });
  next.editor.setRows([readerGap(0, 100, true), row(100, "selected source\n")], true);
  next.editor.restoreSelection(previous.editor.selection());
  next.copy();
  expect(next.selection).toHaveBeenLastCalledWith({ section: MAIN_SECTION, row: 100, offset: 2, side: "after" }, { section: MAIN_SECTION, row: 100, offset: 8, side: "after" });
});

it("does not let a queued viewport restoration override explicit navigation", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  const measure = vi.spyOn(editor.view, "requestMeasure");
  editor.restoreViewport({ rank: 0, fraction: 0 });
  const restoration = measure.mock.calls.at(-1)![0]!;
  expect(editor.reveal(MAIN_SECTION, 100)).toBe(true);
  editor.view.scrollDOM.scrollTop = 123;
  restoration.write?.(987, editor.view);
  expect(editor.view.scrollDOM.scrollTop).toBe(123);
});

it.each([false, true])("lets newer native input supersede a queued viewport restore (atEnd=%s)", atEnd => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  const measure = vi.spyOn(editor.view, "requestMeasure");
  editor.restoreViewport({ rank: 50, fraction: 0.5, atEnd });
  const restoration = measure.mock.calls.at(-1)![0]!;
  editor.view.dom.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
  expect(editor.viewportInputPending).toBe(true);
  editor.view.scrollDOM.scrollTop = 333;
  editor.view.scrollDOM.dispatchEvent(new Event("scroll"));
  restoration.write?.(987, editor.view);
  expect(editor.view.scrollDOM.scrollTop).toBe(333);
  expect(editor.viewportInputPending).toBe(false);
});

it("does not queue a page restore between viewport intent and its native scroll", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  editor.view.dom.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
  editor.view.scrollDOM.dispatchEvent(new Event("scroll"));
  expect(editor.viewportInputPending).toBe(true);
  const measure = vi.spyOn(editor.view, "requestMeasure");
  editor.restoreViewport({ rank: 100, fraction: 0, atEnd: true });
  expect(measure).not.toHaveBeenCalled();
  editor.view.scrollDOM.scrollTop = 300;
  editor.view.scrollDOM.dispatchEvent(new Event("scroll"));
  measure.mockClear();
  editor.restoreViewport({ rank: 50, fraction: 0.5 });
  expect(measure).toHaveBeenCalledOnce();
});

it("keeps layout and reflected scroll observations subordinate to page restoration", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  const measure = vi.spyOn(editor.view, "requestMeasure");
  editor.restoreViewport({ rank: 50, fraction: 0.5 });
  const restoration = measure.mock.calls.at(-1)![0]!;
  editor.view.scrollDOM.scrollTop = 300;
  editor.view.scrollDOM.dispatchEvent(new Event("scroll"));
  restoration.write?.(987, editor.view);
  expect(editor.view.scrollDOM.scrollTop).toBe(987);
});

it("gives newer split-pane input priority over both pending restorations", () => {
  const input = (editor: ReaderEditor) => split.input(editor);
  const before = fixture(input).editor, after = fixture(input).editor;
  const split = new ReaderSplit(before, after);
  cleanup.push(() => split.destroy());
  const measures = [before, after].map(editor => {
    editor.setRows([readerGap(0, 100, true), row(100)], true);
    return vi.spyOn(editor.view, "requestMeasure");
  });
  split.restoreViewport({ rank: 100, fraction: 0, atEnd: true });
  const restorations = measures.map(measure => measure.mock.calls.at(-1)![0]!);
  after.view.dom.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
  before.view.scrollDOM.scrollTop = 250;
  split.scroll(before);
  split.viewportAnchor();
  const restoreBefore = vi.spyOn(before, "restoreViewport"), restoreAfter = vi.spyOn(after, "restoreViewport");
  split.restoreViewport({ rank: 100, fraction: 0, atEnd: true });
  expect(restoreBefore).not.toHaveBeenCalled(); expect(restoreAfter).not.toHaveBeenCalled();
  after.view.scrollDOM.scrollTop = 300;
  after.view.scrollDOM.dispatchEvent(new Event("scroll"));
  split.scroll(after);
  for (const [index, editor] of [before, after].entries()) {
    restorations[index]!.write?.(987, editor.view);
    expect(editor.view.scrollDOM.scrollTop).toBe(300);
  }
});

it("preserves the within-row offset while the viewport crosses an unloaded range", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  const block = editor.view.lineBlockAtHeight(0);
  expect(block.height).toBeGreaterThan(0);
  vi.spyOn(editor.view, "documentTop", "get").mockReturnValue(0);
  vi.spyOn(editor.view.scrollDOM, "getBoundingClientRect").mockReturnValue(new DOMRect(0, block.top + block.height * 0.505, 100, 100));
  const anchor = editor.viewportAnchor();
  expect(anchor?.rank).toBe(50);
  expect(anchor?.fraction).toBeCloseTo(0.5);
});

it("keeps the exact restored anchor until the reader scrolls again", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  let scroll = 0;
  Object.defineProperty(editor.view.scrollDOM, "scrollTop", { configurable: true, get: () => scroll, set: value => { scroll = Math.round(value); } });
  const measure = vi.spyOn(editor.view, "requestMeasure");
  const anchor = { rank: 50, fraction: 0.37 };
  editor.restoreViewport(anchor);
  expect(editor.viewportAnchor()).toEqual(anchor);
  const restoration = measure.mock.calls.at(-1)![0]!;
  restoration.write?.(123.45, editor.view);
  expect(scroll).toBe(123);
  expect(editor.viewportAnchor()).toEqual(anchor);
  editor.view.scrollDOM.scrollTop = 0;
  expect(editor.viewportAnchor()).not.toEqual(anchor);
});

it.each([false, true])("preserves interior or end affinity when unloaded rows materialize (atEnd=%s)", atEnd => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100, true), row(100)], true);
  const viewport = editor.view.scrollDOM;
  let height = 1_000;
  Object.defineProperties(viewport, {
    clientHeight: { configurable: true, value: 100 },
    scrollHeight: { configurable: true, get: () => height },
  });
  const block = editor.view.lineBlockAtHeight(0);
  vi.spyOn(editor.view, "documentTop", "get").mockReturnValue(0);
  vi.spyOn(viewport, "getBoundingClientRect").mockReturnValue(new DOMRect(0, block.top + block.height * 0.505, 100, 100));
  viewport.scrollTop = atEnd ? 900 : 500;
  const anchor = editor.viewportAnchor()!;
  expect(anchor.rank).toBe(50);
  expect(anchor.atEnd === true).toBe(atEnd);
  editor.setRows(Array.from({ length: 101 }, (_, index) => row(index)));
  height = 1_020;
  const measure = vi.spyOn(editor.view, "requestMeasure");
  editor.restoreViewport(anchor);
  const restoration = measure.mock.calls.at(-1)![0]!;
  const top = restoration.read?.(editor.view);
  restoration.write?.(top, editor.view);
  if (atEnd) expect(viewport.scrollTop).toBe(920);
  else {
    expect(viewport.scrollTop).not.toBe(920);
    expect(editor.viewportAnchor()).toEqual(anchor);
  }
});

it.each([
  { side: "before", atEnd: false }, { side: "before", atEnd: true },
  { side: "after", atEnd: false }, { side: "after", atEnd: true },
] as const)("shares the $side pane's viewport across page materialization (atEnd=$atEnd)", ({ side, atEnd }) => {
  const before = fixture().editor, after = fixture().editor;
  const split = new ReaderSplit(before, after);
  cleanup.push(() => split.destroy());
  let height = 1_000;
  for (const editor of [before, after]) {
    editor.setRows([readerGap(0, 100, true, 100), row(100)], true);
    Object.defineProperties(editor.view.scrollDOM, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, get: () => height },
    });
    vi.spyOn(editor.view, "documentTop", "get").mockImplementation(() => -editor.view.scrollDOM.scrollTop);
    vi.spyOn(editor.view.scrollDOM, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 100, 100));
  }
  const source = side === "before" ? before : after;
  source.view.scrollDOM.scrollTop = atEnd ? 900 : 500;
  // The page can arrive before the native scroll event synchronizes the other pane.
  const anchor = split.viewportAnchor()!;
  expect(anchor.atEnd === true).toBe(atEnd);
  const measures = [before, after].map(editor => vi.spyOn(editor.view, "requestMeasure"));
  for (const editor of [before, after]) editor.setRows(Array.from({ length: 101 }, (_, index) => row(index)));
  height = 1_020;
  split.restoreViewport(anchor);
  for (const [index, editor] of [before, after].entries()) {
    const restoration = measures[index]!.mock.calls.at(-1)![0]!;
    restoration.write?.(restoration.read?.(editor.view), editor.view);
    split.scroll(editor);
  }
  for (const editor of [before, after]) {
    expect(editor.viewportAnchor()).toEqual(anchor);
    if (atEnd) expect(editor.view.scrollDOM.scrollTop).toBe(920);
    else expect(editor.view.scrollDOM.scrollTop).not.toBe(920);
  }
  expect(before.view.scrollDOM.scrollTop).toBe(after.view.scrollDOM.scrollTop);
});

it("keeps text and unloaded-range metrics independent of split alignment space", () => {
  const { editor } = fixture();
  const first = row(0, "const before0 = '0';\n");
  editor.setRows([first, readerGap(1, 1500, true, 1499)], true);
  const firstEntry = editor.document.rowAt(MAIN_SECTION, 0)!;
  const gapEntry = editor.document.entryForSlot(MAIN_SECTION, 1)!;
  const textHeight = editor.view.lineBlockAt(firstEntry.from).height;
  const gapHeight = editor.view.lineBlockAt(gapEntry.from).height;
  const fontHeight = editor.view.defaultLineHeight;
  editor.align(new Map([[firstEntry.from, 117]]));
  const space = editor.view.contentDOM.querySelector<HTMLElement>(".cm-den-reader-alignment")!;
  expect(space.parentElement).toBe(editor.view.contentDOM);
  expect(space.style.height).toBe("117px");
  expect(editor.view.lineBlockAt(firstEntry.from).height).toBe(textHeight + 117);
  expect(editor.view.lineBlockAt(gapEntry.from).height).toBe(gapHeight);
  expect(editor.view.defaultLineHeight).toBe(fontHeight);
  editor.setRows([first, ...Array.from({ length: 1499 }, (_, index) => row(index + 1))]);
  expect(editor.view.lineBlockAt(editor.document.rowAt(MAIN_SECTION, 1499)!.from).height).toBe(textHeight);
  expect(editor.view.defaultLineHeight).toBe(fontHeight);
});

it("gives the unloaded-range chip a block of its own to be measured by", () => {
  const { editor } = fixture();
  editor.setRows([readerGap(0, 100), row(100)], true);
  // Only the returned element's border box reaches the height map, so the chip's
  // spacing has to sit inside it for the gutter to stay on its lines.
  const chip = editor.view.contentDOM.querySelector(".cm-den-reader-gap");
  const block = editor.view.contentDOM.querySelector(".cm-den-reader-gap-row");
  expect(chip?.parentElement).toBe(block);
  expect(block?.parentElement).toBe(editor.view.contentDOM);
});

it("recycles its viewport so a row scrolling into view builds no new editor", () => {
  const first = fixture();
  first.editor.setRows([row(0), row(1)], true);
  const recycled = first.editor.view;
  first.editor.destroy();

  const second = fixture();
  expect(second.editor.view).toBe(recycled);
  // A reused viewport carries none of the rows it last showed.
  expect(second.editor.document.text).toBe("");
});

it("asks for no measure of its own when a moved viewport has nothing left to read", () => {
  const { editor } = fixture();
  // When all rows are loaded, scrolling needs no unread-range scan.
  editor.setRows([row(0), row(1), row(2)], true);
  const asked = vi.spyOn(editor.view, "requestMeasure");
  // Count only the reader's scheduled scan, not the underlying editor measures.
  const scans = () => asked.mock.calls.filter(([request]) =>
    (request as { key?: unknown } | undefined)?.key === editor).length;

  editor.view.dispatch({ selection: { anchor: 0 } });
  editor.view.scrollDOM.dispatchEvent(new Event("scroll"));
  expect(scans()).toBe(0);

  // A document that gained an unread range does have something to find.
  editor.setRows([row(0), readerGap(1, 5_000, true, 5_000)]);
  expect(scans()).toBeGreaterThan(0);
});

it("applies the reloading class to lines whose slot is reloading", () => {
  const { editor } = fixture();
  editor.setRows([{ ...row(0), reloading: true }], true);
  const line = editor.view.contentDOM.querySelector(".cm-line");
  expect(line?.classList.contains("cm-den-reader-reloading")).toBe(true);
});

it("dispatches document changes when placeholder lines shrink even though replacement characters match", () => {
  const { editor } = fixture();
  const largePlaceholder = readerGap(0, 10_000, true, 10_000);
  editor.setRows([largePlaceholder], true);
  expect(editor.document.text).toBe("\uFFFC\n");
  expect(editor.document.entries[0]!.slot.lines).toBe(10_000);

  const smallPlaceholder = readerGap(0, 5, true, 5);
  const dispatchSpy = vi.spyOn(editor.view, "dispatch");
  editor.setRows([smallPlaceholder], false);

  expect(editor.document.entries[0]!.slot.lines).toBe(5);
  expect(dispatchSpy).toHaveBeenCalled();
  const lastCall = dispatchSpy.mock.calls.at(-1)![0] as { changes?: { from: number; to: number; insert: string } };
  expect(lastCall.changes).toEqual({ from: 0, to: 2, insert: "\uFFFC\n" });
});

it("drops a removed tail with the separator before it", () => {
  const { editor } = fixture();
  const a = row(0, "a"), b = row(1, "b");
  editor.setRows([a, b], true);
  expect(editor.view.state.doc.toString()).toBe("a\nb");
  editor.setRows([a]);
  expect(editor.view.state.doc.toString()).toBe("a");
  editor.setRows([a, b]);
  expect(editor.view.state.doc.toString()).toBe("a\nb");
  editor.setRows([]);
  expect(editor.view.state.doc.toString()).toBe("");
});
