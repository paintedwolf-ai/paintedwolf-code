import { describe, expect, it } from "vitest";
import { highlightSegments, hitTitleSegments, segmentsAtIndexes } from "./search-highlight.ts";

describe("highlightSegments", () => {
  it("returns the whole text unmarked when there are no terms", () => {
    expect(highlightSegments("package store", [])).toEqual([
      { text: "package store", match: false },
    ]);
    expect(highlightSegments("package store", undefined)).toEqual([
      { text: "package store", match: false },
    ]);
  });

  it("marks case-insensitive term occurrences and keeps original casing", () => {
    expect(highlightSegments("The Store controls records", ["store"])).toEqual([
      { text: "The ", match: false },
      { text: "Store", match: true },
      { text: " controls records", match: false },
    ]);
  });

  it("marks the longest overlapping term as one run", () => {
    const segments = highlightSegments("stores everywhere", ["store", "stores"]);
    expect(segments[0]).toEqual({ text: "stores", match: true });
  });

  it("treats terms as literals, not regex", () => {
    expect(highlightSegments("a+b calc", ["a+b"])).toEqual([
      { text: "a+b", match: true },
      { text: " calc", match: false },
    ]);
  });

  it("drops single-character noise terms", () => {
    expect(highlightSegments("a bug in api", ["a"])).toEqual([
      { text: "a bug in api", match: false },
    ]);
  });

  it.each(["Maple", "A+B", "Café", "日本語", "[brackets]"])(
    "preserves exact text and case-sensitive matches for %s",
    (term) => {
      const text = `before ${term} ${term.toLowerCase()} ${term.toUpperCase()} after`;
      for (const caseSensitive of [false, true]) {
        const segments = highlightSegments(text, [term], { caseSensitive });
        expect(segments.map((segment) => segment.text).join("")).toBe(text);
        const matches = segments.filter((segment) => segment.match);
        expect(matches.length).toBeGreaterThan(0);
        for (const match of matches) {
          expect(caseSensitive ? match.text : match.text.toLowerCase()).toBe(
            caseSensitive ? term : term.toLowerCase(),
          );
        }
      }
    },
  );
});

describe("hitTitleSegments", () => {
  it("marks the host's match positions, which may skip characters", () => {
    expect(segmentsAtIndexes("ParseConfig", [0, 5])).toEqual([
      { text: "P", match: true },
      { text: "arse", match: false },
      { text: "C", match: true },
      { text: "onfig", match: false },
    ]);
  });

  it("falls back to the query terms without host positions", () => {
    expect(hitTitleSegments("parse config", undefined, ["config"])).toEqual([
      { text: "parse ", match: false },
      { text: "config", match: true },
    ]);
    expect(hitTitleSegments("ParseConfig", [0, 5], ["config"])).toEqual(segmentsAtIndexes("ParseConfig", [0, 5]));
  });
});
