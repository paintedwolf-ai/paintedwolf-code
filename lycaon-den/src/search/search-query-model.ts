import type { SearchHit } from "../api/types.ts";
import {
  collectQueryFilters,
  pruneQueryFilters,
  scanSearchQuery,
  suggestsQuotingAsPhrase,
  type QueryFilterOccurrence,
} from "./search-query-syntax.ts";
import type { SearchResultType } from "./search-result-types.ts";
import grammar from "../../../lycaon/config/runtime/search/grammar.json";

const QUERY_LINT_KINDS = ["syntax", "empty_value", "invalid_value"] as const;

type QueryLintKind = (typeof QUERY_LINT_KINDS)[number];

export type QueryLintError = {
  offset: number;
  message: string;
  field?: string;
  kind: QueryLintKind;
};

/** Map host compile errors into query diagnostics. */
export function lintFromApiError(err: {
  message: string;
  details?: unknown;
}): QueryLintError {
  const details = (err.details ?? {}) as {
    offset?: unknown;
    field?: unknown;
    kind?: unknown;
  };
  const offset =
    typeof details.offset === "number" && details.offset >= 0
      ? details.offset
      : 0;
  const field =
    typeof details.field === "string" && details.field
      ? details.field
      : undefined;
  const kind = QUERY_LINT_KINDS.find((k) => k === details.kind) ?? "syntax";
  return { offset, kind, message: err.message, ...(field ? { field } : {}) };
}

export type QuerySegment =
  | { type: "text"; text: string; start: number }
  | { type: "filter"; field: string; value: string; raw: string; start: number };

export const DSL_FIELD_ALLOWLIST: readonly string[] = grammar.fields.map(
  (field) => field.id,
);

const DSL_FIELD_HINTS: Readonly<Record<string, string>> =
  Object.fromEntries(grammar.fields.map((field) => [field.id, field.label]));

/** Closed value vocabularies from the shared grammar catalog. */
const DSL_FIELD_VALUES: Readonly<Record<string, readonly string[]>> =
  Object.fromEntries(
    grammar.fields
      .filter(
        (field): field is (typeof field) & { values: string[] } =>
          "values" in field && Array.isArray(field.values),
      )
      .map((field) => [field.id, field.values]),
  );

const ALLOWED_FIELDS = new Set<string>(DSL_FIELD_ALLOWLIST);

/** Optimistic query-bar lint only — host search_query_invalid is authoritative. */
export function lintSearchQuery(query: string): QueryLintError | null {
  const trimmed = query.trim();
  if (!trimmed) {
    return {
      offset: 0,
      kind: "syntax",
      message: "empty query",
    };
  }
  // Only a recognized filter can lint; free text always scans.
  for (const current of scanSearchQuery(query, ALLOWED_FIELDS)) {
    if (current.kind !== "filter") continue;
    const value = current.value?.trim() ?? "";
    if (!value) {
      return {
        offset: current.start,
        kind: "empty_value",
        field: current.field,
        message: `empty value for ${current.field}`,
      };
    }
    // Same closed vocabularies the host compiler enforces.
    const allowed = current.field ? DSL_FIELD_VALUES[current.field] : undefined;
    if (
      allowed &&
      !allowed.some((entry) => entry.toLowerCase() === value.toLowerCase())
    ) {
      return {
        offset: current.start,
        kind: "invalid_value",
        field: current.field,
        message: `unknown ${current.field} value "${value}" — one of ${allowed.join(", ")}`,
      };
    }
  }
  return null;
}

export function formatLintMessage(error: QueryLintError): string {
  switch (error.kind) {
    case "empty_value":
      return `Add a value after ${error.field}: (e.g. ${error.field}:finding)`;
    case "invalid_value":
      return error.message;
    case "syntax":
      return error.message === "empty query"
        ? "Enter keywords or filters to search"
        : error.message;
  }
}

/** Bare words match in any order; quoted text matches as a phrase. */
export function phraseHintFor(query: string): string | null {
  if (!suggestsQuotingAsPhrase(scanSearchQuery(query, ALLOWED_FIELDS))) return null;
  return "Matching every word in any order — quote the text to match it exactly";
}

