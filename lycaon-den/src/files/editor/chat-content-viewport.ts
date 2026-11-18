import type { Text } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { topEdgePoint, type ScrollAnchor } from "../../components/source/editor/editor-settled-scroll.ts";
import type { ChatContentMatch } from "../../chat/transcript/content/chat-content-reader.ts";
import { RuneOffsets } from "../../chat/transcript/content/rune-offsets.ts";

export type ChatReadingPlace = Omit<ScrollAnchor, "pos"> & { target: ChatContentMatch };

/** The visible text at the viewport edge, including a partially clipped line. */
export function topTextAnchor(view: EditorView): ScrollAnchor | undefined {
  const port = view.scrollDOM.getBoundingClientRect();
  if (!view.dom.isConnected || port.height <= 0) return undefined;
  const pos = view.posAtCoords(topEdgePoint(view), false);
  const rect = view.coordsAtPos(pos);
  return rect ? { pos, offset: rect.top - port.top, left: rect.left - port.left } : undefined;
}

export function inlineReadingPlace(view: EditorView, receipt: boolean): ChatReadingPlace | undefined {
  const anchor = topTextAnchor(view);
  if (!anchor) return undefined;
  const { doc } = view.state;
  const { pos, ...geometry } = anchor;
  if (!receipt) return { ...geometry, target: { offset: RuneOffsets.of(doc.toString()).toRune(pos), length: 0 } };
  const trailingBreak = pos === doc.length && doc.lines > 1 && doc.line(doc.lines).length === 0;
  const line = doc.lineAt(trailingBreak ? pos - 1 : pos);
  const text = doc.sliceString(line.from, Math.min(doc.length, line.to + 1));
  return { ...geometry, target: { row: line.number - 1, column: RuneOffsets.of(text).toRune(pos - line.from), length: 0 } };
}

export function inlineRange(doc: Text, match: ChatContentMatch): { anchor: number; head: number } {
  const line = "row" in match ? doc.line(Math.min(doc.lines, match.row + 1)) : undefined;
  const offsets = RuneOffsets.of(line ? doc.sliceString(line.from, Math.min(doc.length, line.to + 1)) : doc.toString());
  const start = "row" in match ? match.column : match.offset;
  const base = line?.from ?? 0;
  return { anchor: base + offsets.toUtf16(start), head: base + offsets.toUtf16(start + match.length) };
}
