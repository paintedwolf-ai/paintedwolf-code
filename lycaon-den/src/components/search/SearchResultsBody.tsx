import { For, Show } from "solid-js";
import type { SearchHit, SearchResponse } from "../../api/types.ts";
import type { QueryLintError } from "../../search/search-query-model.ts";
import { projectScopeIsCurrent, rewriteProjectScope, groupHitsByProject } from "../../search/search-query-model.ts";
import { hasSearchRefinements } from "../../search/search-filter-model.ts";
import { listRecentQueries } from "../../search/search-history.ts";
import { SEARCH_LIST_SURFACE } from "../../search/search-columns.ts";
import { resolveColumnWidths } from "../../list/list-columns.ts";
import type { FittedColumns } from "../../list/create-fitted-columns.ts";
import type { SortState } from "../../list/list-sort.ts";
import { commitListSort } from "../../shell/layout-store.ts";
import { ListColumnHeader } from "../list/ListColumnHeader.tsx";
import { SearchResultGroup } from "./SearchResultGroup.tsx";
const EMPTY_STATE_EXAMPLES = ["kind:code", "kind:evidence verified:false", "kind:message"] as const;

type Props = {
  runSearch: (query: string) => Promise<void>;
  replaceMode: () => boolean; loading: () => boolean; response: () => SearchResponse | null;
  searchFailed: () => boolean; recent: () => ReturnType<typeof listRecentQueries>;
  query: () => string; setQuery: (value: string) => void; coldSearchPending: () => boolean;
  searchRequestChanged: () => boolean; hitCount: () => number; refining: () => boolean;
  indexing: () => boolean; apiLint: () => QueryLintError | null; emptySearchTitle: () => string;
  include: () => string; exclude: () => string; clearFilters: () => void;
  columnWidths: () => ReturnType<typeof resolveColumnWidths>; paneSort: () => SortState;
  fitted: FittedColumns<SearchHit>; groups: () => ReturnType<typeof groupHitsByProject>;
  selectedHitKey: () => string | null; highlightTerms: () => string[];
  codeCaseSensitive: () => boolean | undefined; issueNote: () => string | undefined;
  setResultsRoot: (el: HTMLDivElement) => void;
  rootRefsForProject: (projectId: string) => { id: string; path: string; is_primary: boolean }[];
  selectHit: (hit: SearchHit, key: string) => void; pivotQuery: (field: string, value: string) => void;
  navigateHit: (hit: SearchHit) => void;
};

export function SearchResultsBody(props: Props) {
return <>
          <Show
            when={
              !props.replaceMode() &&
              !props.loading() &&
              props.response() == null &&
              !props.searchFailed()
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
                      onClick={() => props.setQuery(example)}
                    >
                      {example}
                    </button>
                  )}
                </For>
              </div>
              <Show when={props.recent().length > 0}>
                <div class="den-search__empty-recent">
                  <p class="den-search__empty-recent-label">Recent</p>
                  <For each={props.recent().slice(0, 5)}>
                    {(entry) => (
                      <button
                        type="button"
                        class="den-search__empty-recent-row"
                        data-testid="search-empty-recent"
                        onClick={() => props.setQuery(entry.query)}
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

          <Show when={!props.replaceMode() && props.coldSearchPending()}>
            <div class="den-search__empty" data-testid="search-loading-state">
              <p class="den-search__empty-title">Searching…</p>
              <p class="den-search__empty-hint">Checking the selected scope.</p>
            </div>
          </Show>

          <Show
            when={
              !props.replaceMode() &&
              !props.loading() &&
              props.searchFailed()
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
                  onClick={() => void props.runSearch(props.query())}
                >
                  Retry
                </button>
              </div>
            </div>
          </Show>

          <Show when={!props.replaceMode() && !props.loading() && !props.searchRequestChanged() && !props.searchFailed() && props.response() && props.hitCount() === 0 && (props.refining() || props.indexing())}>
            <div class="den-search__empty" data-testid="search-refining-state">
              <p class="den-search__empty-title">Searching…</p>
              <p class="den-search__empty-hint">
                {props.indexing() ? "Results will appear here as files become searchable." : "Checking the rest of the selected scope."}
              </p>
            </div>
          </Show>

          <Show
            when={
              !props.replaceMode() &&
              !props.searchFailed() &&
              !props.refining() &&
              !props.indexing() &&
              props.query().trim() &&
              props.response() &&
              props.hitCount() === 0 &&
              !props.apiLint()
            }
          >
            <div class="den-search__empty" data-testid="search-no-results">
              <p class="den-search__empty-title">{props.emptySearchTitle()}</p>
              <p class="den-search__empty-hint">
                {props.response()?.exhaustive === false
                  ? "Some of the selected scope could not be searched."
                  : projectScopeIsCurrent(props.query())
                  ? "Nothing matched in this project."
                  : "Nothing matched this query."}
              </p>
              <Show when={props.response()?.interpreted?.dependency_trees_excluded}>
                <p
                  class="den-search__empty-hint"
                  data-testid="search-no-results-dependency-note"
                >
                  Dependency and build trees were skipped. Enable dependency search
                  in Filters to include them.
                </p>
              </Show>
              <div class="den-search__empty-examples">
                <Show when={projectScopeIsCurrent(props.query())}>
                  <button
                    type="button"
                    class="den-search__empty-example"
                    data-testid="search-no-results-everything"
                    onClick={() =>
                      props.setQuery(rewriteProjectScope(props.query(), "everything"))
                    }
                  >
                    Search everything
                  </button>
                </Show>
                <Show
                  when={
                    hasSearchRefinements(
                      props.query(),
                      props.include(),
                      props.exclude(),
                    )
                  }
                >
                  <button
                    type="button"
                    class="den-search__empty-example"
                    data-testid="search-no-results-clear-filters"
                    onClick={props.clearFilters}
                  >
                    Clear filters
                  </button>
                </Show>
              </div>
            </div>
          </Show>

          <Show when={!props.replaceMode() && props.hitCount() > 0}>
            <ListColumnHeader
              surface={SEARCH_LIST_SURFACE}
              columns={props.fitted.columns()}
              widths={props.columnWidths()}
              measureRef={props.fitted.measure}
              sort={props.paneSort()}
              onSort={(state) => void commitListSort(SEARCH_LIST_SURFACE, state)}
              ariaLabel="Result columns"
              testId="search-results-header"
            />
          </Show>

            <Show when={!props.replaceMode()}>
              <div ref={props.setResultsRoot}>
                <SearchResultGroup
                  groups={props.groups()}
                  selectedHitKey={props.selectedHitKey()}
                  highlightTerms={props.highlightTerms()}
                  codeCaseSensitive={props.codeCaseSensitive()}
                  issueNote={props.issueNote()}
                  busy={props.refining()}
                  gridTemplate={props.fitted.template()}
                  showsLocation={props.fitted.shows("location")}
                  rootRefsForProject={props.rootRefsForProject}
                  onSelectHit={props.selectHit}
                  onPivot={props.pivotQuery}
                  onNavigate={props.navigateHit}
                />
              </div>
            </Show>

</>;
}
