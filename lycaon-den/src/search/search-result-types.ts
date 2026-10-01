import type { SearchHit } from "../api/types.ts";

/**
 * One family of search results. Each is a Crossbar tab, a section of
 * Everything, and a selector in the Search view, so the three always agree
 * on what a family holds.
 */
export type SearchResultTypeId = "messages" | "files" | "symbols" | "code" | "evidence";

export type SearchResultType = {
  id: SearchResultTypeId;
  label: string;
  /** Host hit kinds in the family; its query filter is their OR group. */
  kinds: readonly string[];
  emptyLabel: string;
};

const EVIDENCE_KINDS = [
  "evidence",
  "claim",
  "tool",
  "web",
  "artifact",
  "outcome",
  "network",
] as const;

/** Tab and selector order. */
export const SEARCH_RESULT_TYPES: readonly SearchResultType[] = [
  {
    id: "messages",
    label: "Messages",
    kinds: ["message"],
    emptyLabel: "No messages match",
  },
  {
    id: "files",
    label: "Files",
    kinds: ["file"],
    emptyLabel: "No files match",
  },
  {
    id: "symbols",
    label: "Symbols",
    kinds: ["symbol"],
    emptyLabel: "No symbols match",
  },
  {
    id: "code",
    label: "Code",
    kinds: ["code"],
    emptyLabel: "No code matches",
  },
  {
    id: "evidence",
    label: "Evidence",
    kinds: EVIDENCE_KINDS,
    emptyLabel: "No evidence matches",
  },
] as const;

export const SEARCH_RESULT_TYPE_BY_ID: Record<SearchResultTypeId, SearchResultType> =
  Object.fromEntries(SEARCH_RESULT_TYPES.map((type) => [type.id, type])) as Record<
    SearchResultTypeId,
    SearchResultType
  >;

export function hitInResultType(hit: SearchHit, id: SearchResultTypeId): boolean {
  return SEARCH_RESULT_TYPE_BY_ID[id].kinds.includes(hit.hit_kind.trim().toLowerCase());
}

export function isSearchResultTypeId(value: string): value is SearchResultTypeId {
  return SEARCH_RESULT_TYPES.some((type) => type.id === value);
}
