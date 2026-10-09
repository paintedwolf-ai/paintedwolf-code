import { searchHitKey } from "./SearchResultGroup.tsx";
import { batch, createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import type { SearchHit, SearchResponse } from "../../api/types.ts";
import type { SearchMatchOptions } from "../../api/http-capabilities/search.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { lintSearchQuery, lintFromApiError, type QueryLintError } from "../../search/search-query-model.ts";
import { recordRecentQuery } from "../../search/search-history.ts";
import { createSourceSearchRefresh } from "../../search/source-search-refresh.ts";
import { createSearchRefresh, searchNeedsRefinement, searchWarming } from "../../search/search-refresh.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { createDebounced } from "../../ui/debounced.ts";

type Options = {
  originProjectId: () => string | null;
  residentLive: () => boolean;
  query: () => string;
  matchOptions: () => SearchMatchOptions;
  stopPreview: () => void;
  setPreviewLint: (value: QueryLintError | null) => void;
  clearReplace: () => void;
  updateRecent: () => void;
  reportError: (error: unknown) => void;
  replaceMode: () => boolean;
  previewCurrent: () => boolean;
  runReplacePreview: () => unknown;
};

type SearchPage = {
  response: SearchResponse;
  start: number;
};

export function createSearchResultsController(options: Options) {
  const [searchFrame, setSearchFrame] = createSignal<{
    pages: SearchPage[];
    index: number;
    scope?: { query: string; projectId?: string; options: SearchMatchOptions; budget: "interactive" | "complete" };
  }>({ pages: [], index: 0 });
  const searchPages = () => searchFrame().pages;
  const pageIndex = () => searchFrame().index;
  const currentPage = () => searchPages()[pageIndex()] ?? null;
  const response = () => currentPage()?.response ?? null;
  const indexing = () => { const result = response(); return result ? searchWarming(result) : false; };
  const [loading, setLoading] = createSignal(false);
  const [refining, setRefining] = createSignal(false);
  const [loadingPage, setLoadingPage] = createSignal(false);
  const [pageLoadFailed, setPageLoadFailed] = createSignal(false);
  const [searchFailed, setSearchFailed] = createSignal(false);
  const [apiLint, setApiLint] = createSignal<QueryLintError | null>(null);
  const [selectedHit, setSelectedHit] = createSignal<SearchHit | null>(null);
  const [selectedHitKey, setSelectedHitKey] = createSignal<string | null>(null);

  let searchGeneration = 0;
  let searchController: AbortController | undefined;
  const searchRefresh = createSearchRefresh();
  let foregroundSearchGeneration: number | null = null;
  onCleanup(() => { searchGeneration++; options.stopPreview(); searchController?.abort(); searchRefresh.clear(); });
  const runSearch = async (
    q: string,
    presentation: "foreground" | "background" = "foreground",
  ) => {
    if (!options.residentLive()) return;
    searchController?.abort();
    const controller = new AbortController();
    searchController = controller;
    const generation = ++searchGeneration;
    setRefining(presentation === "background");
    setLoadingPage(false);
    const current = () => generation === searchGeneration;
    const trimmed = q.trim();
    if (!trimmed) {
      setRefining(false);
      foregroundSearchGeneration = null;
      setLoading(false);
      setLoadingPage(false);
      setPageLoadFailed(false);
      setSearchFailed(false);
      setSearchFrame({ pages: [], index: 0 });
      setApiLint(null);
      options.setPreviewLint(null);
      setSelectedHit(null);
      setSelectedHitKey(null);
      options.clearReplace();
      return;
    }
    // Local lint is advisory.
    const optimistic = lintSearchQuery(trimmed);
    setApiLint(optimistic);
    const client = getLycaonClient();
    if (!client) {
      setRefining(false);
      foregroundSearchGeneration = null;
      setLoading(false);
      return;
    }
    if (presentation === "foreground") {
      foregroundSearchGeneration = generation;
      setSearchFailed(false);
      setLoading(true);
    }
    try {
      const match = options.matchOptions();
      const projectId = options.originProjectId() ?? undefined;
      const budget = presentation === "foreground" ? "interactive" : "complete";
      const result = await client.search(
        trimmed,
        projectId,
        { ...match, budget },
        controller.signal,
      );
      if (!current()) return;
      batch(() => {
        setSearchFrame({
          pages: [{ response: result, start: 0 }],
          index: 0,
          scope: { query: trimmed, projectId, options: match, budget },
        });
        setPageLoadFailed(false);
        setSearchFailed(false);
        setApiLint(null);
        if (presentation === "foreground") {
          setSelectedHit(null);
          setSelectedHitKey(null);
        } else if (selectedHitKey()) {
          const selected = result.hits.find(
            (hit) => searchHitKey(hit) === selectedHitKey(),
          );
          setSelectedHit(selected ?? null);
          if (!selected) setSelectedHitKey(null);
        }
      });
      if (presentation === "foreground") {
        recordRecentQuery(trimmed, options.originProjectId());
        options.updateRecent();
      }
      if (presentation === "foreground" && searchNeedsRefinement(result)) {
        queueMicrotask(() => {
          if (options.residentLive() && current()) void runSearch(trimmed, "background");
        });
      } else {
        searchRefresh.schedule(result, () =>
          options.residentLive() && current() && void runSearch(trimmed, "background"),
        );
      }
    } catch (err) {
      if (!current()) return;
      if (presentation === "background") {
        searchRefresh.clear();
        setSearchFailed(true);
        options.reportError(err);
        return;
      }
      // Failed queries clear retained results.
      setSearchFrame({ pages: [], index: 0 });
      if (
        err instanceof LycaonApiError &&
        (err.code === "search_query_invalid" ||
          err.code === "search_pattern_invalid")
      ) {
        setApiLint(lintFromApiError(err));
      } else {
        setSearchFailed(true);
        options.reportError(err);
      }
    } finally {
      if (current()) setRefining(false);
      if (foregroundSearchGeneration === generation) {
        foregroundSearchGeneration = null;
        setLoading(false);
      }
    }
  };

  createSourceSearchRefresh(
    () => options.residentLive() && !options.replaceMode() && !!options.query().trim(),
    () => runSearch(options.query(), "background"),
  );

  let resultsViewport: HTMLDivElement | undefined;
  const scrollCurrentPageToTop = () => {
    queueMicrotask(() => {
      const motion = resultsViewport ? scrollportMotionForViewport(resultsViewport) : undefined;
      motion?.cancelApplicationMotion();
      motion?.commit(0, "jump");
    });
  };

  const pageEnd = () => (currentPage()?.start ?? 0) + (response()?.hits.length ?? 0);
  const showPage = (index: number) => {
    if (!searchPages()[index]) return false;
    setSearchFrame((frame) => ({ ...frame, index }));
    setSelectedHit(null);
    setSelectedHitKey(null);
    setPageLoadFailed(false);
    scrollCurrentPageToTop();
    return true;
  };

  const loadNextPage = async (): Promise<boolean> => {
    const nextIndex = pageIndex() + 1;
    if (searchPages()[nextIndex]) return showPage(nextIndex);
    const start = pageEnd();
    const cursor = response()?.next_cursor?.trim();
    const client = getLycaonClient();
    const scope = searchFrame().scope;
    if (!cursor || !client || !scope || loading() || loadingPage()) return false;
    const generation = searchGeneration;
    setPageLoadFailed(false);
    setLoadingPage(true);
    try {
      const page = await client.search(
        scope.query,
        scope.projectId,
        { ...scope.options, budget: scope.budget, cursor },
        searchController?.signal,
      );
      if (generation !== searchGeneration) return false;
      setSearchFrame((frame) => ({ ...frame, index: nextIndex,
        pages: [...frame.pages.slice(0, nextIndex), { response: page, start }] }));
      setSelectedHit(null);
      setSelectedHitKey(null);
      scrollCurrentPageToTop();
      return true;
    } catch (err) {
      if (
        generation === searchGeneration &&
        err instanceof LycaonApiError &&
        err.code === "cursor_generation_expired"
      ) {
        setLoadingPage(false);
        await runSearch(options.query());
        return false;
      }
      if (generation === searchGeneration) {
        setPageLoadFailed(true);
        options.reportError(err);
      }
      return false;
    } finally {
      if (generation === searchGeneration) setLoadingPage(false);
    }
  };

  createEffect(
    on(
      options.residentLive,
      (live) => {
        if (!live) {
          options.stopPreview();
          searchRefresh.clear();
          searchGeneration++;
          searchController?.abort();
          foregroundSearchGeneration = null;
          setLoading(false);
          setRefining(false);
          setLoadingPage(false);
          return;
        }
        if (options.replaceMode() && !options.previewCurrent()) void options.runReplacePreview();
        const result = response();
        const requestChanged = searchRequestChanged();
        if ((result && searchWarming(result)) || requestChanged) {
          void runSearch(options.query(), requestChanged ? "foreground" : "background");
        }
      },
      { defer: true },
    ),
  );

  const scheduleSearch = createDebounced(() => void runSearch(options.query()));
  // Debounce only material request changes.
  const searchRequestKey = createMemo(() =>
    JSON.stringify({
      query: options.query(),
      projectId: options.originProjectId() ?? undefined,
      options: options.matchOptions(),
    }),
  );
  const presentedSearchKey = () => {
    const scope = searchFrame().scope;
    return scope
      ? JSON.stringify({
          query: scope.query,
          projectId: scope.projectId,
          options: scope.options,
        })
      : null;
  };
  const searchRequestChanged = () =>
    Boolean(
      options.query().trim() &&
      !apiLint() &&
      !searchFailed() &&
      searchRequestKey() !== presentedSearchKey(),
    );
  const coldSearchPending = () =>
    loading() && response() == null;
  const retainingResults = () =>
    loading() && response() != null;
  const showRetainedWait = createPresentationWaiting(retainingResults);
  let lastScheduledSearchKey: string | undefined;
  createEffect(
    on(searchRequestKey, (key) => {
      if (key === lastScheduledSearchKey) return;
      lastScheduledSearchKey = key;
      searchGeneration++;
      searchController?.abort();
      foregroundSearchGeneration = null;
      setLoading(false);
      setRefining(false);
      setSearchFailed(false);
      searchRefresh.reset();
      scheduleSearch();
    }),
  );

  return { searchFrame, searchPages, pageIndex, currentPage, response, indexing,
    loading, refining, loadingPage, pageLoadFailed, searchFailed, apiLint, setApiLint,
    selectedHit, setSelectedHit, selectedHitKey, setSelectedHitKey, runSearch,
    showPage, loadNextPage, searchRequestChanged, coldSearchPending, retainingResults,
    showRetainedWait, setResultsViewport: (el: HTMLDivElement) => { resultsViewport = el; },
    resultsViewport: () => resultsViewport,
  };
}

export type SearchResultsController = ReturnType<typeof createSearchResultsController>;
