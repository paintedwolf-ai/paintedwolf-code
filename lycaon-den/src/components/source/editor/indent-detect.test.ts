import { describe, expect, it } from "vitest";
import { detectIndent } from "./indent-detect.ts";

describe("detectIndent", () => {
  it("detects 2-space indent", () => {
    expect(
      detectIndent("a\n  b\n  c\n    d\n"),
    ).toEqual({ style: "spaces", width: 2 });
  });

  it("detects 4-space indent", () => {
    expect(
      detectIndent("fn main() {\n    let x = 1;\n    let y = 2;\n}\n"),
    ).toEqual({ style: "spaces", width: 4 });
  });

  it("detects tabs", () => {
    expect(detectIndent("a\n\tb\n\tc\n\t\td\n")).toEqual({
      style: "tabs",
      width: 4,
    });
  });

  it("picks the majority style when mixed", () => {
    expect(
      detectIndent("a\n  b\n  c\n  d\n\te\n"),
    ).toEqual({ style: "spaces", width: 2 });
  });

  it("returns undefined for empty / undecided input", () => {
    expect(detectIndent("")).toBeUndefined();
    expect(detectIndent("no indent here\nstill none\n")).toBeUndefined();
  });
});
