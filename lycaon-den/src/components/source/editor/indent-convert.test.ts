import { describe, expect, it } from "vitest";
import {
  convertIndentationText,
  leadingWhitespaceToSpaces,
  leadingWhitespaceToTabs,
} from "./indent-convert.ts";

describe("indent convert", () => {
  it("expands leading tabs to spaces", () => {
    expect(leadingWhitespaceToSpaces("\tfoo", 4)).toBe("    foo");
    expect(leadingWhitespaceToSpaces("  \tbar", 4)).toBe("    bar");
  });

  it("collapses leading spaces to tabs", () => {
    expect(leadingWhitespaceToTabs("        foo", 4)).toBe("\t\tfoo");
    expect(leadingWhitespaceToTabs("      foo", 4)).toBe("\t  foo");
  });

  it("converts a whole document", () => {
    const src = "\tline\n\t\tinner\n";
    expect(
      convertIndentationText(src, { style: "spaces", width: 4 }, 4),
    ).toBe("    line\n        inner\n");
    expect(
      convertIndentationText(
        "    line\n        inner\n",
        { style: "tabs", width: 4 },
        4,
      ),
    ).toBe("\tline\n\t\tinner\n");
  });

  it("limits conversion to a line range", () => {
    const src = "\ta\n\tb\n\tc\n";
    expect(
      convertIndentationText(
        src,
        { style: "spaces", width: 4 },
        4,
        { fromLine: 2, toLine: 2 },
      ),
    ).toBe("\ta\n    b\n\tc\n");
  });
});
