import {
  pruneQueryFilterOccurrences,
  queryFilterOccurrences,
  resultTypeActive,
} from "./search-query-model.ts";
import type { QueryFilterOccurrence } from "./search-query-syntax.ts";
import { SEARCH_RESULT_TYPES } from "./search-result-types.ts";

/** Kinds a selector stands for: those of every family the query selects
 * whole. A kind outside a whole family is a refinement. */
function selectedKinds(query: string): Set<string> {
  return new Set(
    SEARCH_RESULT_TYPES.filter((type) => resultTypeActive(query, type)).flatMap(
      (type) => type.kinds,
    ),
  );
}

function selectorPredicate(query: string): (occurrence: QueryFilterOccurrence) => boolean {
  const kinds = selectedKinds(query);
  return (occurrence) =>
    occurrence.field === "project" ||
    (!occurrence.negated &&
      occurrence.field === "kind" &&
      kinds.has(occurrence.value.toLowerCase()));
}

export function searchRefinementOccurrences(
  query: string,
): QueryFilterOccurrence[] {
  const isSelector = selectorPredicate(query);
  return queryFilterOccurrences(query).filter((occurrence) => !isSelector(occurrence));
}

export function hasSearchRefinements(
  query: string,
  include: string,
  exclude: string,
): boolean {
  return (
    searchRefinementOccurrences(query).length > 0 ||
    include.trim() !== "" ||
    exclude.trim() !== ""
  );
}

export function clearSearchRefinements(query: string): string {
  const isSelector = selectorPredicate(query);
  return pruneQueryFilterOccurrences(query, (occurrence) => !isSelector(occurrence));
}
