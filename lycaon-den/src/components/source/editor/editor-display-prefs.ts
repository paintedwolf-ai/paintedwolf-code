import {
  editorFontFamilyPref, editorFontSizePref, editorIndentGuidesPref, editorIndentStylePref,
  editorIndentWidthPref, editorLineHeightPref, editorLineNumbersPref, editorShownScrollbarTicks,
  editorLineLayoutPref, editorWhitespacePref,
} from "../../../settings/editor/editor-prefs.ts";
import type { EditorDisplayPrefs } from "./codemirror-theme.ts";
import type { IndentInfo } from "./indent-detect.ts";

export function editorDisplayPrefs(indent?: IndentInfo): EditorDisplayPrefs {
  return {
    // Pretty wraps whatever it does not lay out.
    wordWrap: editorLineLayoutPref() !== "off", lineNumbers: editorLineNumbersPref(), fontSize: editorFontSizePref(),
    fontFamily: editorFontFamilyPref(), lineHeight: editorLineHeightPref(), indentGuides: editorIndentGuidesPref(),
    whitespace: editorWhitespacePref(), scrollbarTicks: editorShownScrollbarTicks(),
    indent: indent ?? { style: editorIndentStylePref(), width: editorIndentWidthPref() },
  };
}
