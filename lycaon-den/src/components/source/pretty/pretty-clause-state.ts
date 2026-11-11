/** What the SQL clause rule remembers between positions. */
export interface ClauseState {
  /** The last keyword read, lower case. */
  readonly keyword: string;
  /** Only whitespace has followed that keyword. */
  readonly adjacent: boolean;
  /** Only whitespace has followed the last `(`. */
  readonly afterOpen: boolean;
  /** The last code character was `)`. */
  readonly afterClose: boolean;
  /** Group depth whose commas break as a clause list, or -1. */
  readonly listDepth: number;
}

export const INITIAL_CLAUSE: ClauseState = { keyword: "", adjacent: false, afterOpen: false, afterClose: false, listDepth: -1 };

export function clausesEqual(a: ClauseState, b: ClauseState): boolean {
  return a.keyword === b.keyword && a.adjacent === b.adjacent && a.afterOpen === b.afterOpen &&
    a.afterClose === b.afterClose && a.listDepth === b.listDepth;
}
