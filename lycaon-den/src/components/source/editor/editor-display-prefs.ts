import {
  editorFontFamilyPref, editorFontSizePref, editorIndentGuidesPref, editorIndentStylePref,
  editorIndentWidthPref, editorLineHeightPref, editorLineNumbersPref, editorShownScrollbarTicks,
  editorWhitespacePref, editorWordWrapPref,
} from "../../../settings/editor/editor-prefs.ts";
import type { EditorDisplayPrefs } from "./codemirror-theme.ts";
import type { IndentInfo } from "./indent-detect.ts";

export function editorDisplayPrefs(indent?: IndentInfo): EditorDisplayPrefs {
  return {
    wordWrap: editorWordWrapPref(), lineNumbers: editorLineNumbersPref(), fontSize: editorFontSizePref(),
    fontFamily: editorFontFamilyPref(), lineHeight: editorLineHeightPref(), indentGuides: editorIndentGuidesPref(),
    whitespace: editorWhitespacePref(), scrollbarTicks: editorShownScrollbarTicks(),
    indent: indent ?? { style: editorIndentStylePref(), width: editorIndentWidthPref() },
  };
}