/** Every filter with its polarity, against the shared field allowlist. */
export function queryFilterOccurrences(query: string): QueryFilterOccurrence[] {
  return collectQueryFilters(query, ALLOWED_FIELDS);
}

/** Polarity-aware filter removal against the shared field allowlist. */
export function pruneQueryFilterOccurrences(
  query: string,
  remove: (occurrence: QueryFilterOccurrence) => boolean,
): string {
  return pruneQueryFilters(query, ALLOWED_FIELDS, remove);
}

export function removeFilterToken(
  query: string,
  field: string,
  value: string,
  negated = false,
): string {
  const normalizedField = field.toLowerCase();
  return pruneQueryFilters(
    query,
    ALLOWED_FIELDS,
    (occurrence) =>
      occurrence.field === normalizedField &&
      occurrence.value.toLowerCase() === value.toLowerCase() &&
      occurrence.negated === negated,
  );
}

export function segmentSearchQuery(query: string): QuerySegment[] {
  const segments: QuerySegment[] = [];
  const filters = scanSearchQuery(query, ALLOWED_FIELDS).filter(
    (current) => current.kind === "filter",
  );
  let last = 0;
  for (const current of filters) {
    if (current.start > last) {
      segments.push({
        type: "text",
        text: query.slice(last, current.start),
        start: last,
      });
    }
    segments.push({
      type: "filter",
      field: current.field ?? "",
      value: current.value ?? "",
      raw: current.raw,
      start: current.start,
    });
    last = current.end;
  }
  if (last < query.length) {
    segments.push({ type: "text", text: query.slice(last), start: last });
  }
  return segments;
}

export function isFilterBoundaryAtCursor(
  query: string,
  selectionStart: number,
  selectionEnd: number,
): boolean {
  if (
    selectionStart !== selectionEnd ||
    selectionStart < 0 ||
    selectionStart > query.length
  ) {
    return false;
  }

  const before = query.slice(0, selectionStart);
  const segments = segmentSearchQuery(before);
  const last = segments[segments.length - 1];
  return !!(
    last &&
    last.type === "filter" &&
    last.value.trim() &&
    last.start + last.raw.length === before.length
  );
}

export function insertTextAtFilterBoundary(
  query: string,
  selectionStart: number,
  selectionEnd: number,
  text: string,
): { query: string; cursor: number } | null {
  if (
    !text ||
    /^\s/u.test(text) ||
    !isFilterBoundaryAtCursor(query, selectionStart, selectionEnd)
  ) {
    return null;
  }

  const before = query.slice(0, selectionStart);
  return {
    query: `${before} ${text}${query.slice(selectionEnd)}`,
    cursor: selectionStart + text.length + 1,
  };
}

/** Query prose with DSL filters removed. */
export function searchQueryFreeText(query: string): string {
  return scanSearchQuery(query, ALLOWED_FIELDS)
    .filter((current) => current.kind === "text")
    .map((current) => current.text)
    .join(" ")
    .replace(/\s+/g, " ")
    .trim();
}

export function projectScopeIsCurrent(query: string): boolean {
  return queryFilterOccurrences(query).some(
    (occurrence) =>
      !occurrence.negated &&
      occurrence.field === "project" &&
      occurrence.value.toLowerCase() === "current",
  );
}

/** User-editable DSL with managed project scope tokens removed. */
export function stripProjectScopeToken(query: string): string {
  return pruneQueryFilters(
    query,
    ALLOWED_FIELDS,
    (occurrence) => occurrence.field === "project",
  );
}

/** Merge editable text with the scope pill's project:current token for API search. */
export function composeQueryWithProjectScope(editable: string, scoped: boolean): string {
  const base = editable.replace(/\s{2,}/g, " ").trim();
  if (scoped) {
    return base ? `${base} project:current` : "project:current";
  }
  return base;
}

function queryHasKindFilter(query: string): boolean {
  // Only a required kind counts: NOT kind:web narrows nothing by itself.
  return queryFilterOccurrences(query).some(
    (occurrence) => !occurrence.negated && occurrence.field === "kind",
  );
}

