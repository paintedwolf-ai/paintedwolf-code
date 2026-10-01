import { createSearchRefresh } from "../../search/search-refresh.ts";
import type { SearchHit, SearchResponse } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { parseGlobField, type SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import { crossbarServerQuery, shouldRunCrossbarServerSearch } from "../../search/crossbar-model.ts";
import { type CrossbarMode } from "../../search/crossbar-modes.ts";

export const SEARCH_DEBOUNCE_MS = 280;

export type CrossbarRequestOptions = {
  open: () => boolean;
  originProjectId: () => string | null;
  mode: () => CrossbarMode;
  hasLocalFileArm: () => boolean;
  activeSource: () => unknown;
  matchPrefs: () => SearchMatchPrefs;
  setHits: (hits: SearchHit[]) => void;
  setSearchCoverage: (coverage: Pick<SearchResponse, "issues" | "exhaustive"> | null) => void;
  setSearchErrorNote: (message: string) => void;
  setSearching: (busy: boolean) => void;
};

export function createCrossbarRequests({ open, originProjectId, mode, hasLocalFileArm,
  activeSource, matchPrefs, setHits, setSearchCoverage, setSearchErrorNote, setSearching,
}: CrossbarRequestOptions) {
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let searchGeneration = 0;
  let searchController: AbortController | undefined;
  const searchRefresh = createSearchRefresh();
  const runSearch = async (
    q: string,
    presentation: "foreground" | "background" = "foreground",
  ) => {
    if (!open()) return;
    searchController?.abort();
    const controller = new AbortController();
    searchController = controller;
    const generation = ++searchGeneration;
    const trimmed = q.trim();
    if (activeSource()) {
      // The active source lane supplies both results and loading state.
      setHits([]);
      setSearchCoverage(null);
      return;
    }
    if (!shouldRunCrossbarServerSearch(trimmed, mode(), hasLocalFileArm())) {
      setHits([]);
      setSearchCoverage(null);
      setSearchErrorNote("");
      setSearching(false);
      return;
    }
    const client = getLycaonClient();
    if (!client) {
      setHits([]);
      setSearchCoverage(null);
      setSearching(false);
      return;
    }
    if (presentation === "foreground") setSearching(true);
    try {
      const prefs = matchPrefs();
      const result = await client.search(
        crossbarServerQuery(trimmed, mode()),
        originProjectId() ?? undefined,
        {
          regex: prefs.regex,
          caseSensitive: prefs.caseSensitive,
          wholeWord: prefs.wholeWord,
          include: parseGlobField(prefs.include),
          exclude: parseGlobField(prefs.exclude),
          budget: "interactive",
        },
        controller.signal,
      );
      if (generation !== searchGeneration) return;
      setHits(result.hits ?? []);
      searchRefresh.schedule(result, () => {
        if (open() && generation === searchGeneration) {
          void runSearch(trimmed, "background");
        }
      });
      setSearchErrorNote("");
      setSearchCoverage(result);
    } catch (err) {
      if (generation !== searchGeneration) return;
      if (presentation === "background") {
        searchRefresh.clear();
        setSearchErrorNote("Results could not refresh. Type to search again.");
        return;
      }
      setHits([]);
      // Keep rejected queries distinct from empty results.
      setSearchErrorNote(
        err instanceof LycaonApiError &&
          (err.code === "search_query_invalid" ||
            err.code === "search_pattern_invalid")
          ? err.message
          : "Search could not finish. Try again.",
      );
    } finally {
      if (generation === searchGeneration) {
        setSearching(false);
      }
    }
  };

  const scheduleSearch = (q: string) => {
    searchController?.abort();
    searchRefresh.reset();
    if (debounceTimer) clearTimeout(debounceTimer);
    searchGeneration++;
    setSearchErrorNote("");
    setSearching(false);
    debounceTimer = setTimeout(() => void runSearch(q), SEARCH_DEBOUNCE_MS);
  };

  const cancelWhenClosed = () => {
      if (debounceTimer) {
        clearTimeout(debounceTimer);
        debounceTimer = undefined;
      }
      searchGeneration += 1;
      searchController?.abort();
      searchRefresh.clear();
  };
  const dispose = () => {
    searchGeneration++;
    searchController?.abort();
    searchRefresh.clear();
    if (debounceTimer) clearTimeout(debounceTimer);
  };
  return { runSearch, scheduleSearch, cancelWhenClosed, dispose };
}
