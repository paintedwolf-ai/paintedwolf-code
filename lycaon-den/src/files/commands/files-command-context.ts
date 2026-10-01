import type { EditorView } from "@codemirror/view";
import type { CommandInvokeContext } from "../../api/types.ts";
import type { ShellEditorFacts } from "../../contributions/shell-facts.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { editorSymbolTarget } from "../editor/editor-symbol-target.ts";
import { findingOverlappingRange } from "../editor/files-editor-findings.ts";

/** Command coordinates follow the visible editor selection. */
export function filesCommandContext(buffer: Pick<FileBuffer, "key" | "rootId" | "path" | "documentRevision" | "writable" | "language">, view: EditorView): {
  facts: ShellEditorFacts;
  context: CommandInvokeContext;
} {
  const selection = view.state.selection.main;
  const startLine = view.state.doc.lineAt(selection.from).number;
  // The exclusive selection end omits a line whose start is the endpoint.
  const endLine = view.state.doc.lineAt(selection.empty ? selection.to : selection.to - 1).number;
  const target = editorSymbolTarget(view.state, { source: "caret" });
  const symbol = target.kind === "symbol" ? target.symbol : null;
  const findingId = findingOverlappingRange(buffer.key, startLine, endLine)?.fingerprints.primary ?? null;
  return {
    facts: {
      active: true,
      editable: !view.state.readOnly && buffer.writable !== false,
      hasSelection: !selection.empty,
      symbol,
      findingId,
      language: buffer.language?.path === buffer.path ? buffer.language.name : null,
    },
    context: {
      root_id: buffer.rootId,
      path: buffer.path,
      ...(buffer.documentRevision != null ? { document_revision: buffer.documentRevision } : {}),
      start_line: startLine,
      ...(!selection.empty ? { end_line: endLine } : {}),
      ...(symbol ? { symbol } : {}),
      ...(findingId ? { finding_id: findingId } : {}),
    },
  };
}
