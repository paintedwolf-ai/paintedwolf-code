import type { EditorView } from "@codemirror/view";
import { emphasizeAndScrollToLine } from "../../components/source/editor/codemirror-theme.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { mapFilesCollaborationSourceLineColToDisplay } from "./files-editor-collaboration.ts";

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
    const mappedStart = mapFilesCollaborationSourceLineColToDisplay(
      view,
      buffer.revealLine,
      buffer.revealColumn ?? 1,
    );
    const mappedEnd =
      buffer.revealEndLine != null
        ? mapFilesCollaborationSourceLineColToDisplay(view, buffer.revealEndLine, 1)
        : undefined;
    emphasizeAndScrollToLine(
      view,
      mappedStart.line,
      mappedEnd?.line,
      buffer.revealColumn != null ? mappedStart.col : undefined,
    );
  }
  if (buffer.revealFocus) view.focus();
}
