import { createSearchReplacement } from "./search-replacement.ts";
import { searchCoverageNote } from "../../search/search-status.ts";
import { createSourceSearchRefresh } from "../../search/source-search-refresh.ts";
import { createSearchRefresh, searchNeedsRefinement, searchWarming } from "../../search/search-refresh.ts";
import { createEffect, batch, createMemo, createSignal, For, on, onMount, onCleanup, Show } from "solid-js";
import type { SearchHit, SearchReplacePreviewRequest, SearchResponse } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { APP_SCOPE, projectScope } from "../../notices/notice-scope.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  appendPivotFilter,
  composeQueryWithProjectScope,
  groupHitsByProject,
  lintFromApiError,
  lintSearchQuery,
  projectScopeIsCurrent,
  projectScopedCodeQuery,
  queryFilterActive,
  removeFilterToken,
  rewriteProjectScope,
  searchQueryFreeText,
  stripProjectScopeToken,
  type QueryLintError,
  toggleResultType,
  resultTypeActive,
} from "../../search/search-query-model.ts";
import { SEARCH_RESULT_TYPES, type SearchResultType } from "../../search/search-result-types.ts";
import {
  clearSearchRefinements,
  hasSearchRefinements,
  searchRefinementOccurrences,
} from "../../search/search-filter-model.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import { searchHitToNavTarget, type SearchNavTarget } from "../../search/search-hit-nav.ts";
import { listRecentQueries, recordRecentQuery } from "../../search/search-history.ts";
import {
  loadSearchMatchPrefs,
  parseGlobField,
  saveSearchMatchPrefs,
  type SearchMatchPrefs,
} from "../../search/search-match-prefs.ts";
import { selectionToApplyFiles, type ReplaceSelection } from "../../search/search-replace-selection.ts";
import { refreshBuffersAfterReplace } from "../../search/search-replace-buffers.ts";
import { consumeReplaceArm, type ReplaceArmRequest } from "../../search/replace-arm.ts";
import { revertReplaceBatch } from "../../search/replace-revert.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
import { queryScanScoped } from "../../search/search-export.ts";
import type { SearchExportFormat } from "../../api/types.ts";
import { BrowseChip } from "../browse/BrowseChip.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { DenTriggerGroup } from "../primitives/DenTriggerGroup.tsx";
import { SearchFilterChip } from "./SearchFilterChip.tsx";
import { SearchHitDetail } from "./SearchHitDetail.tsx";
import { SearchQueryBar } from "./SearchQueryBar.tsx";
import { SearchReplacePreview } from "./SearchReplacePreview.tsx";
import { SearchResultGroup, searchHitKey } from "./SearchResultGroup.tsx";
import { SEARCH_COLUMNS, SEARCH_LIST_SURFACE } from "../../search/search-columns.ts";
import { resolveColumnWidths, sortRows } from "../../list/list-columns.ts";
import { createFittedColumns } from "../../list/create-fitted-columns.ts";
import type { SortState } from "../../list/list-sort.ts";
import { resolveListPaneSort } from "../../list/list-pane-model.ts";
import { commitListSort, listPanePrefs } from "../../shell/layout-store.ts";
import { ListColumnHeader } from "../list/ListColumnHeader.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { createDebounced } from "../../ui/debounced.ts";

type Props = {
  originProjectId: string | null;
  originName: string | null;
  seed?: string;
  /** Reapplies the seed when this value changes. */
  seedSerial?: number;
  appStore: AppStore;
  projects: readonly import("../../api/types.ts").Project[];
  back?: import("../shell/StageBackChip.tsx").StageBack | null;
  replaceModeRequested?: boolean;
  onNavigate: (target: SearchNavTarget) => void;
};

const EMPTY_STATE_EXAMPLES = [
  "kind:code",
  "kind:evidence verified:false",
  "kind:message",
] as const;

type SearchPage = {
  response: SearchResponse;
  start: number;
};

function mergeArmedFlags(
  prefs: SearchMatchPrefs,
  arm: ReplaceArmRequest | null,
): SearchMatchPrefs {
  if (!arm) return prefs;
  return {
    ...prefs,
    ...(arm.regex !== undefined ? { regex: arm.regex } : {}),
    ...(arm.caseSensitive !== undefined
      ? { caseSensitive: arm.caseSensitive }
      : {}),
    ...(arm.wholeWord !== undefined ? { wholeWord: arm.wholeWord } : {}),
  };
}

