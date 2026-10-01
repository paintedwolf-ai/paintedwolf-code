const STORAGE_KEY = "den.search.history";

export type RecentSearchQuery = {
  query: string;
  originProjectId: string | null;
  ranAt: string;
};

type SearchHistoryStore = {
  recent: RecentSearchQuery[];
  /** Command ids, most recent first. */
  recentActions: string[];
};

function readStore(): SearchHistoryStore {
  if (typeof localStorage === "undefined") {
    return { recent: [], recentActions: [] };
  }
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { recent: [], recentActions: [] };
    const parsed = JSON.parse(raw) as Partial<SearchHistoryStore>;
    return {
      recent: Array.isArray(parsed.recent) ? parsed.recent : [],
      recentActions: Array.isArray(parsed.recentActions)
        ? parsed.recentActions.filter((id): id is string => typeof id === "string")
        : [],
    };
  } catch {
    return { recent: [], recentActions: [] };
  }
}

function writeStore(store: SearchHistoryStore): void {
  if (typeof localStorage === "undefined") return;
  localStorage.setItem(STORAGE_KEY, JSON.stringify(store));
}

export function listRecentQueries(): RecentSearchQuery[] {
  return readStore().recent;
}

export function listRecentActionIds(): string[] {
  return readStore().recentActions;
}

export function recordRecentAction(commandId: string): void {
  const id = commandId.trim();
  if (!id) return;
  const store = readStore();
  store.recentActions = [id, ...store.recentActions.filter((r) => r !== id)].slice(
    0,
    8,
  );
  writeStore(store);
}

export function recordRecentQuery(query: string, originProjectId: string | null): void {
  const trimmed = query.trim();
  if (!trimmed) return;
  const store = readStore();
  const entry: RecentSearchQuery = {
    query: trimmed,
    originProjectId,
    ranAt: new Date().toISOString(),
  };
  store.recent = [
    entry,
    ...store.recent.filter(
      (r) => r.query !== trimmed || r.originProjectId !== originProjectId,
    ),
  ].slice(0, 12);
  writeStore(store);
}
