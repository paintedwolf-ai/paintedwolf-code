import { createEffect, createSignal, createUniqueId, For, Show, untrack } from "solid-js";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import type { SearchFacet } from "../../api/types.ts";
import type { QueryLintError, QuerySegment } from "../../search/search-query-model.ts";
import {
  applyFieldSuggestion,
  applyValueSuggestion,
  composeQueryWithProjectScope,
  formatLintMessage,
  lintSearchQuery,
  phraseHintFor,
  insertTextAtFilterBoundary,
  isFilterBoundaryAtCursor,
  projectScopeIsCurrent,
  querySuggestionsAtCursor,
  rewriteProjectScope,
  segmentSearchQuery,
  stripProjectScopeToken,
  suggestionContextAtCursor,
  type QuerySuggestion,
} from "../../search/search-query-model.ts";
import { SearchIcon } from "./SearchIcons.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { createResidentFocus } from "../../ui/resident-activity.ts";

function SegmentVisual(props: { segment: QuerySegment }) {
  if (props.segment.type === "filter") {
    return (
      <span class="den-search-query__chip den-search-query__chip--filter">
        <span class="den-search-query__chip-key">{props.segment.field}</span>
        <span class="den-search-query__chip-colon">:</span>
        <span class="den-search-query__chip-value">{props.segment.value}</span>
      </span>
    );
  }
  return <span class="den-search-query__text">{props.segment.text}</span>;
}

export type SearchMatchToggleState = {
  caseSensitive: boolean;
  wholeWord: boolean;
  regex: boolean;
};

type Props = {
  query: string;
  originProjectId: string | null;
  originName: string | null;
  facets?: SearchFacet[];
  recentQueries?: string[];
  apiLint?: QueryLintError | null;
  onQueryChange: (query: string) => void;
  match?: SearchMatchToggleState;
  onMatchChange?: (patch: Partial<SearchMatchToggleState>) => void;
  replaceMode?: boolean;
  onReplaceModeChange?: (on: boolean) => void;
  replacement?: string;
  onReplacementChange?: (value: string) => void;
};

