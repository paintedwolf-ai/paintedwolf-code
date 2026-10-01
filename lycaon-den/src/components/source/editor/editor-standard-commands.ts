import { moveLineUp, moveLineDown, copyLineUp, copyLineDown, deleteLine, selectLine, selectParentSyntax, cursorMatchingBracket, toggleComment, toggleBlockComment, indentMore, indentLess, indentSelection, insertBlankLine, selectAll } from "@codemirror/commands";
import { foldCode, unfoldCode, foldAll, unfoldAll } from "@codemirror/language";
import { startCompletion } from "@codemirror/autocomplete";
import type { EditorView } from "@codemirror/view";

export const EDITOR_STANDARD_COMMANDS = {
  "editor.moveLineUp": moveLineUp,
  "editor.moveLineDown": moveLineDown,
  "editor.copyLineUp": copyLineUp,
  "editor.copyLineDown": copyLineDown,
  "editor.deleteLine": deleteLine,
  "editor.selectLine": selectLine,
  "editor.selectParentSyntax": selectParentSyntax,
  "editor.cursorMatchingBracket": cursorMatchingBracket,
  "editor.toggleComment": toggleComment,
  "editor.toggleBlockComment": toggleBlockComment,
  "editor.indentMore": indentMore,
  "editor.indentLess": indentLess,
  "editor.indentSelection": indentSelection,
  "editor.insertBlankLine": insertBlankLine,
  "editor.selectAll": selectAll,
  "editor.foldCode": foldCode,
  "editor.unfoldCode": unfoldCode,
  "editor.foldAll": foldAll,
  "editor.unfoldAll": unfoldAll,
  "editor.startCompletion": startCompletion,
} satisfies Record<string, (view: EditorView) => boolean>;
