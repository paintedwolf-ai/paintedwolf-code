import { describe, expect, it } from "vitest";
import {
  appendPivotFilter,
  applyFieldSuggestion,
  filterToken,
  lintFromApiError,
  removeFilterToken,
  setQueryFilter,
  applyValueSuggestion,
  composeQueryWithProjectScope,
  DSL_FIELD_ALLOWLIST,
  exploreCitationRowQuery,
  exploreDslField,
  exploreRelatedToolCallsQuery,
  queryFilterActive,
  fieldSuggestionsAtCursor,
  groupHitsByProject,
  insertTextAtFilterBoundary,
  lintSearchQuery,
  projectScopeIsCurrent,
  projectScopedCodeQuery,
  rewriteProjectScope,
  searchQueryFreeText,
  segmentSearchQuery,
  stripProjectScopeToken,
  toggleQueryFilter,
  toggleResultType,
  phraseHintFor,
  resultTypeActive,
  valueSuggestionsAtCursor,
} from "./search-query-model.ts";
import { SEARCH_RESULT_TYPE_BY_ID, SEARCH_RESULT_TYPES } from "./search-result-types.ts";

describe("search-query-model", () => {
  it("treats non-allowlisted field-like tokens as prose (no lint)", () => {
    expect(lintSearchQuery("foo:bar")).toBeNull();
    expect(lintSearchQuery("Error: failed")).toBeNull();
    expect(lintSearchQuery("don't break")).toBeNull();
    expect(DSL_FIELD_ALLOWLIST).not.toContain("type");
  });

  it("segments only allowlisted filters outside quoted prose", () => {
    expect(segmentSearchQuery("Error: failed kind:web")).toEqual([
      { type: "text", text: "Error: failed ", start: 0 },
      { type: "filter", field: "kind", value: "web", raw: "kind:web", start: 14 },
    ]);
    expect(searchQueryFreeText("Error: failed kind:web")).toBe("Error: failed");
    expect(segmentSearchQuery('"kind:web behavior"')).toEqual([
      { type: "text", text: '"kind:web behavior"', start: 0 },
    ]);
    expect(searchQueryFreeText('"kind:web behavior"')).toBe("kind:web behavior");
  });

  it("ignores grouping punctuation inside quoted prose", () => {
    expect(lintSearchQuery('"literal ( text"')).toBeNull();
    expect(projectScopeIsCurrent('"project:current"')).toBe(false);
    expect(stripProjectScopeToken('"project:current"')).toBe('"project:current"');
  });

  it("never lints free text, however it is punctuated", () => {
    expect(lintSearchQuery("plus one orient call — named wording/settings")).toBeNull();
    expect(lintSearchQuery("deliverables are not nameable")).toBeNull();
    expect(lintSearchQuery('Nomos "unclosed')).toBeNull();
    expect(lintSearchQuery("foo) bar (baz")).toBeNull();
    expect(lintSearchQuery("foo AND")).toBeNull();
  });

  it("hints at quoting for a sentence of bare words, not for a phrase", () => {
    expect(phraseHintFor("auth token")).toBeNull();
    expect(phraseHintFor("deliverables are not nameable")).toContain("quote");
    expect(phraseHintFor('"deliverables are not nameable"')).toBeNull();
    expect(phraseHintFor("kind:code auth token refresh")).toContain("quote");
  });

  it("lints empty value on allowlisted fields", () => {
    expect(lintSearchQuery("kind:")?.kind).toBe("empty_value");
  });

  it("lints empty query", () => {
    expect(lintSearchQuery("   ")?.kind).toBe("syntax");
  });

  it("rewrites project scope", () => {
    expect(rewriteProjectScope("auth bug", "current")).toBe(
      "auth bug kind:code project:current",
    );
    expect(projectScopeIsCurrent("auth bug kind:code project:current")).toBe(true);
    expect(rewriteProjectScope("auth project:current", "everything")).toBe("auth");
  });

  it("selects kind:code when switching to project scope unless a kind is set", () => {
    expect(projectScopedCodeQuery("toolbar")).toBe(
      "toolbar kind:code project:current",
    );
    expect(projectScopedCodeQuery("")).toBe("kind:code project:current");
    expect(projectScopedCodeQuery("toolbar kind:code project:current")).toBe(
      "toolbar kind:code project:current",
    );
    expect(projectScopedCodeQuery("type:message")).toBe(
      "type:message kind:code project:current",
    );
    expect(rewriteProjectScope("kind:message auth", "current")).toBe(
      "kind:message auth project:current",
    );
    expect(rewriteProjectScope("kind:code auth", "current")).toBe(
      "kind:code auth project:current",
    );
  });

  it("strips and composes managed project scope", () => {
    expect(stripProjectScopeToken("auth project:current")).toBe("auth");
    expect(composeQueryWithProjectScope("auth", true)).toBe("auth project:current");
    expect(composeQueryWithProjectScope("", true)).toBe("project:current");
    expect(composeQueryWithProjectScope("auth", false)).toBe("auth");
  });

  it("starts typed text after a completed filter chip", () => {
    expect(insertTextAtFilterBoundary("kind:code", 9, 9, "a")).toEqual({
      query: "kind:code a",
      cursor: 11,
    });
    expect(
      insertTextAtFilterBoundary("auth kind:code", 14, 14, "needle"),
    ).toEqual({ query: "auth kind:code needle", cursor: 21 });
    expect(insertTextAtFilterBoundary("kind:code auth", 9, 9, "x")).toEqual({
      query: "kind:code x auth",
      cursor: 11,
    });
  });

  it("leaves ordinary query edits alone", () => {
    expect(insertTextAtFilterBoundary("kind:code", 5, 5, "x")).toBeNull();
    expect(insertTextAtFilterBoundary("kind:code", 0, 4, "x")).toBeNull();
    expect(insertTextAtFilterBoundary("plain text", 10, 10, "x")).toBeNull();
    expect(insertTextAtFilterBoundary("kind:code", 9, 9, " ")).toBeNull();
  });

  it("suggests DSL fields at the cursor", () => {
    const ctx = fieldSuggestionsAtCursor("kind:code ver", 13);
    expect(ctx?.partial).toBe("ver");
    expect(ctx?.suggestions).toContain("verified");
  });

  it("applies a field suggestion with a trailing colon", () => {
    const ctx = fieldSuggestionsAtCursor("ki", 2)!;
    const applied = applyFieldSuggestion("ki", ctx, "kind", 2);
    expect(applied.query).toBe("kind:");
    expect(applied.cursor).toBe(5);
  });

  it("suggests facet values after field colon", () => {
    const ctx = valueSuggestionsAtCursor("kind:", 5, [
      {
        key: "kind",
        values: [
          { value: "code", count: 12 },
          { value: "finding", count: 3 },
        ],
      },
    ]);
    expect(ctx?.suggestions.map((v) => v.value)).toEqual(["code", "finding"]);
  });

  it("separates a completed value suggestion from following text", () => {
    expect(applyValueSuggestion("kind:coauth", "code", 7)).toEqual({
      query: "kind:code auth",
      cursor: 10,
    });
  });

  it("toggles query filters in the DSL string", () => {
    const withKind = toggleQueryFilter("auth", "kind", "code");
    expect(queryFilterActive(withKind, "kind", "code")).toBe(true);
    const without = toggleQueryFilter(withKind, "kind", "code");
    expect(without).toBe("auth");
  });

  it("removes filters by simplifying the Boolean expression", () => {
    expect(
      toggleQueryFilter("(kind:code OR kind:message) auth", "kind", "code"),
    ).toBe("kind:message auth");
    expect(
      stripProjectScopeToken("(project:current AND kind:code) OR kind:message"),
    ).toBe("kind:code OR kind:message");
  });

  it("appends pivot filters without duplicates", () => {
    const q = appendPivotFilter("auth", "session", "abc");
    expect(q).toContain("session:abc");
    expect(appendPivotFilter(q, "session", "abc")).toBe(q);
  });

  it("groups hits with origin project first", () => {
    const groups = groupHitsByProject(
      [
        {
          hit_id: "web-b",
          hit_kind: "web",
          source: "tool",
          project_id: "b",
          project_name: "Beta",
          title: "Beta result",
        },
        {
          hit_id: "web-a",
          hit_kind: "web",
          source: "tool",
          project_id: "a",
          project_name: "Alpha",
          title: "Alpha result",
        },
      ],
      "a",
    );
    expect(groups[0]?.projectId).toBe("a");
    expect(groups[0]?.isOrigin).toBe(true);
  });

  it("loads the shared grammar catalog", () => {
    expect(DSL_FIELD_ALLOWLIST).toContain("project");
    expect(new Set(DSL_FIELD_ALLOWLIST).size).toBe(DSL_FIELD_ALLOWLIST.length);
  });

  it("builds explore DSL for citation pivots", () => {
    expect(exploreDslField("session", "sess-1")).toBe("session:sess-1");
    expect(exploreDslField("leg", "leg-1")).toBe("leg:leg-1");
    expect(exploreCitationRowQuery({ handle: "read#1", path: "src/a.go" })).toBe(
      "handle:read#1",
    );
    expect(exploreCitationRowQuery({ path: "src/a.go" })).toBe("path:src/a.go");
  });

  it("finds other calls to the same tool within the originating chat", () => {
    expect(
      exploreRelatedToolCallsQuery({ toolCallId: "tc-1", sessionId: "sess-1", tool: "read" }),
    ).toBe("session:sess-1 tool:read kind:tool NOT ref:tc-1");
    expect(exploreRelatedToolCallsQuery({ toolCallId: "", sessionId: "sess-1", tool: "read" })).toBeUndefined();
  });
});

