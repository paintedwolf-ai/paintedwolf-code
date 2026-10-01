/** EditorConfig pairs the host resolved for a buffer. */

import type { SourceEditorConfig } from "../../../api/types.ts";
import type { IndentInfo } from "./indent-detect.ts";
import type { EolKind } from "./eol.ts";

export type EditorConfigProps = {
  indentStyle?: "spaces" | "tabs";
  /** Columns per indent level; omitted when indent_size is `tab`. */
  indentSize?: number;
  /** True when indent_size = tab (indent with hard tabs). */
  indentSizeIsTab?: boolean;
  tabWidth?: number;
  endOfLine?: EolKind;
  trimTrailingWhitespace?: boolean;
  insertFinalNewline?: boolean;
  /** Charset token (utf-8, utf-8-bom, utf-16le, …). */
  charset?: string;
};

/** Maps the host resolution onto buffer props; undefined when nothing is declared. */
export function editorConfigPropsFromHost(
  resolved: SourceEditorConfig,
): EditorConfigProps | undefined {
  const props: EditorConfigProps = {};
  if (resolved.indent_style === "space") props.indentStyle = "spaces";
  if (resolved.indent_style === "tab") props.indentStyle = "tabs";
  if (resolved.indent_size_tab) props.indentSizeIsTab = true;
  else if (resolved.indent_size != null) {
    props.indentSize = resolved.indent_size;
    props.indentSizeIsTab = false;
  }
  if (resolved.tab_width != null) props.tabWidth = resolved.tab_width;
  if (resolved.end_of_line) props.endOfLine = resolved.end_of_line;
  if (resolved.trim_trailing_whitespace != null) {
    props.trimTrailingWhitespace = resolved.trim_trailing_whitespace;
  }
  if (resolved.insert_final_newline != null) {
    props.insertFinalNewline = resolved.insert_final_newline;
  }
  if (resolved.charset) props.charset = resolved.charset;
  return editorConfigIsActive(props) ? props : undefined;
}

/**
 * Indent for Tab and auto-indent when EditorConfig sets one; undefined otherwise.
 * `tabWidth` is carried separately when it differs from indent size.
 */
export function indentInfoFromEditorConfig(
  props: EditorConfigProps | undefined,
): IndentInfo | undefined {
  if (!props) return undefined;
  const style =
    props.indentStyle ??
    (props.indentSizeIsTab
      ? "tabs"
      : props.indentSize != null
        ? "spaces"
        : undefined);
  if (!style) return undefined;
  if (style === "tabs") {
    // indent_size = tab → size follows tab_width (or editor default 4).
    const tabWidth = props.tabWidth ?? props.indentSize ?? 4;
    const indentSize = props.indentSizeIsTab
      ? tabWidth
      : (props.indentSize ?? tabWidth);
    return {
      style: "tabs",
      width: indentSize,
      tabWidth,
    };
  }
  const width = props.indentSize ?? props.tabWidth ?? 4;
  const tabWidth = props.tabWidth ?? width;
  return {
    style: "spaces",
    width,
    tabWidth: tabWidth !== width ? tabWidth : undefined,
  };
}

/** True when any EditorConfig pair is in effect for this buffer. */
export function editorConfigIsActive(
  props: EditorConfigProps | undefined,
): boolean {
  if (!props) return false;
  return Object.keys(props).length > 0;
}

/** The host encoding named by a charset; `latin1` has none. */
export function editorConfigCharsetToHost(
  charset: string | undefined,
): string | null {
  return charset && charset !== "latin1" ? charset : null;
}
