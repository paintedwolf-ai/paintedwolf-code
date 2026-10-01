import { describe, expect, it } from "vitest";
import {
  normalizeLineRange,
  pathFileChipLabel,
  samePathFileRange,
  SELECTION_VERB_TEMPLATES,
} from "./path-file-ref.ts";

describe("path-file-ref", () => {
  it("normalizes inverted ranges", () => {
    expect(normalizeLineRange(34, 12)).toEqual({ startLine: 12, endLine: 34 });
  });

  it("labels chips with an en-dash range", () => {
    expect(
      pathFileChipLabel({
        path: "src/main.ts",
        name: "main.ts",
        startLine: 12,
        endLine: 34,
      }),
    ).toBe("main.ts:12–34");
  });

  it("prefixes multi-root chip labels", () => {
    expect(
      pathFileChipLabel({
        path: "a.ts",
        name: "a.ts",
        rootLabel: "app",
        multiRoot: true,
        startLine: 2,
        endLine: 2,
      }),
    ).toBe("@app/a.ts:2");
  });

  it("treats exact same path+range as identical", () => {
    const a = {
      projectId: "p",
      rootId: "r",
      path: "a.ts",
      startLine: 2,
      endLine: 4,
    };
    expect(samePathFileRange(a, { ...a })).toBe(true);
    expect(samePathFileRange(a, { ...a, endLine: 5 })).toBe(false);
    expect(
      samePathFileRange(a, {
        projectId: "p",
        rootId: "r",
        path: "a.ts",
      }),
    ).toBe(false);
  });

  it("locks verb template copy", () => {
    expect(SELECTION_VERB_TEMPLATES).toEqual({
      explain: "Explain this.",
      improve: "Improve this.",
      addTest: "Add a test covering this.",
    });
  });
});