describe("filter polarity", () => {
  it("does not light a chip for a negated filter", () => {
    const query = "NOT kind:message auth";
    expect(queryFilterActive(query, "kind", "message")).toBe(false);
  });

  it("removes a negated filter without touching the positive twin", () => {
    const query = "kind:code auth NOT kind:message";
    const next = removeFilterToken(query, "kind", "message", true);
    expect(next).toContain("kind:code");
    expect(next).not.toContain("kind:message");
  });
});

describe("same-field multi-select", () => {
  it("composes an OR group instead of an impossible AND", () => {
    const one = toggleQueryFilter("auth", "kind", "code");
    const two = toggleQueryFilter(one, "kind", "message");
    expect(two).toContain("(kind:code OR kind:message)");
    const three = toggleQueryFilter(two, "kind", "web");
    expect(three).toContain("(kind:code OR kind:message OR kind:web)");
  });

  it("collapses back to a bare filter when one value remains", () => {
    const two = toggleQueryFilter(
      toggleQueryFilter("auth", "kind", "code"),
      "kind",
      "message",
    );
    const one = toggleQueryFilter(two, "kind", "code");
    expect(one).toContain("kind:message");
    expect(one).not.toContain("OR");
  });

  it("adding a value lifts its own negation", () => {
    const next = toggleQueryFilter("NOT kind:code auth", "kind", "code");
    expect(next).not.toContain("NOT");
    expect(queryFilterActive(next, "kind", "code")).toBe(true);
  });

  it("keeps AND semantics for non-equality fields", () => {
    const next = toggleQueryFilter("auth path:src/*", "path", "docs/*");
    expect(next).not.toContain("OR");
    expect(next).toContain("path:src/*");
    expect(next).toContain("path:docs/*");
  });
});

