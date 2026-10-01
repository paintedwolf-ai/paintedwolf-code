import { describe, expect, it } from "vitest";
import {
  clearSearchRefinements,
  hasSearchRefinements,
  searchRefinementOccurrences,
} from "./search-filter-model.ts";
import { toggleResultType } from "./search-query-model.ts";
import { SEARCH_RESULT_TYPE_BY_ID } from "./search-result-types.ts";

describe("search-filter-model", () => {
  it("detects query and file-pattern refinements", () => {
    expect(hasSearchRefinements("kind:code project:current", "", "")).toBe(
      false,
    );
    expect(
      hasSearchRefinements(
        "kind:code verified:false project:current",
        "",
        "",
      ),
    ).toBe(true);
    expect(hasSearchRefinements("kind:code", "src/**", "")).toBe(true);
    expect(hasSearchRefinements("kind:code", "", "vendor/**")).toBe(true);
    expect(hasSearchRefinements("kind:claim", "", "")).toBe(true);
    expect(hasSearchRefinements("NOT kind:code", "", "")).toBe(true);
  });

  it("clears refinements while preserving result types and scope", () => {
    expect(
      clearSearchRefinements(
        "(kind:code OR kind:message) NOT kind:web kind:claim verified:false source:web project:current",
      ),
    ).toBe("(kind:code OR kind:message) project:current");
  });

  it("treats a kind as a selector only inside its whole family", () => {
    const evidence = toggleResultType("", SEARCH_RESULT_TYPE_BY_ID.evidence);
    expect(hasSearchRefinements(evidence, "", "")).toBe(false);
    expect(hasSearchRefinements("kind:evidence", "", "")).toBe(true);
  });

  it("returns only filters not represented by selectors", () => {
    expect(
      searchRefinementOccurrences(
        "kind:code kind:claim NOT kind:web verified:false project:current",
      ).map((occurrence) => ({
        field: occurrence.field,
        value: occurrence.value,
        negated: occurrence.negated,
      })),
    ).toEqual([
      { field: "kind", value: "claim", negated: false },
      { field: "kind", value: "web", negated: true },
      { field: "verified", value: "false", negated: false },
    ]);
  });
});
