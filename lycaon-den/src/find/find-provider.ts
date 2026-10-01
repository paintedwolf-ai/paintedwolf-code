/** Provider contract for in-view find. */

export type FindProviderKind = "dom" | "codemirror";

export type FindMatchCount = {
  /** Absolute match count, or the cap when `capped`. */
  total: number;
  /** 0-based index of the active match; -1 when none. */
  activeIndex: number;
  /** True when iteration stopped at `FIND_MATCH_COUNT_CAP`. */
  capped: boolean;
};

/** Complete interface for a find provider. */
export type FindProvider = {
  setQuery(q: string, opts: { caseSensitive: boolean }): void;
  count(): FindMatchCount;
  next(): void;
  prev(): void;
  /** Returns false when there is no active editable match. */
  replaceCurrent(text: string): boolean;
  /** Reports a capped replacement without editing. */
  replaceAll(text: string): { replaced: number; capped: boolean };
  /** Freeze current multi-line selection as scope. Returns false when unsupported. */
  scopeToSelection(on: boolean): boolean;
  supportsScope(): boolean;
  /** Whether the live selection spans ≥2 lines (for enabling the toggle). */
  selectionSpansMultipleLines(): boolean;
  /** Turn every in-scope match into a cursor, up to `FIND_SELECT_ALL_CURSOR_CAP`; null when unsupported. */
  selectAllMatches(): { cursors: number; capped: boolean } | null;
  dispose(): void;
};

export const FIND_MATCH_COUNT_CAP = 10_000;
export const FIND_SELECT_ALL_CURSOR_CAP = 1_000;
