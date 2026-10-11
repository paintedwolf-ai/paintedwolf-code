import type { SearchResultsController } from "./search-results-controller.ts";
import { SEARCH_COLUMNS, SEARCH_LIST_SURFACE } from "../../search/search-columns.ts";
import { resolveColumnWidths, sortRows } from "../../list/list-columns.ts";
import { createFittedColumns } from "../../list/create-fitted-columns.ts";
import type { SortState } from "../../list/list-sort.ts";
import { resolveListPaneSort } from "../../list/list-pane-model.ts";
import { listPanePrefs } from "../../shell/layout-store.ts";
import { groupHitsByProject } from "../../search/search-query-model.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
type Options = Pick<SearchResultsController, "response" | "currentPage" | "pageIndex" | "searchPages" | "searchFrame" | "selectedHit" | "loading" | "searchFailed"> & {
 query: () => string;
 originProjectId: () => string | null;
};
export function createSearchResultsPresentation(options: Options) {
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
      options.response()?.hits ?? [],
      options.originProjectId(),
    );
    const sort = paneSort();
    if (!sort) return grouped;
    return grouped.map((group) => ({
      ...group,
      hits: sortRows(group.hits, SEARCH_COLUMNS, sort),
    }));
  };

  const hitCount = () => options.response()?.hits?.length ?? 0;
  const pageStart = () => options.currentPage()?.start ?? 0;
  const pageEnd = () => pageStart() + hitCount();
  const hasPreviousPage = () => options.pageIndex() > 0;
  const hasNextPage = () =>
    !!options.searchPages()[options.pageIndex() + 1] || !!options.response()?.next_cursor;

  const highlightTerms = () => options.response()?.interpreted?.fts_terms ?? [];
  const codeCaseSensitive = () => options.searchFrame().scope?.options.caseSensitive;

  const emptySearchTitle = () => options.response()?.exhaustive === false ? "No results found so far" : "No results";

  const hitSummary = () => {
    const result = options.response();
    if (!result) return "No results";
    const suffix = result.count_relation === "lower_bound" ? "+" : "";
    if (result.total_hits === 1) return suffix ? "1+ results" : "1 result";
    const range = `${(pageStart() + 1).toLocaleString()}–${pageEnd().toLocaleString()}`;
    return `${range} of ${result.total_hits.toLocaleString()}${suffix} results`;
  };

  const selectedHitAnnouncement = () => {
    const hit = options.selectedHit();
    if (!hit) return "";
    const display = searchHitDisplay(hit);
    const location = display.context ? `, ${display.context}` : "";
    return `Selected ${display.kindLabel}: ${display.title}${location}. Press Enter to open.`;
  };

  const hitsMeta = () => {
    if (!options.query().trim()) return null;
    return (
      <span class="den-search__hits" data-testid="search-hits-count">
        {options.loading()
          ? "Searching…"
          : options.searchFailed()
            ? "Search failed"
            : hitCount() === 0
              ? emptySearchTitle()
              : hitSummary()}
      </span>
    );
  };

  return { paneSort, columnWidths, fitted, groups, hitCount, pageStart, pageEnd, hasPreviousPage, hasNextPage, highlightTerms, codeCaseSensitive, emptySearchTitle, hitSummary, selectedHitAnnouncement, hitsMeta };
}
