import type { EditorView } from "@codemirror/view";
import { emphasizeAndScrollToLine } from "../../components/source/editor/codemirror-theme.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";

/** A buffer carrying either half of a one-shot arrival target. */
export function hasPendingReveal(buffer: Pick<FileBuffer, "revealLine" | "revealFocus">): boolean {
  return buffer.revealLine != null || !!buffer.revealFocus;
}

/** Places the caret, then hands the editor keyboard attention. */
export function applyBufferReveal(
  view: EditorView,
  buffer: Pick<FileBuffer, "revealLine" | "revealEndLine" | "revealColumn" | "revealFocus">,
): void {
  if (buffer.revealLine != null) {
    emphasizeAndScrollToLine(
      view,
      buffer.revealLine,
      buffer.revealEndLine ?? undefined,
      buffer.revealColumn ?? undefined,
    );
  }
  if (buffer.revealFocus) view.focus();
}
