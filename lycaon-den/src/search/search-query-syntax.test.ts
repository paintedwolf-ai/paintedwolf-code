import { describe, expect, it } from "vitest";
import {
  collectQueryFilters,
  pruneQueryFilters,
  scanSearchQuery,
} from "./search-query-syntax.ts";

const FIELDS = new Set(["kind", "project", "path", "verified"]);

describe("collectQueryFilters", () => {
  it("carries polarity through NOT", () => {
    expect(
      collectQueryFilters("kind:code NOT kind:message auth", FIELDS),
    ).toEqual([
      { field: "kind", value: "code", negated: false },
      { field: "kind", value: "message", negated: true },
    ]);
  });

  it("resolves double negation", () => {
    expect(collectQueryFilters("NOT NOT kind:code", FIELDS)).toEqual([
      { field: "kind", value: "code", negated: false },
    ]);
  });

  it("sees members inside OR groups", () => {
    expect(
      collectQueryFilters("(kind:code OR kind:message) auth", FIELDS),
    ).toEqual([
      { field: "kind", value: "code", negated: false },
      { field: "kind", value: "message", negated: false },
    ]);
  });

  it("keeps polarity through the mid-edit fallback", () => {
    // A trailing operator parses to null; filters and their adjacent NOT
    // runs are still reported.
    expect(collectQueryFilters("kind:code NOT", FIELDS)).toEqual([
      { field: "kind", value: "code", negated: false },
    ]);
    expect(collectQueryFilters("NOT kind:code AND", FIELDS)).toEqual([
      { field: "kind", value: "code", negated: true },
    ]);
  });
});

describe("pruneQueryFilters", () => {
  it("removes only the matching polarity", () => {
    const query = "kind:code NOT kind:code auth";
    expect(
      pruneQueryFilters(query, FIELDS, (o) => o.negated && o.value === "code"),
    ).toBe("kind:code auth");
    expect(
      pruneQueryFilters(query, FIELDS, (o) => !o.negated && o.value === "code"),
    ).toBe("NOT kind:code auth");
  });

  it("collapses an OR group when a member is pruned", () => {
    expect(
      pruneQueryFilters(
        "(kind:code OR kind:message) auth",
        FIELDS,
        (o) => o.value === "code",
      ),
    ).toBe("kind:message auth");
  });

  it("prunes textually when the query does not parse", () => {
    expect(
      pruneQueryFilters("auth kind:code NOT", FIELDS, (o) => o.value === "code"),
    ).toBe("auth NOT");
  });
});

describe("managed-token removal mid-edit", () => {
  it("removes a managed token even when the parse fails", () => {
    // Remove managed tokens even when the remaining query is incomplete.
    expect(
      pruneQueryFilters(
        "project:current auth NOT",
        FIELDS,
        (occurrence) => occurrence.field === "project",
      ),
    ).toBe("auth NOT");
  });
});

describe("lenient scanning", () => {
  it("treats lowercase operator words as text", () => {
    const kinds = scanSearchQuery("a not b AND c", FIELDS).map((t) => t.kind);
    expect(kinds).toEqual(["text", "text", "text", "and", "text"]);
  });

  it("separates on punctuation outside the word set", () => {
    const texts = scanSearchQuery("call — named; x->y", FIELDS).map((t) => t.text);
    expect(texts).toEqual(["call", "named", "x-", "y"]);
  });

  it("drops a sentence-final dot but keeps dots inside a word", () => {
    const texts = scanSearchQuery("tree survey. fmt.Println ...", FIELDS).map((t) => t.text);
    expect(texts).toEqual(["tree", "survey", "fmt.Println"]);
  });

  it("runs an unterminated quote to the end and drops a stray paren", () => {
    const tokens = scanSearchQuery('foo) "bar baz', FIELDS);
    expect(tokens.map((t) => [t.kind, t.text])).toEqual([
      ["text", "foo"],
      ["text", "bar baz"],
    ]);
  });
});

describe("quoted value escapes", () => {
  it("resolves host-lexer escapes on read", () => {
    const tokens = scanSearchQuery(String.raw`path:"a \"b\" c"`, FIELDS);
    const filter = tokens.find((token) => token.kind === "filter");
    expect(filter?.value).toBe('a "b" c');
  });
});