/** project:current alone does not walk the live tree. */
function ensureCodeKind(query: string): string {
  if (queryHasKindFilter(query)) return query;
  return toggleQueryFilter(query, "kind", "code");
}

export function projectScopedCodeQuery(freeText: string): string {
  const editable = stripProjectScopeToken(freeText.trim());
  return composeQueryWithProjectScope(ensureCodeKind(editable), true);
}

type FieldSuggestionContext = {
  start: number;
  partial: string;
  suggestions: string[];
};

type ValueSuggestionContext = {
  field: string;
  valueStart: number;
  partial: string;
  suggestions: { value: string; count: number }[];
};

export type QuerySuggestion =
  | { kind: "field"; field: string; hint: string }
  | { kind: "value"; field: string; value: string; count: number }
  | { kind: "recent"; query: string };

type FacetLike = { key: string; values: { value: string; count: number }[] };

export function valueSuggestionsAtCursor(
  query: string,
  cursor: number,
  facets: FacetLike[],
): ValueSuggestionContext | null {
  const before = query.slice(0, Math.max(0, cursor));
  const match = /(?:^|\s)([a-z_]+):(?:"([^"]*)"|([^\s(]*))$/i.exec(before);
  if (!match) return null;
  const field = (match[1] ?? "").toLowerCase();
  const partial = (match[2] ?? match[3] ?? "").toLowerCase();
  const facet = facets.find((entry) => entry.key.toLowerCase() === field);
  if (!facet) return null;
  const valuePart = match[2] ?? match[3] ?? "";
  const valueStart = cursor - valuePart.length;
  const suggestions = facet.values
    .filter(
      (entry) =>
        !partial || entry.value.toLowerCase().startsWith(partial),
    )
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value))
    .slice(0, 10);
  if (suggestions.length === 0 && partial) return null;
  return { field, valueStart, partial, suggestions };
}

export function applyValueSuggestion(
  query: string,
  value: string,
  cursor: number,
): { query: string; cursor: number } {
  const before = query.slice(0, cursor);
  const after = query.slice(cursor);
  const match = /((?:^|\s)[a-z_]+:)(?:"[^"]*"|[^\s(]*)$/i.exec(before);
  if (!match) return { query, cursor };
  const prefix = before.slice(0, match.index + (match[1] ?? "").length);
  const token = quoteFilterValue(value);
  const separator = after && !/^[\s)]/u.test(after) ? " " : "";
  return {
    query: `${prefix}${token}${separator}${after}`,
    cursor: prefix.length + token.length + separator.length,
  };
}

export function fieldSuggestionsAtCursor(
  query: string,
  cursor: number,
): FieldSuggestionContext | null {
  const before = query.slice(0, Math.max(0, cursor));
  const after = query.slice(cursor);
  if (after.trimStart().length > 0 && !after.startsWith(" ")) return null;
  const match = /(?:^|\s)([a-z_]*)$/i.exec(before);
  if (!match) return null;
  const partial = (match[1] ?? "").toLowerCase();
  const start = cursor - partial.length;
  const pool = DSL_FIELD_ALLOWLIST.filter((field) => field !== "project");
  if (!partial) {
    return null;
  }
  const suggestions = pool.filter(
    (field) => field.startsWith(partial) && field !== partial,
  );
  if (suggestions.length === 0) return null;
  return { start, partial, suggestions: suggestions.slice(0, 8) };
}

function recentQuerySuggestions(
  query: string,
  recentQueries: string[],
  limit = 5,
): string[] {
  const partial = query.trim().toLowerCase();
  if (partial.length < 2) return [];
  return recentQueries
    .filter((entry) => entry.toLowerCase().includes(partial))
    .slice(0, limit);
}