describe("search result types", () => {
  const evidence = SEARCH_RESULT_TYPE_BY_ID.evidence;
  const code = SEARCH_RESULT_TYPE_BY_ID.code;
  const symbols = SEARCH_RESULT_TYPE_BY_ID.symbols;

  it("lists one family per selector in tab order", () => {
    expect(SEARCH_RESULT_TYPES.map((type) => type.id)).toEqual([
      "messages",
      "files",
      "symbols",
      "code",
      "evidence",
    ]);
    expect(symbols.kinds).toEqual(["symbol"]);
    expect(evidence.kinds).toContain("web");
  });

  it("selects and clears the Symbols family", () => {
    const next = toggleResultType("ParseConfig", symbols);
    expect(next).toContain("kind:symbol");
    expect(resultTypeActive(next, symbols)).toBe(true);
    expect(toggleResultType(next, symbols)).toBe("ParseConfig");
  });

  it("adds Evidence to Code as one OR selection of every evidence kind", () => {
    const next = toggleResultType("auth kind:code", evidence);
    for (const kind of ["code", ...evidence.kinds]) expect(next).toContain(`kind:${kind}`);
    expect(next).not.toContain("NOT kind:");
    expect(resultTypeActive(next, code)).toBe(true);
    expect(resultTypeActive(next, evidence)).toBe(true);
    expect(toggleResultType(next, evidence)).toBe("auth kind:code");
  });

  it("completes a partly selected family instead of clearing it", () => {
    const partial = "auth kind:web";
    expect(resultTypeActive(partial, evidence)).toBe(false);
    expect(resultTypeActive(toggleResultType(partial, evidence), evidence)).toBe(true);
  });

  it("does not light a type selector for a negated kind", () => {
    expect(resultTypeActive("NOT kind:evidence auth", evidence)).toBe(false);
  });
});

describe("single-value filter replacement", () => {
  it("sets and clears verification without touching type or scope", () => {
    const verified = setQueryFilter(
      "kind:evidence project:current",
      "verified",
      "false",
    );
    expect(verified).toContain("verified:false");
    expect(setQueryFilter(verified, "verified")).toBe(
      "kind:evidence project:current",
    );
  });

  it("replaces an existing verification choice", () => {
    expect(setQueryFilter("verified:true auth", "verified", "false")).toBe(
      "auth verified:false",
    );
  });
});

describe("filter token quoting", () => {
  it("escapes quotes so the token survives a re-parse", () => {
    expect(filterToken("path", 'a "b" c')).toBe(String.raw`path:"a \"b\" c"`);
    expect(filterToken("kind", "code")).toBe("kind:code");
  });
});

describe("lintFromApiError", () => {
  it("maps host details onto the lint shape", () => {
    expect(
      lintFromApiError({
        message: "unknown kind value",
        details: { offset: 5, field: "kind", kind: "invalid_value" },
      }),
    ).toEqual({
      offset: 5,
      field: "kind",
      kind: "invalid_value",
      message: "unknown kind value",
    });
  });

  it("defaults safely without details", () => {
    expect(lintFromApiError({ message: "boom" })).toEqual({
      offset: 0,
      kind: "syntax",
      message: "boom",
    });
  });
});
