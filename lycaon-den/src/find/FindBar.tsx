import { Show, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import { invokeCommand } from "../shortcuts/dispatcher.ts";
import { bindingForHandler } from "../shortcuts/display-binding-for.ts";
import {
  registerFocusRegion,
  releaseFocusRegion,
} from "../shortcuts/focus-region.ts";
import { chromeProps } from "../styling/ui-chrome.ts";
import { DenCheckboxControl } from "../components/primitives/DenCheckbox.tsx";
import { ThemeIcon } from "../components/primitives/ThemeIcon.tsx";
import {
  closeFind,
  findController,
  findHistoryDown,
  findHistoryUp,
  findMatchCount,
  findNext,
  findPrev,
  findSelectionAllowsScope,
  findSupportsReplacement,
  findSupportsScope,
  formatFindCountLabel,
  selectAllFindMatches,
  replaceAllFindMatches,
  replaceCurrentFindMatch,
  setFindCaseSensitive,
  setFindQuery,
  setFindReplacement,
  setFindScopeToSelection,
  toggleFindReplace,
} from "./find-controller.ts";

function withBinding(text: string, handler: string): string {
  const binding = bindingForHandler(handler).trim();
  return binding && binding !== "—" ? `${text} (${binding})` : text;
}

export function FindBar() {
  const [inputEl, setInputEl] = createSignal<HTMLInputElement | undefined>();
  const [replacementEl, setReplacementEl] = createSignal<
    HTMLInputElement | undefined
  >();
  const findFocusClaim = {};

  createEffect(() => {
    if (!findController.isOpen()) return;
    const el = inputEl();
    if (!el) return;
    // Avoid scrolling the active view.
    queueMicrotask(() => el.focus({ preventScroll: true }));
  });

  // Expanding the replace lane hands it the caret.
  createEffect(() => {
    if (!findController.replaceOpen()) return;
    const el = replacementEl();
    if (!el) return;
    queueMicrotask(() => el.focus({ preventScroll: true }));
  });

  const countLabel = createMemo(() => {
    const q = findController.query();
    if (!q) return "";
    // Track both provider result sources.
    void findController.cmCount();
    void findController.matches();
    void findController.activeIndex();
    void findController.collapsedCount();
    return formatFindCountLabel(findMatchCount());
  });

  const showScope = createMemo(() => findSupportsScope());
  const showReplace = createMemo(() => findSupportsReplacement());
  const replaceLaneOpen = createMemo(
    () => showReplace() && findController.replaceOpen(),
  );
  const replaceAllDisabled = createMemo(() => findMatchCount().capped);
  const scopeEnabled = createMemo(
    () => findController.scopeActive() || findSelectionAllowsScope(),
  );

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      const el = e.currentTarget as HTMLInputElement;
      const cur = el.value;
      const next =
        e.key === "ArrowUp" ? findHistoryUp(cur) : findHistoryDown(cur);
      if (next == null) return;
      e.preventDefault();
      e.stopPropagation();
      setFindQuery(next);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      if (e.shiftKey) findPrev();
      else findNext();
    }
  };

  return (
    <Show when={findController.isOpen()}>
      <div
        class="den-find-bar den-editor-command-bar den-editor-command-bar--find"
        role="search"
        aria-label="Find in view"
        data-testid="find-bar"
        {...chromeProps()}
        ref={(el) => {
          registerFocusRegion("find", el, findFocusClaim);
          onCleanup(() => releaseFocusRegion("find", findFocusClaim));
        }}
      >
        <div class="den-find-bar__lane den-find-bar__lane--query">
          <Show when={showReplace()}>
            <button
              type="button"
              class="den-editor-command-bar__button den-find-bar__replace-toggle"
              aria-label={
                findController.replaceOpen() ? "Hide replace" : "Show replace"
              }
              aria-expanded={findController.replaceOpen()}
              aria-controls={
                findController.replaceOpen() ? "den-find-replace-lane" : undefined
              }
              data-tip={withBinding("Replace", "find.replace")}
              data-testid="find-bar-replace-toggle"
              onClick={() => toggleFindReplace()}
            >
              <ThemeIcon slot="chevron-down" size={14} />
            </button>
          </Show>
          <label class="den-editor-command-bar__label" for="den-find-input">
            Find
          </label>
          <input
            id="den-find-input"
            ref={setInputEl}
            class="den-editor-command-bar__input den-input"
            type="search"
            autocomplete="off"
            spellcheck={false}
            placeholder="Search this view"
            value={findController.query()}
            data-testid="find-bar-input"
            onInput={(e) => setFindQuery(e.currentTarget.value)}
            onKeyDown={onKeyDown}
          />
          <span
            class="den-editor-command-bar__count text-sm text-den-text-muted tabular-nums"
            aria-live="polite"
            aria-atomic="true"
            data-testid="find-bar-count"
          >
            {countLabel()}
          </span>
          <button
            type="button"
            class="den-editor-command-bar__button"
            aria-label="Find previous"
            data-tip={withBinding("Find previous", "find.prev")}
            data-testid="find-bar-prev"
            onClick={() => findPrev()}
          >
            ↑
          </button>
          <button
            type="button"
            class="den-editor-command-bar__button"
            aria-label="Find next"
            data-tip={withBinding("Find next", "find.next")}
            data-testid="find-bar-next"
            onClick={() => findNext()}
          >
            ↓
          </button>
        </div>
        <Show when={replaceLaneOpen()}>
          <div
            id="den-find-replace-lane"
            class="den-find-bar__lane den-find-bar__lane--replace"
          >
            <label
              class="den-editor-command-bar__label"
              for="den-find-replacement"
            >
              Replace
            </label>
            <input
              id="den-find-replacement"
              ref={setReplacementEl}
              class="den-editor-command-bar__input den-input"
              type="text"
              autocomplete="off"
              spellcheck={false}
              placeholder="Replace with"
              value={findController.replacement()}
              data-testid="find-bar-replacement"
              onInput={(e) => setFindReplacement(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter") return;
                e.preventDefault();
                e.stopPropagation();
                replaceCurrentFindMatch();
              }}
            />
            <button
              type="button"
              class="den-editor-command-bar__button"
              aria-label="Replace current match"
              data-tip="Replace current match"
              data-tip-when-clipped=".den-find-bar__full-label"
              data-testid="find-bar-replace-current"
              onClick={() => replaceCurrentFindMatch()}
            >
              <span class="den-find-bar__full-label">Replace</span>
              <span class="den-find-bar__compact-label" aria-hidden="true">
                R
              </span>
            </button>
            <button
              type="button"
              class="den-editor-command-bar__button"
              aria-label="Replace all matches"
              data-tip={
                replaceAllDisabled()
                  ? "Refine the query before replacing all matches"
                  : "Replace all matches"
              }
              data-tip-when-clipped={replaceAllDisabled() ? undefined : ".den-find-bar__full-label"}
              disabled={replaceAllDisabled()}
              data-testid="find-bar-replace-all"
              onClick={() => replaceAllFindMatches()}
            >
              <span class="den-find-bar__full-label">Replace all</span>
              <span class="den-find-bar__compact-label" aria-hidden="true">
                All
              </span>
            </button>
          </div>
        </Show>
        <div class="den-find-bar__lane den-find-bar__lane--options">
          <label
            class="den-find-bar__toggle"
            data-tip="Match case"
            data-tip-when-clipped=".den-find-bar__full-label"
          >
            <DenCheckboxControl
              aria-label="Match case"
              checked={findController.caseSensitive()}
              data-testid="find-bar-case"
              onChange={(e) => setFindCaseSensitive(e.currentTarget.checked)}
            />
            <span class="den-find-bar__full-label">Match case</span>
            <span class="den-find-bar__compact-label" aria-hidden="true">
              Aa
            </span>
          </label>
          <Show when={showScope()}>
            <label
              class="den-find-bar__toggle"
              data-tip={
                scopeEnabled()
                  ? "Limit find to the selection frozen when you turn this on"
                  : "Select two or more lines to find in selection"
              }
            >
              <DenCheckboxControl
                aria-label="Find in selection"
                checked={findController.scopeActive()}
                disabled={
                  !findController.scopeActive() && !findSelectionAllowsScope()
                }
                data-testid="find-bar-scope"
                onChange={(e) => {
                  setFindScopeToSelection(e.currentTarget.checked);
                }}
              />
              <span class="den-find-bar__full-label">Find in selection</span>
              <span class="den-find-bar__compact-label" aria-hidden="true">
                Lines
              </span>
            </label>
          </Show>
          <Show when={showScope()}>
            <button
              type="button"
              class="den-editor-command-bar__button"
              aria-label="Select all matches"
              data-tip={withBinding(
                "Select all matches",
                "find.selectAllMatches",
              )}
              data-testid="find-bar-select-all"
              onClick={() => selectAllFindMatches()}
            >
              <span class="den-find-bar__full-label">Select all</span>
              <span class="den-find-bar__compact-label" aria-hidden="true">
                Select
              </span>
            </button>
          </Show>
          <Show when={findController.query().trim()}>
            <button
              type="button"
              class="den-editor-command-bar__button"
              aria-label="Find everywhere"
              data-tip={withBinding(
                "Find everywhere — carries this query",
                "find.everywhere",
              )}
              data-testid="find-bar-everywhere"
              onClick={() => {
                closeFind();
                invokeCommand("find.everywhere");
              }}
            >
              <span class="den-find-bar__full-label">Find everywhere…</span>
              <span class="den-find-bar__compact-label" aria-hidden="true">
                Project…
              </span>
            </button>
          </Show>
        </div>
        <button
          type="button"
          class="den-editor-command-bar__button den-editor-command-bar__close den-find-bar__close"
          aria-label="Close find"
          data-testid="find-bar-close"
          onClick={() => closeFind()}
        >
          ×
        </button>
      </div>
    </Show>
  );
}
