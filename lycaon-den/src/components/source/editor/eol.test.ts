import { describe, expect, it } from "vitest";
import {
  analyzeEol,
  defaultEol,
  normalizeEolForEditor,
} from "./eol.ts";

describe("eol", () => {
  it("detects lf and crlf by majority", () => {
    expect(analyzeEol("a\nb\n").eol).toBe("lf");
    expect(analyzeEol("a\r\nb\r\n").eol).toBe("crlf");
    expect(analyzeEol("a\r\nb\nc\n").eol).toBe("lf");
    expect(analyzeEol("").eol).toBe(defaultEol());
  });

  it("uses the native platform default for files without line endings", () => {
    expect(defaultEol("windows")).toBe("crlf");
    expect(defaultEol("macos")).toBe("lf");
    expect(defaultEol("linux")).toBe("lf");
  });

  it("marks mixed and legacy CR endings instead of hiding the conversion", () => {
    expect(analyzeEol("a\r\nb\nc\r")).toEqual({ eol: "lf", mixed: true });
  });

  it("flags a bare-CR-only file even though it has just one kind", () => {
    expect(analyzeEol("a\rb\rc\r")).toEqual({ eol: "lf", mixed: true });
  });

  it("normalizes to LF for the editor", () => {
    const { text, eol } = normalizeEolForEditor("a\r\nb\r\n");
    expect(eol).toBe("crlf");
    expect(text).toBe("a\nb\n");
  });
});
