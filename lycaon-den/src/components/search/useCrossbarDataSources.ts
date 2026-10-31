import { batch, createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import type {
  ContributionSearchResult,
  ContributionSearchSource,
  SearchHit,
  SearchResponse,
  SourceIndexRootCoverage,
  SourceRevisionComparison,
  SourceSearchLocation,
  SourceSearchOutside,
} from "../../api/types.ts";
import { looksLikeRevisionSpec, resolveRevisionComparisons } from "../../files/review/revision-diffs.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { hostSharesDevice } from "../../platform/connection/host-identity.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import {
  inventoryMru,
  orderIdleInventory,
  watchIndexedFiles,
  type InventoryFile,
} from "../../files/tree/file-inventory.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { fetchCachedSourceSymbols } from "../../files/source/source-symbols-cache.ts";
import { searchCoverageNote } from "../../search/search-status.ts";
import { createSourceSearchRefresh } from "../../search/source-search-refresh.ts";
import { type SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import {
  crossbarArm,
  type CrossbarLineTarget,
  type CrossbarSymbolRow,
} from "../../search/crossbar-model.ts";
import type { CrossbarMode } from "../../search/crossbar-modes.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { createCrossbarRequests, SEARCH_DEBOUNCE_MS } from "./crossbar-requests.ts";
import { sourceDisabledLabel } from "./CrossbarResults.tsx";

export type IndexPage = {
  query: string;
  files: InventoryFile[];
  location?: SourceSearchLocation;
  outside?: SourceSearchOutside;
};

const EMPTY_INDEX_PAGE: IndexPage = { query: "", files: [] };

export type UseCrossbarDataSourcesOptions = {
  open: () => boolean;
  originProjectId: () => string | null;
  originSessionId: () => string | null | undefined;
  query: () => string;
  mode: () => CrossbarMode;
  activeMode: () => CrossbarMode;
  matchPrefs: () => SearchMatchPrefs;
};

export function useCrossbarDataSources(options: UseCrossbarDataSourcesOptions) {
  const [hits, setHits] = createSignal<SearchHit[]>([]);
  const [searching, setSearching] = createSignal(false);
  const [indexPage, setIndexPage] = createSignal<IndexPage>(EMPTY_INDEX_PAGE);
  const [indexing, setIndexing] = createSignal(false);
  const [indexError, setIndexError] = createSignal("");
  const [indexCoverage, setIndexCoverage] = createSignal<SourceIndexRootCoverage[]>([]);
  const [indexRevision, setIndexRevision] = createSignal(0);
  const [symbolRows, setSymbolRows] = createSignal<CrossbarSymbolRow[]>([]);
  const [sourceResults, setSourceResults] = createSignal<ContributionSearchResult[]>([]);
  const [sourceError, setSourceError] = createSignal("");
  const [searchErrorNote, setSearchErrorNote] = createSignal("");
  const [searchCoverage, setSearchCoverage] = createSignal<Pick<SearchResponse, "issues" | "exhaustive"> | null>(null);
  const [revisions, setRevisions] = createSignal<{ query: string; comparisons: SourceRevisionComparison[] }>({
    query: "",
    comparisons: [],
  });

  const searchBusy = () => searching() || indexing();
  const projectId = () => options.originProjectId()?.trim() ?? "";
  const sessionId = () => options.originSessionId()?.trim() || undefined;
  const hasLocalFileArm = () => projectId().length > 0;

  const searchSources = () => contributionFrame()?.search_sources ?? [];
  const activeSource = createMemo<ContributionSearchSource | null>(() => {
    // Accept auto-capitalized source prefixes.
    const prefix = options
      .query()
      .trimStart()
      .match(/^([a-z0-9]+(?:-[a-z0-9]+)*):/i)?.[1]
      ?.toLowerCase();
    return searchSources().find((source) => source.prefix === prefix) ?? null;
  });

  const activeSourceQuery = () => {
    const raw = options.query().trimStart();
    const separator = raw.indexOf(":");
    return separator < 0 ? "" : raw.slice(separator + 1).trim();
  };

  const { runSearch, scheduleSearch, cancelWhenClosed, dispose: disposeSearchRequests } = createCrossbarRequests({
    open: options.open,
    originProjectId: options.originProjectId,
    mode: options.mode,
    hasLocalFileArm,
    activeSource,
    matchPrefs: options.matchPrefs,
    setHits,
    setSearchCoverage,
    setSearchErrorNote,
    setSearching,
  });

  onCleanup(disposeSearchRequests);

  createSourceSearchRefresh(
    () => options.open() && !activeSource(),
    async () => {
      setIndexRevision((value) => value + 1);
      await runSearch(options.query(), "background");
    },
  );

  createEffect(
    on(
      projectId,
      () => {
        if (options.open()) {
          setHits([]);
          setSearchCoverage(null);
          scheduleSearch(options.query());
        }
      },
      { defer: true },
    ),
  );

  createEffect(() => {
    const source = activeSource();
    const sourceQuery = activeSourceQuery();
    const pid = projectId();
    const client = getLycaonClient();
    if (!options.open() || !source) {
      setSourceResults([]);
      setSourceError("");
      if (!options.open() || !activeSource()) setSearching(false);
      return;
    }
    if (!source.ready) {
      setSourceResults([]);
      setSourceError(sourceDisabledLabel(source));
      setSearching(false);
      return;
    }
    if (!pid || !client || sourceQuery.length < source.min_query_length) {
      setSourceResults([]);
      setSourceError("");
      setSearching(false);
      return;
    }
    const controller = new AbortController();
    setSearching(false);
    setSourceError("");
    const timer = setTimeout(() => {
      const frame = contributionFrame();
      if (!frame) return;
      setSearching(true);
      void client
        .searchContributionSource(
          pid,
          source.id,
          { frame_revision: frame.frame_revision, query: sourceQuery },
          controller.signal,
        )
        .then((response) => {
          if (!controller.signal.aborted) setSourceResults(response.results);
        })
        .catch((cause: unknown) => {
          if (!controller.signal.aborted) {
            setSourceResults([]);
            setSourceError(cause instanceof Error ? cause.message : String(cause));
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) setSearching(false);
        });
    }, SEARCH_DEBOUNCE_MS);
    onCleanup(() => {
      clearTimeout(timer);
      controller.abort();
    });
  });

  // The host ranks typed queries; only the idle list orders by local recency.
  const inventoryFiles = createMemo((): InventoryFile[] => {
    const pid = projectId();
    if (!pid) return [];
    const q = options.query().trim();
    const page = indexPage();
    if (crossbarArm(q).kind !== "search" || page.query !== q) return [];
    const entries = page.files;
    if (q) return entries;
    const buffers = projectFilesState(pid);
    const open = buffers.order.flatMap((key) => {
      const buffer = buffers.byKey[key];
      return buffer ? [{ rootId: buffer.rootId, path: buffer.path }] : [];
    });
    return orderIdleInventory(entries, { recent: inventoryMru(pid), open });
  });

  let inventoryQueryKey = "";
  let inventoryScopeKey = "";
  let inventoryHasResponse = false;
  const showsInventory = () =>
    projectId() &&
    !activeSource() &&
    crossbarArm(options.query()).kind === "search" &&
    (options.activeMode() === "everything" || options.activeMode() === "files");

  const coverageNote = () => {
    const errors = [searchErrorNote(), showsInventory() ? indexError() : ""].filter(Boolean);
    return (
      [...new Set(errors)].join(" ") ||
      searchCoverageNote(searchCoverage(), showsInventory() ? indexCoverage() : [])
    );
  };

  createEffect(() => {
    if (
      !options.open() ||
      activeSource() ||
      (options.activeMode() !== "everything" && options.activeMode() !== "files")
    ) {
      return;
    }
    const pid = projectId();
    if (!pid) {
      inventoryQueryKey = "";
      inventoryScopeKey = "";
      inventoryHasResponse = false;
      setIndexPage(EMPTY_INDEX_PAGE);
      setIndexError("");
      setIndexCoverage([]);
      return;
    }
    const sourceQuery = options.query().trim();
    const sid = sessionId();
    const scopeKey = JSON.stringify([pid, sid]);
    if (scopeKey !== inventoryScopeKey) {
      inventoryScopeKey = scopeKey;
      setIndexError("");
      setIndexCoverage([]);
    }
    const key = JSON.stringify([pid, sid, sourceQuery]);
    if (key !== inventoryQueryKey) {
      inventoryQueryKey = key;
      inventoryHasResponse = false;
      setIndexPage(EMPTY_INDEX_PAGE);
    }
    indexRevision();
    if (crossbarArm(sourceQuery).kind !== "search") {
      setIndexPage(EMPTY_INDEX_PAGE);
      return;
    }
    const client = getLycaonClient();
    if (!client) return;
    const controller = new AbortController();
    setIndexing(false);
    const timer = setTimeout(() => {
      if (!inventoryHasResponse) setIndexing(true);
      void watchIndexedFiles(
        client,
        pid,
        sourceQuery,
        (entries, page) => {
          if (controller.signal.aborted) return;
          inventoryHasResponse = true;
          batch(() => {
            setIndexPage({
              query: sourceQuery,
              files: [...entries],
              location: page.location,
              outside: page.outside,
            });
            setIndexCoverage(page.coverage);
            setIndexError(page.state === "failed" ? "The file index could not be loaded. Try again." : "");
            setIndexing(false);
          });
        },
        { sessionId: sid, signal: controller.signal },
      )
        .catch(() => {
          if (!controller.signal.aborted) {
            setIndexError("File results could not refresh. Type to search again.");
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) setIndexing(false);
        });
    }, SEARCH_DEBOUNCE_MS);
    onCleanup(() => {
      controller.abort();
      clearTimeout(timer);
      setIndexing(false);
    });
  });

  createEffect(() => {
    if (crossbarArm(options.query()).kind !== "file-symbols") {
      setSymbolRows([]);
      return;
    }
    const pid = projectId();
    if (!pid) {
      setSymbolRows([]);
      return;
    }
    const files = projectFilesState(pid);
    const key = files.activeKey;
    const buf = key ? files.byKey[key] : null;
    if (!buf || buf.jobId) {
      setSymbolRows([]);
      return;
    }
    const client = getLycaonClient();
    if (!client) {
      setSymbolRows([]);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      void fetchCachedSourceSymbols({
        client,
        projectId: pid,
        sessionId: sessionId(),
        rootId: buf.rootId,
        path: buf.path,
        sha256: buf.baseSha256,
      }).then((symbols) => {
        if (cancelled) return;
        setSymbolRows(
          symbols.map((s) => ({
            name: s.name,
            kind: s.kind,
            line: s.line,
            rootId: buf.rootId,
            path: buf.path,
            bufferKey: buf.key,
          })),
        );
      });
    }, SEARCH_DEBOUNCE_MS);
    onCleanup(() => {
      cancelled = true;
      clearTimeout(timer);
    });
  });

  // `:42` jumps within the file the editor shows.
  const lineTarget = createMemo((): CrossbarLineTarget | null => {
    const pid = projectId();
    if (!pid) return null;
    const files = projectFilesState(pid);
    const buf = files.activeKey ? files.byKey[files.activeKey] : null;
    if (!buf?.path) return null;
    return { rootId: buf.rootId, path: buf.path, ...(buf.jobId ? { jobId: buf.jobId } : {}) };
  });

  const canRevealOutside = () => isTauriRuntime() && hostSharesDevice();

  // A commit, branch, or range opens on the Diffs page; only host-resolved specs get a row.
  createEffect(() => {
    const q = options.query().trim();
    const pid = projectId();
    const client = getLycaonClient();
    if (
      !options.open() ||
      activeSource() ||
      !pid ||
      !client ||
      options.activeMode() !== "everything" ||
      crossbarArm(q).kind !== "search" ||
      !looksLikeRevisionSpec(q)
    ) {
      return;
    }
    const sid = sessionId();
    const controller = new AbortController();
    const timer = setTimeout(
      () =>
        void (async () => {
          let comparisons: SourceRevisionComparison[] = [];
          try {
            comparisons = await resolveRevisionComparisons(client, pid, q, {
              sessionId: sid,
              signal: controller.signal,
            });
          } catch {
            // Not a revision here; other results still answer the query.
          }
          if (!controller.signal.aborted) setRevisions({ query: q, comparisons });
        })(),
      SEARCH_DEBOUNCE_MS,
    );
    onCleanup(() => {
      clearTimeout(timer);
      controller.abort();
    });
  });

  const resetSearchState = () => {
    setHits([]);
    setSearchErrorNote("");
    setSearchCoverage(null);
    setSearching(false);
  };

  return {
    projectId,
    hits,
    searchBusy,
    indexPage,
    symbolRows,
    sourceResults,
    sourceError,
    searchSources,
    activeSource,
    activeSourceQuery,
    inventoryFiles,
    coverageNote,
    lineTarget,
    canRevealOutside,
    revisions,
    scheduleSearch,
    cancelWhenClosed,
    setSearching,
    resetSearchState,
  };
}
