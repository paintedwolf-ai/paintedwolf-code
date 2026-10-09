import { For, Show } from "solid-js";
import type { SearchResponse, SearchExportFormat } from "../../api/types.ts";
import type { SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import { SEARCH_RESULT_TYPES, type SearchResultType } from "../../search/search-result-types.ts";
import { resultTypeActive } from "../../search/search-query-model.ts";
import { searchRefinementOccurrences } from "../../search/search-filter-model.ts";
import { listRecentQueries } from "../../search/search-history.ts";
import { queryScanScoped } from "../../search/search-export.ts";
import { BrowseChip } from "../browse/BrowseChip.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import { DenTriggerGroup } from "../primitives/DenTriggerGroup.tsx";
import { SearchFilterChip } from "./SearchFilterChip.tsx";
type Props = {
 query: () => string; setQuery: (value: string) => void;
 visibleFilterSegments: () => ReturnType<typeof searchRefinementOccurrences>;
 response: () => SearchResponse | null; matchPrefs: () => SearchMatchPrefs;
 recent: () => ReturnType<typeof listRecentQueries>; exportDisabled: () => boolean;
 toggleSearchType: (type: SearchResultType) => void;
 removeExtraFilter: (filter: { field: string; value: string; negated: boolean }) => void;
 patchMatchPrefs: (patch: Partial<SearchMatchPrefs>) => void;
 runExport: (format: SearchExportFormat) => Promise<void>;
};
export function SearchToolbar(props: Props) { return (
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
                    const on = () => resultTypeActive(props.query(), resultType);
                    return (
                      <button
                        type="button"
                        class="den-browse-filter-toggle"
                        classList={{ "den-browse-filter-toggle--on": on() }}
                        data-testid={`search-type-${resultType.id}`}
                        aria-pressed={on()}
                        onClick={() => props.toggleSearchType(resultType)}
                      >
                        {resultType.label}
                      </button>
                    );
                  }}
                </For>
              </div>

              <Show when={props.visibleFilterSegments().length > 0}>
                <div
                  class="den-search-active-filters"
                  role="group"
                  aria-label="Active filters"
                >
                  <For each={props.visibleFilterSegments()}>
                    {(occurrence) => (
                      <BrowseChip
                        label={`${occurrence.negated ? "NOT " : ""}${occurrence.field}:${occurrence.value}`}
                        active
                        testId={`search-active-filter-${occurrence.field}-${occurrence.value}`}
                        onRemove={() => props.removeExtraFilter(occurrence)}
                      />
                    )}
                  </For>
                </div>
              </Show>
            </div>

            <div class="den-search-chrome-tools">
              <DenTriggerGroup ariaLabel="Search tools" testId="search-tools">
                <SearchFilterChip
                  facets={props.response()?.facets ?? []}
                  query={props.query()}
                  onQueryChange={props.setQuery}
                  includeDependencies={props.matchPrefs().includeDependencies}
                  onIncludeDependenciesChange={(value) => props.patchMatchPrefs({ includeDependencies: value })}
                  include={props.matchPrefs().include}
                  exclude={props.matchPrefs().exclude}
                  onIncludeChange={(value) => props.patchMatchPrefs({ include: value })}
                  onExcludeChange={(value) => props.patchMatchPrefs({ exclude: value })}
                />
                {/* The disabled Recent control preserves toolbar width. */}
                <BrowseOverflowMenu
                  testId="search-recent-menu"
                  label="Recent"
                  disabled={props.recent().length === 0}
                  items={props.recent()
                    .slice(0, 6)
                    .map((entry) => ({
                      label: entry.query,
                      onSelect: () => props.setQuery(entry.query),
                    }))}
                />
                <BrowseOverflowMenu
                  testId="search-overflow"
                  label="Export"
                  items={[
                    {
                      label: "JSONL",
                      testId: "search-export-jsonl",
                      disabled: props.exportDisabled(),
                      onSelect: () => void props.runExport("jsonl"),
                    },
                    {
                      label: "CSV",
                      testId: "search-export-csv",
                      disabled: props.exportDisabled(),
                      onSelect: () => void props.runExport("csv"),
                    },
                    {
                      label: "SARIF",
                      testId: "search-export-sarif",
                      disabled: props.exportDisabled() || !queryScanScoped(props.query()),
                      onSelect: () => void props.runExport("sarif"),
                    },
                  ]}
                />
              </DenTriggerGroup>
            </div>

          </div>
); }