export function SearchQueryBar(props: Props) {
  const [activeSuggestion, setActiveSuggestion] = createSignal(0);
  const [inputFocused, setInputFocused] = createSignal(false);
  const [suggestionsDismissed, setSuggestionsDismissed] = createSignal(false);
  let inputEl: HTMLInputElement | undefined;
  let chipsEl: HTMLDivElement | undefined;
  let fieldEl: HTMLDivElement | undefined;
  const [focusInput, setFocusInput] = createSignal<HTMLInputElement>();
  const suggestionListId = `${createUniqueId()}-search-suggestions`;
  const suggestionId = (index: number) => `${suggestionListId}-${index}`;

  const syncHighlightScroll = () => {
    if (inputEl && chipsEl) chipsEl.scrollLeft = inputEl.scrollLeft;
  };

  // Measure with the input font to account for proportional glyph widths.
  let measureCtx: CanvasRenderingContext2D | null | undefined;
  const caretOffsetPx = (el: HTMLInputElement, cursor: number): number => {
    if (measureCtx === undefined) {
      measureCtx = document.createElement("canvas").getContext("2d");
    }
    if (!measureCtx) {
      return (el.scrollWidth * cursor) / Math.max(1, el.value.length);
    }
    const style = getComputedStyle(el);
    measureCtx.font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
    return (
      measureCtx.measureText(el.value.slice(0, cursor)).width +
      (Number.parseFloat(style.paddingLeft) || 0)
    );
  };

  const revealCaret = () => {
    if (!inputEl || inputEl.value.length === 0) return;
    const cursor = inputEl.selectionEnd ?? inputEl.value.length;
    const caretX = caretOffsetPx(inputEl, cursor);
    const inset = 8;
    if (caretX < inputEl.scrollLeft + inset) {
      inputEl.scrollLeft = Math.max(0, caretX - inset);
    } else if (caretX > inputEl.scrollLeft + inputEl.clientWidth - inset) {
      inputEl.scrollLeft = caretX - inputEl.clientWidth + inset;
    }
    syncHighlightScroll();
  };

  const [scoped, setScoped] = createSignal(projectScopeIsCurrent(props.query));
  // Scope state is independent of transient filter text.
  let pendingPublishedQuery: string | undefined;

  // Local text preserves the caret while external controls update the query.
  const [editableQuery, setEditableQuery] = createSignal(
    stripProjectScopeToken(props.query),
  );
  const collapseWhitespace = (text: string) => text.replace(/\s+/g, " ").trim();
  createEffect(() => {
    const published = props.query;
    if (published === pendingPublishedQuery) {
      pendingPublishedQuery = undefined;
      return;
    }
    setScoped(projectScopeIsCurrent(published));
    const incoming = stripProjectScopeToken(published);
    // Untracked local text distinguishes external updates.
    if (collapseWhitespace(incoming) !== collapseWhitespace(untrack(editableQuery))) {
      setEditableQuery(incoming);
    }
  });

  const hasQuery = () => props.query.trim().length > 0;
  const lint = () => (hasQuery() ? props.apiLint ?? lintSearchQuery(props.query) : null);
  const phraseHint = () => (hasQuery() && !lint() ? phraseHintFor(props.query) : null);

  const suggestions = (): QuerySuggestion[] => {
    const el = inputEl;
    if (!el) return [];
    const cursor = el.selectionStart ?? editableQuery().length;
    return querySuggestionsAtCursor(
      editableQuery(),
      cursor,
      props.facets ?? [],
      props.recentQueries ?? [],
    );
  };

  const showSuggestions = () =>
    inputFocused() && !suggestionsDismissed() && suggestions().length > 0;

  const dismissSuggestions = () => {
    setSuggestionsDismissed(true);
    setActiveSuggestion(0);
  };

  const publishQuery = (query: string) => {
    pendingPublishedQuery = query;
    props.onQueryChange(query);
  };

  const publishEditable = (raw: string, cursor?: number) => {
    setEditableQuery(raw);
    publishQuery(composeQueryWithProjectScope(raw, scoped()));
    if (cursor != null) {
      queueMicrotask(() => {
        if (inputEl) {
          inputEl.selectionStart = cursor;
          inputEl.selectionEnd = cursor;
        }
      });
    }
  };

  const prepareFilterBoundaryForComposition = () => {
    const el = inputEl;
    if (!el) return;
    const start = el.selectionStart ?? editableQuery().length;
    const end = el.selectionEnd ?? start;
    if (!isFilterBoundaryAtCursor(editableQuery(), start, end)) return;

    const followingWhitespace = /^\s+/u.exec(editableQuery().slice(end))?.[0];
    if (followingWhitespace) {
      const cursor = end + followingWhitespace.length;
      el.selectionStart = cursor;
      el.selectionEnd = cursor;
      return;
    }
    const separated =
      `${editableQuery().slice(0, start)} ${editableQuery().slice(end)}`;
    setEditableQuery(separated);
    el.value = separated;
    el.selectionStart = start + 1;
    el.selectionEnd = start + 1;
  };

  const setScope = (next: "current" | "everything") => {
    const isCurrent = next === "current";
    setScoped(isCurrent);
    publishQuery(rewriteProjectScope(props.query, next));
    queueMicrotask(() => inputEl?.focus());
  };

  const acceptSuggestion = (item: QuerySuggestion) => {
    const el = inputEl;
    if (!el) return;
    const cursor = el.selectionStart ?? editableQuery().length;
    const editable = editableQuery();

    if (item.kind === "recent") {
      const recentEditable = stripProjectScopeToken(item.query);
      setEditableQuery(recentEditable);
      publishQuery(composeQueryWithProjectScope(recentEditable, scoped()));
      setActiveSuggestion(0);
      queueMicrotask(() => inputEl?.focus());
      return;
    }

    if (item.kind === "value") {
      const { query, cursor: nextCursor } = applyValueSuggestion(
        editable,
        item.value,
        cursor,
      );
      publishEditable(query, nextCursor);
      setActiveSuggestion(0);
      return;
    }

    const ctx = suggestionContextAtCursor(editable, cursor, props.facets ?? []);
    if (!ctx || !("start" in ctx)) return;
    const { query, cursor: nextCursor } = applyFieldSuggestion(
      editable,
      ctx,
      item.field,
      cursor,
    );
    publishEditable(query, nextCursor);
    setActiveSuggestion(0);
  };

  const suggestionLabel = (item: QuerySuggestion): string => {
    if (item.kind === "field") return item.field;
    if (item.kind === "value") return item.value;
    return item.query;
  };

  const suggestionMeta = (item: QuerySuggestion): string | undefined => {
    if (item.kind === "field") return item.hint;
    if (item.kind === "value") return `${item.count} hits`;
    return "Recent";
  };

  createResidentFocus(focusInput, revealCaret);

  const activeSuggestionId = () => {
    if (!showSuggestions()) return undefined;
    return suggestions()[activeSuggestion()] ? suggestionId(activeSuggestion()) : undefined;
  };

  return (
    <div
      class="den-search-query"
      data-testid="search-query-bar"
    >
      <div class="den-search-query__row">
        <Show when={props.originProjectId}>
          <div
            class="den-search-scope"
            data-testid="search-scope-control"
            data-tip={
              props.originName
                ? `This project is ${props.originName}. Everything still ranks it first.`
                : undefined
            }
          >
            <BrowseSegmented
              ariaLabel="Search scope"
              value={scoped() ? "current" : "everything"}
              onChange={(id) =>
                setScope(id === "current" ? "current" : "everything")
              }
              options={[
                {
                  id: "current",
                  label: "This project",
                  testId: "search-scope-current",
                },
                {
                  id: "everything",
                  label: "Everything",
                  testId: "search-scope-everything",
                },
              ]}
            />
          </div>
        </Show>
        <div
          class="den-search-query__field"
          classList={{ "den-search-query__field--error": !!lint() }}
          data-first-time-tip-anchor="project-search"
          ref={(element) => {
            fieldEl = element;
          }}
        >
          <span class="den-search-query__icon" aria-hidden="true">
            <SearchIcon />
          </span>
          <div class="den-search-query__editor">
            <div
              ref={chipsEl}
              class="den-search-query__chips"
              aria-hidden="true"
            >
              <For each={segmentSearchQuery(editableQuery())}>
                {(segment) => <SegmentVisual segment={segment} />}
              </For>
            </div>
            <input
              ref={(element) => {
                inputEl = element;
                setFocusInput(element);
              }}
              class="den-search-query__input"
              data-testid="search-query-input"
              type="text"
              role="combobox"
              value={editableQuery()}
              spellcheck={false}
              placeholder={
                scoped()
                  ? "Search this project…"
                  : "Search code, findings, messages and the web…"
              }
              aria-label="Search query"
              aria-autocomplete="list"
              aria-expanded={showSuggestions()}
              aria-controls={showSuggestions() ? suggestionListId : undefined}
              aria-activedescendant={activeSuggestionId()}
              onFocus={() => {
                setInputFocused(true);
                setSuggestionsDismissed(false);
              }}
              onBlur={() => {
                setInputFocused(false);
              }}
              onInput={(e) => {
                const el = e.currentTarget;
                setSuggestionsDismissed(false);
                publishEditable(el.value);
                setActiveSuggestion(0);
                queueMicrotask(revealCaret);
              }}
              onBeforeInput={(e) => {
                const input = e as InputEvent;
                if (
                  input.inputType !== "insertText" ||
                  input.isComposing ||
                  !input.data
                ) {
                  return;
                }
                const el = e.currentTarget;
                const start = el.selectionStart ?? editableQuery().length;
                const end = el.selectionEnd ?? start;
                const insertion = insertTextAtFilterBoundary(
                  editableQuery(),
                  start,
                  end,
                  input.data,
                );
                if (!insertion) return;
                e.preventDefault();
                setSuggestionsDismissed(false);
                publishEditable(insertion.query, insertion.cursor);
                setActiveSuggestion(0);
                queueMicrotask(revealCaret);
              }}
              onCompositionStart={prepareFilterBoundaryForComposition}
              onScroll={syncHighlightScroll}
              onClick={() => queueMicrotask(revealCaret)}
              onKeyUp={() => queueMicrotask(revealCaret)}
              onKeyDown={(e) => {
                if (e.isComposing) return;
                if (e.key === "Escape") {
                  if (showSuggestions()) {
                    e.stopPropagation();
                    dismissSuggestions();
                    return;
                  }
                }
                if (!showSuggestions()) return;
                const items = suggestions();
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setActiveSuggestion((i) => (i + 1) % items.length);
                  return;
                }
                if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setActiveSuggestion((i) => (i - 1 + items.length) % items.length);
                  return;
                }
                if (e.key === "Enter" || e.key === "Tab") {
                  const pick = items[activeSuggestion()];
                  if (pick) {
                    e.preventDefault();
                    acceptSuggestion(pick);
                  }
                }
              }}
            />
          </div>
          <Show when={props.match && props.onMatchChange}>
            <div
              class="den-search-query__toggles"
              role="group"
              aria-label="Match options"
              data-testid="search-match-toggles"
            >
              <button
                type="button"
                class="den-search-query__toggle"
                data-tip="Match case"
                aria-label="Match case"
                aria-pressed={!!props.match?.caseSensitive}
                data-testid="search-toggle-case"
                onClick={() =>
                  props.onMatchChange?.({
                    caseSensitive: !props.match?.caseSensitive,
                  })
                }
              >
                Aa
              </button>
              <button
                type="button"
                class="den-search-query__toggle"
                data-tip="Whole word"
                aria-label="Whole word"
                aria-pressed={!!props.match?.wholeWord}
                data-testid="search-toggle-word"
                onClick={() =>
                  props.onMatchChange?.({
                    wholeWord: !props.match?.wholeWord,
                  })
                }
              >
                \b
              </button>
              <button
                type="button"
                class="den-search-query__toggle"
                data-tip="Regular expression"
                aria-label="Regular expression"
                aria-pressed={!!props.match?.regex}
                data-testid="search-toggle-regex"
                onClick={() =>
                  props.onMatchChange?.({ regex: !props.match?.regex })
                }
              >
                .*
              </button>
            </div>
          </Show>
          <Show when={props.onReplaceModeChange}>
            <button
              type="button"
              class="den-search-query__toggle den-search-query__toggle--replace"
              aria-label="Replace"
              aria-pressed={!!props.replaceMode}
              data-testid="search-toggle-replace"
              onClick={() => props.onReplaceModeChange?.(!props.replaceMode)}
            >
              Replace
            </button>
          </Show>
          <Show when={showSuggestions()}>
            <AnchoredSurface
              class="den-menu-surface den-search-query__suggestions-wrap"
              anchor={() => fieldEl}
              preferredSide="bottom"
              align="start"
              width="anchor"
              overflow="hidden"
              onDismiss={dismissSuggestions}
            >
              <div class="den-search-query__suggestions-head">
                <span>Suggestions</span>
                <button
                  type="button"
                  class="den-search-query__suggestions-close"
                  aria-label="Close suggestions"
                  data-testid="search-suggestions-close"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => dismissSuggestions()}
                >
                  ×
                </button>
              </div>
              <Scrollport
                class="den-search-query__suggestions-scroll"
                data-testid="search-field-suggestions-scroll"
                contentAs="ul"
                contentClass="den-search-query__suggestions"
                content={{
                  id: suggestionListId,
                  role: "listbox",
                  "data-testid": "search-field-suggestions",
                }}
              >
                  <For each={suggestions()}>
                    {(item, index) => (
                      <li role="presentation">
                        <button
                          id={suggestionId(index())}
                          type="button"
                          role="option"
                          class="den-search-query__suggestion"
                          classList={{
                            "den-search-query__suggestion--active": index() === activeSuggestion(),
                          }}
                          aria-selected={index() === activeSuggestion()}
                          data-testid={`search-suggestion-${item.kind}-${suggestionLabel(item)}`}
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={() => acceptSuggestion(item)}
                        >
                          <span
                            class="den-search-query__suggestion-field"
                            classList={{
                              "den-search-query__suggestion-field--value": item.kind === "value",
                            }}
                          >
                            <Show when={item.kind === "field" ? item : undefined}>
                              {(fieldItem) => (
                                <>
                                  {fieldItem().field}
                                  <span class="den-search-query__suggestion-colon">:</span>
                                </>
                              )}
                            </Show>
                            <Show when={item.kind === "value" ? item : undefined}>
                              {(valueItem) => (
                                <>
                                  <span class="den-search-query__suggestion-prefix">
                                    {valueItem().field}:
                                  </span>
                                  {valueItem().value}
                                </>
                              )}
                            </Show>
                            {item.kind === "recent" && item.query}
                          </span>
                          <Show when={suggestionMeta(item)}>
                            {(meta) => (
                              <span class="den-search-query__suggestion-hint">{meta()}</span>
                            )}
                          </Show>
                        </button>
                      </li>
                    )}
                  </For>
              </Scrollport>
            </AnchoredSurface>
          </Show>
        </div>
      </div>

      <Show when={props.replaceMode && props.onReplacementChange}>
        <div class="den-search-query__replace-row" data-testid="search-replace-row">
          <input
            class="den-search-query__replace-input"
            data-testid="search-replace-input"
            type="text"
            value={props.replacement ?? ""}
            spellcheck={false}
            placeholder={
              props.match?.regex
                ? "Replacement — use $1 for capture groups"
                : "Replacement"
            }
            aria-label="Replacement"
            onInput={(e) => props.onReplacementChange?.(e.currentTarget.value)}
          />
        </div>
      </Show>

      <Show when={lint()}>
        {(error) => (
          <p class="den-search-query__lint" data-testid="search-query-lint">
            {formatLintMessage(error())}
          </p>
        )}
      </Show>
      <Show when={phraseHint()}>
        {(hint) => (
          <p class="den-search-query__hint" data-testid="search-query-hint">
            {hint()}
          </p>
        )}
      </Show>
    </div>
  );
}
