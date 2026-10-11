import type { SearchHit, SearchResponse } from "../../api/types.ts";
import type { SearchNavTarget } from "../../search/search-hit-nav.ts";
import { searchHitToNavTarget } from "../../search/search-hit-nav.ts";
import { appendPivotFilter, queryFilterActive, searchQueryFreeText, groupHitsByProject } from "../../search/search-query-model.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { searchHitKey } from "./SearchResultGroup.tsx";
type Options = {
 groups: () => ReturnType<typeof groupHitsByProject>;
 selectedHitKey: () => string | null; setSelectedHitKey: (value: string | null) => void;
 selectedHit: () => SearchHit | null; setSelectedHit: (value: SearchHit | null) => void;
 response: () => SearchResponse | null; loadNextPage: () => Promise<boolean>;
 resultsRoot: () => HTMLElement | null; resultsViewport: () => HTMLDivElement | undefined;
 query: () => string; setQuery: (value: string) => void;
 onNavigate: (target: SearchNavTarget) => void;
};
export function createSearchResultNavigation(options: Options) {
  // Keyboard navigation follows rendered group order.
  const flatHits = () =>
    options.groups().flatMap((group) =>
      group.hits.map((hit) => ({
        hit,
        key: searchHitKey(hit),
      })),
    );

  const moveSelection = (delta: 1 | -1) => {
    const list = flatHits();
    if (list.length === 0) return;
    const current = options.selectedHitKey();
    const idx = list.findIndex((entry) => entry.key === current);
    const next =
      idx < 0
        ? delta === 1
          ? 0
          : list.length - 1
        : Math.min(list.length - 1, Math.max(0, idx + delta));
    if (delta === 1 && idx === list.length - 1 && options.response()?.next_cursor) {
      void options.loadNextPage().then((loaded) => {
        if (!loaded) return;
        const entry = flatHits()[0];
        if (!entry) return;
        options.setSelectedHit(entry.hit);
        options.setSelectedHitKey(entry.key);
      });
      return;
    }
    const entry = list[next];
    if (!entry) return;
    options.setSelectedHit(entry.hit);
    options.setSelectedHitKey(entry.key);
    queueMicrotask(() => {
      // Attribute selectors require quoted key escaping.
      const safe = entry.key.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
      const target = options.resultsRoot()?.querySelector(
        `[data-hit-key="${safe}"]`,
      );
      if (!target) return;
      const viewport = options.resultsViewport();
      if (viewport) scrollportMotionForViewport(viewport)?.revealElement(target);
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
      const hit = options.selectedHit();
      if (hit) {
        e.preventDefault();
        openSearchHit(hit, navigateHit);
      }
      return;
    }
    if (e.key === "Escape" && options.selectedHitKey()) {
      // First Escape clears selection; the next reaches the shell.
      e.preventDefault();
      e.stopPropagation();
      options.setSelectedHit(null);
      options.setSelectedHitKey(null);
    }
  };

  const pivotQuery = (field: string, value: string) => {
    options.setQuery(appendPivotFilter(options.query(), field, value));
  };

  const navigateHit = (hit: SearchHit) => {
    options.onNavigate(searchHitToNavTarget(hit, searchQueryFreeText(options.query())));
  };

  const inspectorPivots = () => {
    const hit = options.selectedHit();
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
      (pivot) => !queryFilterActive(options.query(), pivot.field, pivot.value),
    );
  };

  const selectHit = (hit: SearchHit, key: string) => {
    if (options.selectedHitKey() === key) {
      options.setSelectedHit(null);
      options.setSelectedHitKey(null);
      return;
    }
    options.setSelectedHit(hit);
    options.setSelectedHitKey(key);
  };

  return { onStageKeyDown, pivotQuery, navigateHit, inspectorPivots, selectHit };
}