export function GlobalSearchView(props: Props) {
  onMount(() => requestFirstTimeTip("project-search"));
  const residentLive = useResidentLive();

  const initialQuery = () => {
    const seed = props.seed?.trim();
    if (seed) return seed;
    return props.originProjectId ? projectScopedCodeQuery("") : "";
  };
  const [query, setQuery] = createSignal(initialQuery());
  createEffect(
    on(
      () => props.seedSerial,
      () => {
        const seed = props.seed?.trim();
        if (seed) setQuery(seed);
      },
      { defer: true },
    ),
  );
  const [searchFrame, setSearchFrame] = createSignal<{
    pages: SearchPage[];
    index: number;
    scope?: { query: string; projectId?: string; options: ReturnType<typeof matchOptions>; budget: "interactive" | "complete" };
  }>({ pages: [], index: 0 });
  const searchPages = () => searchFrame().pages;
  const pageIndex = () => searchFrame().index;
  const currentPage = () => searchPages()[pageIndex()] ?? null;
  const response = () => currentPage()?.response ?? null;
  const indexing = () => { const result = response(); return result ? searchWarming(result) : false; };
  // Attribute failures to the search origin.
  const searchReporter = () =>
    noticeReporterFor(
      props.originProjectId ? projectScope(props.originProjectId) : APP_SCOPE,
    );

  const [loading, setLoading] = createSignal(false);
  const [refining, setRefining] = createSignal(false);
  const [loadingPage, setLoadingPage] = createSignal(false);
  const [pageLoadFailed, setPageLoadFailed] = createSignal(false);
  const [searchFailed, setSearchFailed] = createSignal(false);
  const [apiLint, setApiLint] = createSignal<QueryLintError | null>(null);
  // Search and preview errors clear independently.
  const [previewLint, setPreviewLint] = createSignal<QueryLintError | null>(null);
  const queryBarLint = () => apiLint() ?? previewLint();
  const [selectedHit, setSelectedHit] = createSignal<SearchHit | null>(null);
  const [selectedHitKey, setSelectedHitKey] = createSignal<string | null>(null);

  const [recent, setRecent] = createSignal(listRecentQueries());
  const [exportTruncated, setExportTruncated] = createSignal(false);
  const [resultsRoot, setResultsRoot] = createSignal<HTMLElement | null>(null);
  // Apply one-shot replace flags.
  const arm = consumeReplaceArm();
  const [matchPrefs, setMatchPrefs] = createSignal<SearchMatchPrefs>(
    mergeArmedFlags(loadSearchMatchPrefs(), arm),
  );
  const [replaceMode, setReplaceMode] = createSignal(
    arm != null || !!props.replaceModeRequested,
  );
  const [replacement, setReplacement] = createSignal(arm?.replacement ?? "");
  const [renameFrom, setRenameFrom] = createSignal<string | null>(
    arm?.renameFrom ?? null,
  );
  const { replacePreview, setReplacePreview, reviewedRequest, replaceSelection, setReplaceSelection,
    replaceApplying, replaceSummary, setReplaceSummary, previewPreparing, previewNotice,
    stopPreview, cancelPreview, runReplacePreview, applyReplaceFiles, observePreviewChanges,
  } = createSearchReplacement({
    originProjectId: () => props.originProjectId, query, replaceMode, residentLive, renameFrom,
    replaceRequest: () => replaceRequest(), previewRequestKey: () => previewRequestKey(),
    previewCurrent: () => previewCurrent(), setApiLint, setPreviewLint,
    reportError: (error) => searchReporter().reportError(error), runSearch: (value) => { void runSearch(value); },
  });

  bindFindableView({
    id: "search-results",
    root: resultsRoot,
  });

  const rootRefsForProject = (projectId: string) => {
    const project = props.projects.find((p) => p.id === projectId.trim());
    return (project?.roots ?? []).map((r) => ({
      id: r.id,
      path: r.path,
      is_primary: r.is_primary,
    }));
  };

  // Ignore superseded responses.
  let searchGeneration = 0;
  let searchController: AbortController | undefined;
  const searchRefresh = createSearchRefresh();
  let foregroundSearchGeneration: number | null = null;
  onCleanup(() => { searchGeneration++; stopPreview(); searchController?.abort(); searchRefresh.clear(); });

  const matchOptions = () => {
    const prefs = matchPrefs();
    return {
      regex: prefs.regex,
      caseSensitive: prefs.caseSensitive,
      wholeWord: prefs.wholeWord,
      include: parseGlobField(prefs.include),
      exclude: parseGlobField(prefs.exclude),
    };
  };

  const replaceRequest = createMemo<SearchReplacePreviewRequest>(() => {
    const flags = matchOptions();
    return {
      query: query().trim(),
      replacement: replacement(),
      origin_project_id: props.originProjectId ?? undefined,
      regex: !!flags.regex,
      case_sensitive: !!flags.caseSensitive,
      whole_word: !!flags.wholeWord,
      include: flags.include,
      exclude: flags.exclude,
    };
  });
  const previewRequestKey = createMemo(() => JSON.stringify({
    request: replaceRequest(),
    active: replaceMode(),
  }));
  const previewCurrent = () =>
    replaceMode() && replacePreview()?.state !== "preparing" && reviewedRequest()?.key === previewRequestKey();

  const persistMatchPrefs = (next: SearchMatchPrefs) => {
    setMatchPrefs(next);
    saveSearchMatchPrefs(next);
  };

  const patchMatchPrefs = (patch: Partial<SearchMatchPrefs>) => {
    persistMatchPrefs({ ...matchPrefs(), ...patch });
  };

  const runSearch = async (
    q: string,
    presentation: "foreground" | "background" = "foreground",
  ) => {
    if (!residentLive()) return;
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
      setPreviewLint(null);
      setSelectedHit(null);
      setSelectedHitKey(null);
      setReplacePreview(null);
      setReplaceSelection(null);
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
      const options = matchOptions();
      const projectId = props.originProjectId ?? undefined;
      const budget = presentation === "foreground" ? "interactive" : "complete";
      const result = await client.search(
        trimmed,
        projectId,
        { ...options, budget },
        controller.signal,
      );
      if (!current()) return;
      batch(() => {
        setSearchFrame({
          pages: [{ response: result, start: 0 }],
          index: 0,
          scope: { query: trimmed, projectId, options, budget },
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
        recordRecentQuery(trimmed, props.originProjectId);
        setRecent(listRecentQueries());
      }
      if (presentation === "foreground" && searchNeedsRefinement(result)) {
        queueMicrotask(() => {
          if (residentLive() && current()) void runSearch(trimmed, "background");
        });
      } else {
        searchRefresh.schedule(result, () =>
          residentLive() && current() && void runSearch(trimmed, "background"),
        );
      }
    } catch (err) {
      if (!current()) return;
      if (presentation === "background") {
        searchRefresh.clear();
        setSearchFailed(true);
        searchReporter().reportError(err);
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
        searchReporter().reportError(err);
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
    () => residentLive() && !replaceMode() && !!query().trim(),
    () => runSearch(query(), "background"),
  );

  let resultsViewport: HTMLDivElement | undefined;
  const scrollCurrentPageToTop = () => {
    queueMicrotask(() => {
      const motion = resultsViewport ? scrollportMotionForViewport(resultsViewport) : undefined;
      motion?.cancelApplicationMotion();
      motion?.commit(0, "jump");
    });
  };

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
        await runSearch(query());
        return false;
      }
      if (generation === searchGeneration) {
        setPageLoadFailed(true);
        searchReporter().reportError(err);
      }
      return false;
    } finally {
      if (generation === searchGeneration) setLoadingPage(false);
    }
  };

  createEffect(
    on(
      residentLive,
      (live) => {
        if (!live) {
          stopPreview();
          searchRefresh.clear();
          searchGeneration++;
          searchController?.abort();
          foregroundSearchGeneration = null;
          setLoading(false);
          setRefining(false);
          setLoadingPage(false);
          return;
        }
        if (replaceMode() && !previewCurrent()) void runReplacePreview();
        const result = response();
        const requestChanged = searchRequestChanged();
        if ((result && searchWarming(result)) || requestChanged) {
          void runSearch(query(), requestChanged ? "foreground" : "background");
        }
      },
      { defer: true },
    ),
  );

  const scheduleSearch = createDebounced(() => void runSearch(query()));
  // Debounce only material request changes.
  const searchRequestKey = createMemo(() =>
    JSON.stringify({
      query: query(),
      projectId: props.originProjectId ?? undefined,
      options: matchOptions(),
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
      query().trim() &&
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

  observePreviewChanges();


  const applyReplace = async () => {
    const sel = replaceSelection();
    if (!sel || replacePreview()?.state !== "ready") return;
    await applyReplaceFiles(selectionToApplyFiles(sel));
  };

  const applyReplaceFile = async (fileIndex: number) => {
    const sel = replaceSelection();
    if (!sel) return;
    const one = sel.files[fileIndex];
    if (!one) return;
    const subset: ReplaceSelection = {
      files: sel.files.map((f, i) =>
        i === fileIndex
          ? f
          : { ...f, checked: false, hunks: f.hunks.map(() => false) },
      ),
    };
    await applyReplaceFiles(selectionToApplyFiles(subset));
  };

  const paneSort = (): SortState =>
    resolveListPaneSort(listPanePrefs(SEARCH_LIST_SURFACE));
  const columnWidths = () =>
    resolveColumnWidths(
      SEARCH_COLUMNS,
      listPanePrefs(SEARCH_LIST_SURFACE).columnWidths,
    );
  // Grid tracks and row cells share one set of fitted columns.
  const fitted = createFittedColumns({
    columns: () => SEARCH_COLUMNS,
    widths: columnWidths,
  });

  // Sorting stays within project groups.
  const groups = () => {
    const grouped = groupHitsByProject(
      response()?.hits ?? [],
      props.originProjectId,
    );
    const sort = paneSort();
    if (!sort) return grouped;
    return grouped.map((group) => ({
      ...group,
      hits: sortRows(group.hits, SEARCH_COLUMNS, sort),
    }));
  };

  const hitCount = () => response()?.hits?.length ?? 0;
  const pageStart = () => currentPage()?.start ?? 0;
  const pageEnd = () => pageStart() + hitCount();
  const hasPreviousPage = () => pageIndex() > 0;
  const hasNextPage = () =>
    !!searchPages()[pageIndex() + 1] || !!response()?.next_cursor;

  const highlightTerms = () => response()?.interpreted?.fts_terms ?? [];
  const codeCaseSensitive = () => searchFrame().scope?.options.caseSensitive;

  // Keyboard navigation follows rendered group order.
  const flatHits = () =>
    groups().flatMap((group) =>
      group.hits.map((hit) => ({
        hit,
        key: searchHitKey(hit),
      })),
    );

  const moveSelection = (delta: 1 | -1) => {
    const list = flatHits();
    if (list.length === 0) return;
    const current = selectedHitKey();
    const idx = list.findIndex((entry) => entry.key === current);
    const next =
      idx < 0
        ? delta === 1
          ? 0
          : list.length - 1
        : Math.min(list.length - 1, Math.max(0, idx + delta));
    if (delta === 1 && idx === list.length - 1 && response()?.next_cursor) {
      void loadNextPage().then((loaded) => {
        if (!loaded) return;
        const entry = flatHits()[0];
        if (!entry) return;
        setSelectedHit(entry.hit);
        setSelectedHitKey(entry.key);
      });
      return;
    }
    const entry = list[next];
    if (!entry) return;
    setSelectedHit(entry.hit);
    setSelectedHitKey(entry.key);
    queueMicrotask(() => {
      // Attribute selectors require quoted key escaping.
      const safe = entry.key.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
      const target = resultsRoot()?.querySelector(
        `[data-hit-key="${safe}"]`,
      );
      if (!target) return;
      const motion = resultsViewport ? scrollportMotionForViewport(resultsViewport) : undefined;
      motion?.revealElement(target);
    });
  };

  // Stage shortcuts skip controls and suggestions.
  const onStageKeyDown = (e: KeyboardEvent) => {
    if (e.defaultPrevented || e.isComposing) return;
    const target = e.target as HTMLElement | null;
    if (target?.closest("button, a, select, textarea")) return;
    if (
      target instanceof HTMLInputElement &&
      !target.classList.contains("den-search-query__input")
    ) {
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      moveSelection(1);
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      moveSelection(-1);
      return;
    }
    if (e.key === "Enter") {
      const hit = selectedHit();
      if (hit) {
        e.preventDefault();
        openSearchHit(hit, navigateHit);
      }
      return;
    }
    if (e.key === "Escape" && selectedHitKey()) {
      // First Escape clears selection; the next reaches the shell.
      e.preventDefault();
      e.stopPropagation();
      setSelectedHit(null);
      setSelectedHitKey(null);
    }
  };

  const issueNote = () => searchCoverageNote(response()) || undefined;

  const pivotQuery = (field: string, value: string) => {
    setQuery(appendPivotFilter(query(), field, value));
  };

  const navigateHit = (hit: SearchHit) => {
    props.onNavigate(searchHitToNavTarget(hit, searchQueryFreeText(query())));
  };

  const inspectorPivots = () => {
    const hit = selectedHit();
    if (!hit) return [];
    const pivots: { label: string; field: string; value: string }[] = [];
    const kind = hit.hit_kind?.trim();
    if (kind) {
      pivots.push({ label: `kind:${kind}`, field: "kind", value: kind });
    }
    const source = hit.source?.trim();
    if (source && source !== kind) {
      pivots.push({ label: `source:${source}`, field: "source", value: source });
    }
    const path = hit.path?.trim();
    if (path) {
      const label = hit.line && hit.line > 0 ? `${path}:${hit.line}` : path;
      pivots.push({ label, field: "path", value: path });
    }
    return pivots.filter(
      (pivot) => !queryFilterActive(query(), pivot.field, pivot.value),
    );
  };

  const selectHit = (hit: SearchHit, key: string) => {
    if (selectedHitKey() === key) {
      setSelectedHit(null);
      setSelectedHitKey(null);
      return;
    }
    setSelectedHit(hit);
    setSelectedHitKey(key);
  };

  const runExport = async (format: SearchExportFormat) => {
    const trimmed = query().trim();
    if (!trimmed) return;
    const client = getLycaonClient();
    if (!client) return;
    setExportTruncated(false);
    try {
      // Live search flags, so the export matches the screen.
      const result = await client.exportSearchResults(
        trimmed,
        format,
        props.originProjectId ?? undefined,
        matchOptions(),
      );
      await downloadExport(result.blob, result.filename);
      setExportTruncated(result.truncated);
    } catch (err) {
      if (
        err instanceof LycaonApiError &&
        (err.code === "search_query_invalid" ||
          err.code === "search_pattern_invalid")
      ) {
        setApiLint(lintFromApiError(err));
        return;
      }
      searchReporter().reportError(err);
    }
  };

  const exportDisabled = () => !query().trim() || !!apiLint();
  const emptySearchTitle = () => response()?.exhaustive === false ? "No results found so far" : "No results";

  const hitSummary = () => {
    const result = response();
    if (!result) return "No results";
    const suffix = result.count_relation === "lower_bound" ? "+" : "";
    if (result.total_hits === 1) return suffix ? "1+ results" : "1 result";
    const range = `${(pageStart() + 1).toLocaleString()}–${pageEnd().toLocaleString()}`;
    return `${range} of ${result.total_hits.toLocaleString()}${suffix} results`;
  };

  const selectedHitAnnouncement = () => {
    const hit = selectedHit();
    if (!hit) return "";
    const display = searchHitDisplay(hit);
    const location = display.context ? `, ${display.context}` : "";
    return `Selected ${display.kindLabel}: ${display.title}${location}. Press Enter to open.`;
  };

  const hitsMeta = () => {
    if (!query().trim()) return null;
    return (
      <span class="den-search__hits" data-testid="search-hits-count">
        {loading()
          ? "Searching…"
          : searchFailed()
            ? "Search failed"
            : hitCount() === 0
              ? emptySearchTitle()
              : hitSummary()}
      </span>
    );
  };

  const visibleFilterSegments = () => searchRefinementOccurrences(query());

  const editQueryPreservingScope = (edit: (editable: string) => string) => {
    const editable = stripProjectScopeToken(query());
    setQuery(
      composeQueryWithProjectScope(
        edit(editable),
        projectScopeIsCurrent(query()),
      ),
    );
  };

  const toggleSearchType = (resultType: SearchResultType) => {
    editQueryPreservingScope((editable) =>
      toggleResultType(editable, resultType),
    );
  };

  const removeExtraFilter = (occurrence: {
    field: string;
    value: string;
    negated: boolean;
  }) => {
    editQueryPreservingScope((editable) =>
      removeFilterToken(
        editable,
        occurrence.field,
        occurrence.value,
        occurrence.negated,
      ),
    );
  };

  const clearFilters = () => {
    setQuery(clearSearchRefinements(query()));
    patchMatchPrefs({ include: "", exclude: "" });
  };

  return (
    <section
      class="den-search-takeover"
      data-testid="global-search-view"
      aria-label="Search"
      onKeyDown={onStageKeyDown}
    >
      <span
        class="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        data-testid="search-selection-status"
      >
        {selectedHitAnnouncement()}
      </span>
      <BrowseStagePanel
        appStore={props.appStore}
        requireClient={false}
        scrollHost="content"
        testId="browse-stage-panel"
        back={props.back}
        primary={
          <SearchQueryBar
            query={query()}
            originProjectId={props.originProjectId}
            originName={props.originName}
            facets={response()?.facets ?? []}
            recentQueries={recent().map((entry) => entry.query)}
            apiLint={queryBarLint()}
            onQueryChange={setQuery}
            match={{
              caseSensitive: matchPrefs().caseSensitive,
              wholeWord: matchPrefs().wholeWord,
              regex: matchPrefs().regex,
            }}
            onMatchChange={(patch) => patchMatchPrefs(patch)}
            replaceMode={replaceMode()}
            onReplaceModeChange={(on) => {
              setReplaceMode(on);
              if (!on) {
                setReplacePreview(null);
                setReplaceSelection(null);
                setReplaceSummary(null);
                setPreviewLint(null);
              }
            }}
            replacement={replacement()}
            onReplacementChange={setReplacement}
          />
        }
        chips={
          <div class="den-search-chrome-chips">
            <div class="den-search-chrome-chips__filters">
              <div
                class="den-browse-filter-toggles den-search-kind-filters"
                role="group"
                aria-label="Result types"
                data-testid="search-type-filters"
              >
                <For each={SEARCH_RESULT_TYPES}>
                  {(resultType) => {
                    const on = () => resultTypeActive(query(), resultType);
                    return (
                      <button
                        type="button"
                        class="den-browse-filter-toggle"
                        classList={{ "den-browse-filter-toggle--on": on() }}
                        data-testid={`search-type-${resultType.id}`}
                        aria-pressed={on()}
                        onClick={() => toggleSearchType(resultType)}
                      >
                        {resultType.label}
                      </button>
                    );
                  }}
                </For>
              </div>

              <Show when={visibleFilterSegments().length > 0}>
                <div
                  class="den-search-active-filters"
                  role="group"
                  aria-label="Active filters"
                >
                  <For each={visibleFilterSegments()}>
                    {(occurrence) => (
                      <BrowseChip
                        label={`${occurrence.negated ? "NOT " : ""}${occurrence.field}:${occurrence.value}`}
                        active
                        testId={`search-active-filter-${occurrence.field}-${occurrence.value}`}
                        onRemove={() => removeExtraFilter(occurrence)}
                      />
                    )}
                  </For>
                </div>
              </Show>
            </div>

            <div class="den-search-chrome-tools">
              <DenTriggerGroup ariaLabel="Search tools" testId="search-tools">
                <SearchFilterChip
                  facets={response()?.facets ?? []}
                  query={query()}
                  onQueryChange={setQuery}
                  include={matchPrefs().include}
                  exclude={matchPrefs().exclude}
                  onIncludeChange={(value) => patchMatchPrefs({ include: value })}
                  onExcludeChange={(value) => patchMatchPrefs({ exclude: value })}
                />
                {/* The disabled Recent control preserves toolbar width. */}
                <BrowseOverflowMenu
                  testId="search-recent-menu"
                  label="Recent"
                  disabled={recent().length === 0}
                  items={recent()
                    .slice(0, 6)
                    .map((entry) => ({
                      label: entry.query,
                      onSelect: () => setQuery(entry.query),
                    }))}
                />
                <BrowseOverflowMenu
                  testId="search-overflow"
                  label="Export"
                  items={[
                    {
                      label: "JSONL",
                      testId: "search-export-jsonl",
                      disabled: exportDisabled(),
                      onSelect: () => void runExport("jsonl"),
                    },
                    {
                      label: "CSV",
                      testId: "search-export-csv",
                      disabled: exportDisabled(),
                      onSelect: () => void runExport("csv"),
                    },
                    {
                      label: "SARIF",
                      testId: "search-export-sarif",
                      disabled: exportDisabled() || !queryScanScoped(query()),
                      onSelect: () => void runExport("sarif"),
                    },
                  ]}
                />
              </DenTriggerGroup>
            </div>

          </div>
        }
        meta={hitsMeta() ?? undefined}
        detailSurface={SEARCH_LIST_SURFACE}
        detail={(() => {
          const hit = selectedHit();
          if (!hit) return null;
          return (
            <SearchHitDetail
              hit={hit}
              highlightTerms={highlightTerms()}
              codeCaseSensitive={codeCaseSensitive()}
              pivots={inspectorPivots()}
              rootRefs={rootRefsForProject(hit.project_id)}
              onPivot={pivotQuery}
              onNavigate={() => navigateHit(hit)}
              onClose={() => {
                setSelectedHit(null);
                setSelectedHitKey(null);
              }}
            />
          );
        })()}
      >
        <div
          class="den-search-stage-boundary den-retained-presentation"
          data-testid="search-stage-boundary"
          data-retained={retainingResults() ? "true" : "false"}
          aria-hidden={retainingResults() ? "true" : undefined}
          inert={retainingResults() ? true : undefined}
        >
          <Scrollport
            class="den-search-results-scroll"
            contentClass="den-search-results-scroll__content"
            axis="both"
            data-testid="search-results-list"
            viewportRef={(el) => (resultsViewport = el)}
          >
            <Show when={exportTruncated()}>
              <p
                class="den-search-status den-search-export__truncated"
                data-testid="search-export-truncated"
              >
                Export stopped at its safety limit. Refine the query for a complete
                file.
              </p>
            </Show>

          <Show when={replaceMode()}>
            <SearchReplacePreview
              preview={replacePreview()}
              selection={replaceSelection()}
              onSelectionChange={setReplaceSelection}
              applying={replaceApplying()}
              previewCurrent={previewCurrent()}
              applySummary={replaceSummary()}
              applyAllDisabled={replacePreview()?.state !== "ready"}
              preparing={previewPreparing()}
              preparationNotice={previewNotice()}
              onCancelPreparation={cancelPreview}
              onRetryPreparation={query().trim() ? () => void runReplacePreview() : undefined}
              renameFrom={renameFrom()}
              onApply={() => void applyReplace()}
              onApplyFile={(index) => void applyReplaceFile(index)}
              onRevertBatch={async (summary) => {
                const client = getLycaonClient();
                const projectId = props.originProjectId;
                if (!client || !projectId) return { ok: 0, skipped: [], undos: [] };
                const report = await revertReplaceBatch(
                  client,
                  projectId,
                  summary.batchId,
                  summary.applied,
                );
                await refreshBuffersAfterReplace(
                  client,
                  projectId,
                  summary.applied,
                );
                void runSearch(query());
                return report;
              }}
              onCancel={() => {
                setReplaceMode(false);
                setRenameFrom(null);
                setReplacePreview(null);
                setReplaceSelection(null);
                setReplaceSummary(null);
              }}
              onSearchAgain={() => {
                setReplaceSummary(null);
                void runReplacePreview();
                void runSearch(query());
              }}
            />
          </Show>

          <Show
            when={
              !replaceMode() &&
              !loading() &&
              response() == null &&
              !searchFailed()
            }
          >
            <div class="den-search__empty" data-testid="search-empty-state">
              <p class="den-search__empty-title">
                Search code, messages, and web
              </p>
              <p class="den-search__empty-hint">
                Combine keywords with filters to narrow by kind, path, or
                session.
              </p>
              <div
                class="den-search__empty-examples"
                role="group"
                aria-label="Example queries"
              >
                <For each={EMPTY_STATE_EXAMPLES}>
                  {(example) => (
                    <button
                      type="button"
                      class="den-search__empty-example"
                      data-testid="search-empty-example"
                      onClick={() => setQuery(example)}
                    >
                      {example}
                    </button>
                  )}
                </For>
              </div>
              <Show when={recent().length > 0}>
                <div class="den-search__empty-recent">
                  <p class="den-search__empty-recent-label">Recent</p>
                  <For each={recent().slice(0, 5)}>
                    {(entry) => (
                      <button
                        type="button"
                        class="den-search__empty-recent-row"
                        data-testid="search-empty-recent"
                        onClick={() => setQuery(entry.query)}
                      >
                        {entry.query}
                      </button>
                    )}
                  </For>
                </div>
              </Show>
              <p class="den-search__empty-keys" aria-hidden="true">
                <kbd>↑</kbd>
                <kbd>↓</kbd> navigate · <kbd>↵</kbd> open
              </p>
            </div>
          </Show>

          <Show when={!replaceMode() && coldSearchPending()}>
            <div class="den-search__empty" data-testid="search-loading-state">
              <p class="den-search__empty-title">Searching…</p>
              <p class="den-search__empty-hint">Checking the selected scope.</p>
            </div>
          </Show>

          <Show
            when={
              !replaceMode() &&
              !loading() &&
              searchFailed()
            }
          >
            <div class="den-search__empty" data-testid="search-failed">
              <p class="den-search__empty-title">Search couldn't run</p>
              <p class="den-search__empty-hint">
                The engine didn't answer. The query is kept — try again.
              </p>
              <div class="den-search__empty-examples">
                <button
                  type="button"
                  class="den-search__empty-example"
                  data-testid="search-failed-retry"
                  onClick={() => void runSearch(query())}
                >
                  Retry
                </button>
              </div>
            </div>
          </Show>

          <Show when={!replaceMode() && !loading() && !searchRequestChanged() && !searchFailed() && response() && hitCount() === 0 && (refining() || indexing())}>
            <div class="den-search__empty" data-testid="search-refining-state">
              <p class="den-search__empty-title">Searching…</p>
              <p class="den-search__empty-hint">
                {indexing() ? "Results will appear here as files become searchable." : "Checking the rest of the selected scope."}
              </p>
            </div>
          </Show>

          <Show
            when={
              !replaceMode() &&
              !searchFailed() &&
              !refining() &&
              !indexing() &&
              query().trim() &&
              response() &&
              hitCount() === 0 &&
              !apiLint()
            }
          >
            <div class="den-search__empty" data-testid="search-no-results">
              <p class="den-search__empty-title">{emptySearchTitle()}</p>
              <p class="den-search__empty-hint">
                {response()?.exhaustive === false
                  ? "Some of the selected scope could not be searched."
                  : projectScopeIsCurrent(query())
                  ? "Nothing matched in this project."
                  : "Nothing matched this query."}
              </p>
              <Show when={response()?.interpreted?.dependency_trees_excluded}>
                <p
                  class="den-search__empty-hint"
                  data-testid="search-no-results-dependency-note"
                >
                  Dependency and build trees were skipped — add a path: filter
                  to search inside one.
                </p>
              </Show>
              <div class="den-search__empty-examples">
                <Show when={projectScopeIsCurrent(query())}>
                  <button
                    type="button"
                    class="den-search__empty-example"
                    data-testid="search-no-results-everything"
                    onClick={() =>
                      setQuery(rewriteProjectScope(query(), "everything"))
                    }
                  >
                    Search everything
                  </button>
                </Show>
                <Show
                  when={
                    hasSearchRefinements(
                      query(),
                      matchPrefs().include,
                      matchPrefs().exclude,
                    )
                  }
                >
                  <button
                    type="button"
                    class="den-search__empty-example"
                    data-testid="search-no-results-clear-filters"
                    onClick={clearFilters}
                  >
                    Clear filters
                  </button>
                </Show>
              </div>
            </div>
          </Show>

          <Show when={!replaceMode() && hitCount() > 0}>
            <ListColumnHeader
              surface={SEARCH_LIST_SURFACE}
              columns={fitted.columns()}
              widths={columnWidths()}
              measureRef={fitted.measure}
              sort={paneSort()}
              onSort={(state) => void commitListSort(SEARCH_LIST_SURFACE, state)}
              ariaLabel="Result columns"
              testId="search-results-header"
            />
          </Show>

            <Show when={!replaceMode()}>
              <div ref={setResultsRoot}>
                <SearchResultGroup
                  groups={groups()}
                  selectedHitKey={selectedHitKey()}
                  highlightTerms={highlightTerms()}
                  codeCaseSensitive={codeCaseSensitive()}
                  issueNote={issueNote()}
                  busy={refining()}
                  gridTemplate={fitted.template()}
                  showsLocation={fitted.shows("location")}
                  rootRefsForProject={rootRefsForProject}
                  onSelectHit={selectHit}
                  onPivot={pivotQuery}
                  onNavigate={navigateHit}
                />
              </div>
            </Show>
          </Scrollport>
          <Show when={!replaceMode() && (loadingPage() || hitCount() > 0)}>
            <div
              class="den-search-results__footer"
              data-testid="search-results-footer"
            >
              <span role="status" aria-live="polite">
                {loadingPage()
                  ? "Loading next page…"
                  : pageLoadFailed()
                    ? "Couldn't load the next page."
                    : hitSummary()}
              </span>
              <Show when={hasPreviousPage()}>
                <DenButton
                  variant="secondary"
                  compact
                  class="den-search-results__page"
                  data-testid="search-page-previous"
                  disabled={loadingPage()}
                  onClick={() => showPage(pageIndex() - 1)}
                >
                  Previous
                </DenButton>
              </Show>
              <Show when={hasNextPage()}>
                <DenButton
                  variant="secondary"
                  compact
                  class="den-search-results__page"
                  data-testid="search-page-next"
                  disabled={loadingPage()}
                  onClick={() => void loadNextPage()}
                >
                  {loadingPage()
                    ? "Loading…"
                    : pageLoadFailed()
                      ? "Retry"
                      : "Next"}
                </DenButton>
              </Show>
            </div>
          </Show>
        </div>
      </BrowseStagePanel>
      <Show when={showRetainedWait()}>
        <div class="den-presentation-wait" role="status">
          Updating results…
        </div>
      </Show>
    </section>
  );
}