/** Unified autocomplete: facet values, then fields, then recent searches. */
export function querySuggestionsAtCursor(
  query: string,
  cursor: number,
  facets: FacetLike[],
  recentQueries: string[],
): QuerySuggestion[] {
  const values = valueSuggestionsAtCursor(query, cursor, facets);
  if (values) {
    return values.suggestions.map((entry) => ({
      kind: "value" as const,
      field: values.field,
      value: entry.value,
      count: entry.count,
    }));
  }

  const fields = fieldSuggestionsAtCursor(query, cursor);
  if (fields) {
    return fields.suggestions.map((field) => ({
      kind: "field" as const,
      field,
      hint: DSL_FIELD_HINTS[field] ?? field,
    }));
  }

  const recent = recentQuerySuggestions(query, recentQueries);
  if (recent.length > 0 && cursor === query.length) {
    return recent.map((entry) => ({ kind: "recent" as const, query: entry }));
  }

  return [];
}

export function suggestionContextAtCursor(
  query: string,
  cursor: number,
  facets: FacetLike[],
): FieldSuggestionContext | ValueSuggestionContext | null {
  return (
    valueSuggestionsAtCursor(query, cursor, facets) ??
    fieldSuggestionsAtCursor(query, cursor)
  );
}

export function applyFieldSuggestion(
  query: string,
  context: FieldSuggestionContext,
  field: string,
  cursor: number,
): { query: string; cursor: number } {
  const before = query.slice(0, context.start);
  const after = query.slice(cursor);
  const needsColon = !after.startsWith(":");
  const inserted = `${field}${needsColon ? ":" : ""}`;
  const next = `${before}${inserted}${after}`;
  const nextCursor = context.start + inserted.length;
  return { query: next, cursor: nextCursor };
}

export function rewriteProjectScope(
  query: string,
  mode: "everything" | "current",
): string {
  if (mode === "current") {
    return projectScopedCodeQuery(query);
  }
  return composeQueryWithProjectScope(stripProjectScopeToken(query), false);
}

