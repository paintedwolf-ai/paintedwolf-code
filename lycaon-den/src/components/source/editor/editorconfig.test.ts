import { describe, expect, it } from "vitest";
import {
  editorConfigPropsFromHost,
  indentInfoFromEditorConfig,
} from "./editorconfig.ts";

describe("editorConfigPropsFromHost", () => {
  it("maps declared host pairs onto buffer props", () => {
    expect(
      editorConfigPropsFromHost({
        path: "a.go",
        root_id: "r1",
        indent_style: "tab",
        indent_size_tab: true,
        tab_width: 8,
        end_of_line: "crlf",
        trim_trailing_whitespace: false,
        insert_final_newline: true,
        charset: "utf-8-bom",
      }),
    ).toEqual({
      indentStyle: "tabs",
      indentSizeIsTab: true,
      tabWidth: 8,
      endOfLine: "crlf",
      trimTrailingWhitespace: false,
      insertFinalNewline: true,
      charset: "utf-8-bom",
    });
  });

  it("returns undefined when nothing is declared", () => {
    expect(editorConfigPropsFromHost({ path: "a.go", root_id: "r1" })).toBeUndefined();
  });
});

describe("indentInfoFromEditorConfig", () => {
  it("maps space and tab styles with separate tab_width", () => {
    expect(
      indentInfoFromEditorConfig({ indentStyle: "spaces", indentSize: 2 }),
    ).toEqual({ style: "spaces", width: 2 });
    expect(
      indentInfoFromEditorConfig({
        indentStyle: "tabs",
        indentSize: 4,
        tabWidth: 8,
      }),
    ).toEqual({ style: "tabs", width: 4, tabWidth: 8 });
  });
});
