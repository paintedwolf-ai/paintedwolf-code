/** Session-scoped search matching preferences. */

const STORAGE_KEY = "lycaon.search.matchPrefs";

export type SearchMatchPrefs = {
  caseSensitive: boolean;
  wholeWord: boolean;
  regex: boolean;
  includeDependencies: boolean;
  include: string;
  exclude: string;
};

export const DEFAULT_SEARCH_MATCH_PREFS: SearchMatchPrefs = {
  caseSensitive: false,
  wholeWord: false,
  regex: false,
  includeDependencies: false,
  include: "",
  exclude: "",
};

export function loadSearchMatchPrefs(): SearchMatchPrefs {
  if (typeof sessionStorage === "undefined") {
    return { ...DEFAULT_SEARCH_MATCH_PREFS };
  }
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...DEFAULT_SEARCH_MATCH_PREFS };
    const parsed = JSON.parse(raw) as Partial<SearchMatchPrefs>;
    return {
      caseSensitive: !!parsed.caseSensitive,
      wholeWord: !!parsed.wholeWord,
      regex: !!parsed.regex,
      includeDependencies: !!parsed.includeDependencies,
      include: typeof parsed.include === "string" ? parsed.include : "",
      exclude: typeof parsed.exclude === "string" ? parsed.exclude : "",
    };
  } catch {
    return { ...DEFAULT_SEARCH_MATCH_PREFS };
  }
}

export function saveSearchMatchPrefs(prefs: SearchMatchPrefs): void {
  if (typeof sessionStorage === "undefined") return;
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
}

/** Splits comma- or line-separated file patterns. */
export function parseGlobField(raw: string): string[] | undefined {
  const parts = raw
    .split(/[,\n]+/)
    .map((p) => p.trim())
    .filter(Boolean);
  return parts.length > 0 ? parts : undefined;
}

export function resetSearchMatchPrefsForTests(): void {
  if (typeof sessionStorage !== "undefined") {
    sessionStorage.removeItem(STORAGE_KEY);
  }
}
