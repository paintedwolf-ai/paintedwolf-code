import { createSearchExport } from "./search-export-controller.ts";
import { createSearchQueryControls } from "./search-query-controls.ts";
import { createSearchResultsPresentation } from "./search-results-presentation.tsx";
import { SearchToolbar } from "./SearchToolbar.tsx";
import { createSearchResultNavigation } from "./search-result-navigation.ts";
import { SearchResultsBody } from "./SearchResultsBody.tsx";
import { createSearchResultsController } from "./search-results-controller.ts";
import { createSearchReplacement } from "./search-replacement.ts";
import { searchCoverageNote } from "../../search/search-status.ts";
import { createMemo, createSignal, onMount, Show } from "solid-js";
import type { SearchReplacePreviewRequest } from "../../api/types.ts";
import { getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { APP_SCOPE, projectScope } from "../../notices/notice-scope.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { type QueryLintError } from "../../search/search-query-model.ts";
import type { SearchNavTarget } from "../../search/search-hit-nav.ts";
import { listRecentQueries } from "../../search/search-history.ts";
import { selectionToApplyFiles, type ReplaceSelection } from "../../search/search-replace-selection.ts";
import { refreshBuffersAfterReplace } from "../../search/search-replace-buffers.ts";
import { revertReplaceBatch } from "../../search/replace-revert.ts";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { SearchHitDetail } from "./SearchHitDetail.tsx";
import { SearchQueryBar } from "./SearchQueryBar.tsx";
import { SearchReplacePreview } from "./SearchReplacePreview.tsx";
import { SEARCH_LIST_SURFACE } from "../../search/search-columns.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";

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

export function GlobalSearchView(props: Props) {
  onMount(() => requestFirstTimeTip("project-search"));
  const residentLive = useResidentLive();
  const { query, setQuery, matchPrefs, patchMatchPrefs, matchOptions, replaceMode, setReplaceMode,
    replacement, setReplacement, renameFrom, setRenameFrom, visibleFilterSegments,
    toggleSearchType, removeExtraFilter, clearFilters,
  } = createSearchQueryControls({ seed: () => props.seed, seedSerial: () => props.seedSerial,
    originProjectId: () => props.originProjectId, replaceModeRequested: () => props.replaceModeRequested });


  // Attribute failures to the search origin.
  const searchReporter = () =>
    noticeReporterFor(
      props.originProjectId ? projectScope(props.originProjectId) : APP_SCOPE,
    );

  const [previewLint, setPreviewLint] = createSignal<QueryLintError | null>(null);
  const queryBarLint = () => apiLint() ?? previewLint();
  const [recent, setRecent] = createSignal(listRecentQueries());
  const [resultsRoot, setResultsRoot] = createSignal<HTMLElement | null>(null);


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

  const replaceRequest = createMemo<SearchReplacePreviewRequest>(() => {
    const flags = matchOptions();
    return {
      query: query().trim(),
      replacement: replacement(),
      origin_project_id: props.originProjectId ?? undefined,
      regex: !!flags.regex,
      include_dependencies: flags.includeDependencies,
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

  const { searchFrame, searchPages, pageIndex, currentPage, response, indexing,
    loading, refining, loadingPage, pageLoadFailed, searchFailed, apiLint, setApiLint,
    selectedHit, setSelectedHit, selectedHitKey, setSelectedHitKey, runSearch,
    showPage, loadNextPage, searchRequestChanged, coldSearchPending, retainingResults,
    showRetainedWait, setResultsViewport, resultsViewport,
  } = createSearchResultsController({
    originProjectId: () => props.originProjectId, residentLive, query, matchOptions, stopPreview: () => stopPreview(),
    setPreviewLint, replaceMode, previewCurrent: () => previewCurrent(), runReplacePreview: () => runReplacePreview(),
    clearReplace: () => { setReplacePreview(null); setReplaceSelection(null); },
    updateRecent: () => setRecent(listRecentQueries()),
    reportError: (error) => searchReporter().reportError(error),
  });

  const { replacePreview, setReplacePreview, reviewedRequest, replaceSelection, setReplaceSelection,
    replaceApplying, replaceSummary, setReplaceSummary, previewPreparing, previewNotice,
    stopPreview, cancelPreview, runReplacePreview, applyReplaceFiles, observePreviewChanges,
  } = createSearchReplacement({
    originProjectId: () => props.originProjectId, query, replaceMode, residentLive, renameFrom,
    replaceRequest: () => replaceRequest(), previewRequestKey: () => previewRequestKey(),
    previewCurrent: () => previewCurrent(), setApiLint, setPreviewLint,
    reportError: (error) => searchReporter().reportError(error), runSearch: (value) => { void runSearch(value); },
  });

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

  const { paneSort, columnWidths, fitted, groups, hitCount, hasPreviousPage, hasNextPage,
    highlightTerms, codeCaseSensitive, emptySearchTitle, hitSummary, selectedHitAnnouncement, hitsMeta,
  } = createSearchResultsPresentation({ response, currentPage, pageIndex, searchPages, searchFrame,
    selectedHit, loading, searchFailed, query, originProjectId: () => props.originProjectId });

  const { onStageKeyDown, pivotQuery, navigateHit, inspectorPivots, selectHit } = createSearchResultNavigation({
    groups, selectedHitKey, setSelectedHitKey, selectedHit, setSelectedHit, response,
    loadNextPage, resultsRoot, resultsViewport, query, setQuery, onNavigate: props.onNavigate,
  });

  const issueNote = () => searchCoverageNote(response()) || undefined;

  const { exportTruncated, runExport, exportDisabled } = createSearchExport({ query,
    originProjectId: () => props.originProjectId, matchOptions, apiLint, setApiLint,
    reportError: (error) => searchReporter().reportError(error),
  });

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
          <SearchToolbar query={query} setQuery={setQuery} visibleFilterSegments={visibleFilterSegments}
            response={response} matchPrefs={matchPrefs} recent={recent} exportDisabled={exportDisabled}
            toggleSearchType={toggleSearchType} removeExtraFilter={removeExtraFilter}
            patchMatchPrefs={patchMatchPrefs} runExport={runExport} />
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
            viewportRef={setResultsViewport}
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

            <SearchResultsBody
              replaceMode={replaceMode}
              loading={loading}
              response={response}
              searchFailed={searchFailed}
              recent={recent}
              query={query}
              coldSearchPending={coldSearchPending}
              searchRequestChanged={searchRequestChanged}
              hitCount={hitCount}
              refining={refining}
              indexing={indexing}
              apiLint={apiLint}
              emptySearchTitle={emptySearchTitle}
              runSearch={runSearch}
              clearFilters={clearFilters}
              columnWidths={columnWidths}
              paneSort={paneSort}
              groups={groups}
              selectedHitKey={selectedHitKey}
              highlightTerms={highlightTerms}
              codeCaseSensitive={codeCaseSensitive}
              issueNote={issueNote}
              setQuery={setQuery}
              fitted={fitted}
              setResultsRoot={setResultsRoot}
              rootRefsForProject={rootRefsForProject}
              selectHit={selectHit}
              pivotQuery={pivotQuery}
              navigateHit={navigateHit}
              include={() => matchPrefs().include} exclude={() => matchPrefs().exclude}
            />
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
