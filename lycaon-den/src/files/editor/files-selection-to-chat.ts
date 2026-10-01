import type { EditorView } from "@codemirror/view";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import {
  pathFileChipLabel,
  SELECTION_VERB_TEMPLATES,
  normalizeLineRange,
  type SelectionVerb,
} from "../../chat/composer/path-file-ref.ts";
import { fileDisplayName } from "../components/project-files-model.ts";

export type EditorSelectionRange = {
  startLine: number;
  endLine: number;
  empty: boolean;
};

/** 1-based inclusive lines; an empty selection uses the caret line. */
export function editorSelectionRange(view: EditorView): EditorSelectionRange {
  const sel = view.state.selection.main;
  const fromLine = view.state.doc.lineAt(sel.from).number;
  const toLine = view.state.doc.lineAt(sel.to).number;
  const start = Math.min(fromLine, toLine);
  const end = Math.max(fromLine, toLine);
  return { startLine: start, endLine: end, empty: sel.empty };
}

export type AddSelectionToChatArgs = {
  projectId: string;
  rootId: string;
  path: string;
  rootLabel: string;
  multiRoot: boolean;
  view: EditorView | null | undefined;
  /** When set, always attach this range (menu verbs). Otherwise read from view. */
  range?: { startLine: number; endLine: number } | null;
  verb?: SelectionVerb;
};

/**
 * Attach a path-file chip for the selection (or whole file when empty).
 * Optional verb prefills the locked template only when the draft is empty.
 */
export async function addEditorSelectionToChat(
  args: AddSelectionToChatArgs,
): Promise<{ ok: true; attachedRange: boolean } | { ok: false; reason: string }> {
  const rootId = args.rootId.trim();
  const path = args.path.trim();
  if (!rootId || !path) {
    return { ok: false, reason: "No file open." };
  }

  let range = normalizeLineRange(
    args.range?.startLine,
    args.range?.endLine,
  );
  let fromSelection = range != null;
  if (!range && args.view) {
    const sel = editorSelectionRange(args.view);
    if (!sel.empty) {
      range = { startLine: sel.startLine, endLine: sel.endLine };
      fromSelection = true;
    }
  }

  const name = pathFileChipLabel({
    path,
    name: fileDisplayName(path),
    rootLabel: args.rootLabel,
    multiRoot: args.multiRoot,
  });

  const result = await addToChat(
    {
      kind: "path-file",
      projectId: args.projectId,
      rootId,
      path,
      name,
      startLine: fromSelection ? range?.startLine : undefined,
      endLine: fromSelection ? range?.endLine : undefined,
    },
    {
      draftPrefill: args.verb
        ? SELECTION_VERB_TEMPLATES[args.verb]
        : undefined,
    },
  );
  if (!result.ok) return result;

  return { ok: true, attachedRange: fromSelection };
}
