import { createEffect, createSignal, on } from "solid-js";
import { composeQueryWithProjectScope, projectScopeIsCurrent, projectScopedCodeQuery, removeFilterToken, stripProjectScopeToken, toggleResultType } from "../../search/search-query-model.ts";
import type { SearchResultType } from "../../search/search-result-types.ts";
import { clearSearchRefinements, searchRefinementOccurrences } from "../../search/search-filter-model.ts";
import { loadSearchMatchPrefs, parseGlobField, saveSearchMatchPrefs, type SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import { consumeReplaceArm, type ReplaceArmRequest } from "../../search/replace-arm.ts";
type Options = {
 seed: () => string | undefined; seedSerial: () => number | undefined;
 originProjectId: () => string | null; replaceModeRequested: () => boolean | undefined;
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

export function createSearchQueryControls(options: Options) {
  const initialQuery = () => {
    const seed = options.seed()?.trim();
    if (seed) return seed;
    return options.originProjectId() ? projectScopedCodeQuery("") : "";
  };
  const [query, setQuery] = createSignal(initialQuery());
  createEffect(
    on(
      () => options.seedSerial(),
      () => {
        const seed = options.seed()?.trim();
        if (seed) setQuery(seed);
      },
      { defer: true },
    ),
  );
  // Apply one-shot replace flags.
  const arm = consumeReplaceArm();
  const [matchPrefs, setMatchPrefs] = createSignal<SearchMatchPrefs>(
    mergeArmedFlags(loadSearchMatchPrefs(), arm),
  );
  const [replaceMode, setReplaceMode] = createSignal(
    arm != null || !!options.replaceModeRequested(),
  );
  const [replacement, setReplacement] = createSignal(arm?.replacement ?? "");
  const [renameFrom, setRenameFrom] = createSignal<string | null>(
    arm?.renameFrom ?? null,
  );
  const matchOptions = () => {
    const prefs = matchPrefs();
    return {
      regex: prefs.regex,
      includeDependencies: prefs.includeDependencies,
      caseSensitive: prefs.caseSensitive,
      wholeWord: prefs.wholeWord,
      include: parseGlobField(prefs.include),
      exclude: parseGlobField(prefs.exclude),
    };
  };

  const persistMatchPrefs = (next: SearchMatchPrefs) => {
    setMatchPrefs(next);
    saveSearchMatchPrefs(next);
  };

  const patchMatchPrefs = (patch: Partial<SearchMatchPrefs>) => {
    persistMatchPrefs({ ...matchPrefs(), ...patch });
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
    patchMatchPrefs({ include: "", exclude: "", includeDependencies: false });
  };

  return { query, setQuery, matchPrefs, patchMatchPrefs, matchOptions, replaceMode, setReplaceMode, replacement, setReplacement, renameFrom, setRenameFrom, visibleFilterSegments, toggleSearchType, removeExtraFilter, clearFilters };
}