/** Quote a filter value when needed, escaping so it survives a re-parse. */
export function quoteFilterValue(value: string): string {
  if (/[\s"']/.test(value)) {
    return `"${value.replace(/(["\\])/g, "\\$1")}"`;
  }
  return value;
}

/** Serialize one filter token. */
export function filterToken(field: string, value: string): string {
  return `${field}:${quoteFilterValue(value)}`;
}

function sameValue(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}

/** Reports whether the query requires this filter. */
export function queryFilterActive(
  query: string,
  field: string,
  value: string,
): boolean {
  const normalizedField = field.toLowerCase();
  return collectQueryFilters(query, ALLOWED_FIELDS).some(
    (occurrence) =>
      !occurrence.negated &&
      occurrence.field === normalizedField &&
      sameValue(occurrence.value, value),
  );
}

/** Single-valued catalog columns use OR for multiple required values. */
const OR_GROUP_FIELDS = new Set(
  grammar.fields
    .filter((field) => "single_valued" in field && field.single_valued)
    .map((field) => field.id),
);

function positiveFieldValues(query: string, field: string): string[] {
  const seen: string[] = [];
  for (const occurrence of collectQueryFilters(query, ALLOWED_FIELDS)) {
    if (occurrence.negated || occurrence.field !== field) continue;
    if (seen.some((value) => sameValue(value, occurrence.value))) continue;
    seen.push(occurrence.value);
  }
  return seen;
}

function appendClause(query: string, clause: string): string {
  const trimmed = query.trim();
  return trimmed ? `${trimmed} ${clause}` : clause;
}

/** Rewrite a field's required values as one clause: bare or an OR group. */
function writeFieldSelection(query: string, field: string, values: string[]): string {
  const base = pruneQueryFilters(
    query,
    ALLOWED_FIELDS,
    (occurrence) => !occurrence.negated && occurrence.field === field,
  );
  if (values.length === 0) return base;
  if (values.length === 1) return appendClause(base, filterToken(field, values[0]!));
  const group = values.map((value) => filterToken(field, value)).join(" OR ");
  return appendClause(base, `(${group})`);
}

/** Replaces one field's required selection. */
export function setQueryFilter(
  query: string,
  field: string,
  value?: string,
): string {
  const normalizedField = field.toLowerCase();
  const base = pruneQueryFilters(
    query,
    ALLOWED_FIELDS,
    (occurrence) =>
      !occurrence.negated && occurrence.field === normalizedField,
  );
  const trimmed = value?.trim();
  return trimmed
    ? appendClause(base, filterToken(normalizedField, trimmed))
    : base;
}

export function toggleQueryFilter(
  query: string,
  field: string,
  value: string,
): string {
  const normalizedField = field.toLowerCase();
  if (queryFilterActive(query, normalizedField, value)) {
    // Positional pruning collapses an OR group in place.
    return pruneQueryFilters(
      query,
      ALLOWED_FIELDS,
      (occurrence) =>
        !occurrence.negated &&
        occurrence.field === normalizedField &&
        sameValue(occurrence.value, value),
    );
  }
  // A positive selection removes its matching negation.
  const cleared = pruneQueryFilters(
    query,
    ALLOWED_FIELDS,
    (occurrence) =>
      occurrence.negated &&
      occurrence.field === normalizedField &&
      sameValue(occurrence.value, value),
  );
  if (OR_GROUP_FIELDS.has(normalizedField)) {
    const values = [...positiveFieldValues(cleared, normalizedField), value];
    return writeFieldSelection(cleared, normalizedField, values);
  }
  return appendClause(cleared, filterToken(normalizedField, value));
}

/** Toggle a result family: select every kind it holds, or clear them all.
 * Kinds compose as one OR group with the other selected families. */
export function toggleResultType(
  query: string,
  resultType: SearchResultType,
): string {
  const active = resultTypeActive(query, resultType);
  let next = query;
  for (const kind of resultType.kinds) {
    if (queryFilterActive(next, "kind", kind) === active) {
      next = toggleQueryFilter(next, "kind", kind);
    }
  }
  return next;
}

/** A family is selected when every kind it holds is. */
export function resultTypeActive(
  query: string,
  resultType: SearchResultType,
): boolean {
  return resultType.kinds.every((kind) => queryFilterActive(query, "kind", kind));
}

export function appendPivotFilter(
  query: string,
  field: string,
  value: string,
): string {
  if (!value.trim()) return query;
  if (queryFilterActive(query, field, value)) return query;
  return toggleQueryFilter(query, field, value);
}

/** Single DSL token for chat-to-search Explore pivots (handle/path/session/leg). */
export function exploreDslField(field: string, value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  return filterToken(field, trimmed);
}

export function exploreCitationRowQuery(item: {
  handle?: string;
  path?: string;
}): string | undefined {
  const handle = item.handle?.trim();
  if (handle) return exploreDslField("handle", handle);
  const path = item.path?.trim();
  if (path) return exploreDslField("path", path);
  return undefined;
}

/** Other calls to the same tool in this chat, excluding the current call. */
export function exploreRelatedToolCallsQuery(opts: {
  toolCallId: string;
  sessionId: string;
  tool: string;
}): string | undefined {
  const toolCallId = opts.toolCallId.trim();
  const sessionId = opts.sessionId.trim();
  const tool = opts.tool.trim();
  if (!toolCallId || !sessionId || !tool) return undefined;
  return [
    exploreDslField("session", sessionId),
    exploreDslField("tool", tool),
    "kind:tool",
    `NOT ${exploreDslField("ref", toolCallId)}`,
  ].join(" ");
}

export type ProjectHitGroup = {
  projectId: string;
  projectName: string;
  hits: SearchHit[];
  isOrigin: boolean;
};

export function groupHitsByProject(
  hits: SearchHit[],
  originProjectId: string | null,
): ProjectHitGroup[] {
  const map = new Map<string, ProjectHitGroup>();
  for (const hit of hits) {
    const existing = map.get(hit.project_id);
    if (existing) {
      existing.hits.push(hit);
      continue;
    }
    map.set(hit.project_id, {
      projectId: hit.project_id,
      projectName: hit.project_name?.trim() || hit.project_id,
      hits: [hit],
      isOrigin: originProjectId != null && hit.project_id === originProjectId,
    });
  }
  const groups = [...map.values()];
  groups.sort((a, b) => {
    if (a.isOrigin && !b.isOrigin) return -1;
    if (!a.isOrigin && b.isOrigin) return 1;
    return a.projectName.localeCompare(b.projectName);
  });
  return groups;
}
