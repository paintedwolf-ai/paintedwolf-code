import { editorIndentStylePref, editorIndentWidthPref } from "../../settings/editor/editor-prefs.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import { type EditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import { indentInfoFromEditorConfig } from "../../components/source/editor/editorconfig.ts";
import type { IndentInfo } from "../../components/source/editor/indent-detect.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";

export function currentDisplayPrefs(buf: FileBuffer): EditorDisplayPrefs {
  return editorDisplayPrefs(effectiveIndent(buf));
}

export function effectiveIndent(buf: FileBuffer): IndentInfo {
  return (
    buf.indentOverride ??
    indentInfoFromEditorConfig(buf.editorConfig) ??
    buf.detectedIndent ?? {
      style: editorIndentStylePref(),
      width: editorIndentWidthPref(),
    }
  );
}
