import { describe, expect, it } from "vitest";
import type { SourceDefinitionCandidate } from "../../api/types.ts";
import {
  DEFINITION_PENDING_NOTICE_DELAY_MS,
  definitionPickerExplainer,
  DEFINITION_PICKER_TRUNCATION_FOOTER,
  definitionJumpOutcome,
  definitionMissCopy,
  definitionPendingCopy,
  definitionPickerHeader,
} from "./definition-jump.ts";

const cand = (
  path: string,
  line: number,
): SourceDefinitionCandidate => ({
  root_id: "r1",
  path,
  line,
  kind: "function",
  snippet: `func ${path}`,
});

describe("definition-jump", () => {
  it("empty symbols are a no-op", () => {
    expect(definitionJumpOutcome("  ", { candidates: [], truncated: false })).toEqual({
      kind: "noop",
    });
  });

  it("one candidate jumps; many open picker; zero stays in the editor", () => {
    expect(
      definitionJumpOutcome("Foo", {
        candidates: [cand("a.go", 1)],
        truncated: false,
      }),
    ).toEqual({ kind: "jump", candidate: cand("a.go", 1) });

    const many = definitionJumpOutcome("Foo", {
      candidates: [cand("a.go", 1), cand("b.go", 2)],
      truncated: true,
    });
    expect(many.kind).toBe("picker");
    if (many.kind === "picker") {
      expect(many.candidates).toHaveLength(2);
      expect(many.truncated).toBe(true);
      expect(many.symbol).toBe("Foo");
    }

    expect(
      definitionJumpOutcome("Foo", { candidates: [], truncated: false }),
    ).toEqual({ kind: "miss", symbol: "Foo", truncated: false });
  });

  it("keeps a single result from an incomplete lookup as an explicit choice", () => {
    expect(definitionJumpOutcome("Foo", { candidates: [cand("a.go", 1)], truncated: true })).toEqual({
      kind: "picker", symbol: "Foo", candidates: [cand("a.go", 1)], truncated: true,
    });
    expect(definitionPickerHeader("Foo", 1)).toBe("1 definition of Foo");
    expect(definitionPickerExplainer(true)).not.toContain("more than one place");
  });

  it("copy never overclaims", () => {
    expect(DEFINITION_PICKER_TRUNCATION_FOOTER).toMatch(/incomplete/i);
    expect(definitionMissCopy("Resolve", false)).toBe(
      "No definition found for Resolve",
    );
    expect(definitionMissCopy("Resolve", true)).toBe(
      "No definition found for Resolve in the files searched; the lookup was incomplete",
    );
  });

  it("picker copy frames the list as ambiguity, never as failure", () => {
    expect(definitionPickerHeader("Resolve", 3)).toBe("3 definitions of Resolve");
    expect(definitionPickerExplainer(false)).toBe(
      "This name is declared in more than one place — pick the one you meant.",
    );
    for (const copy of [
      definitionPickerHeader("Resolve", 3),
      definitionPickerExplainer(false),
    ]) {
      expect(copy).not.toMatch(/fail|error|couldn|wrong|unable/i);
    }
  });

  it("pending copy names the symbol; the grace keeps fast resolves silent", () => {
    expect(definitionPendingCopy("Resolve")).toBe(
      "Finding definition of Resolve…",
    );
    // The grace hides cached responses and acknowledges longer waits.
    expect(DEFINITION_PENDING_NOTICE_DELAY_MS).toBeGreaterThan(50);
    expect(DEFINITION_PENDING_NOTICE_DELAY_MS).toBeLessThanOrEqual(300);
  });
});
