import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { For, Show, batch, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type {
  ContributionCommand,
  ContributionSearchResult,
  ContributionSearchSource,
  ProjectRoot,
  SearchHit,
} from "../../api/types.ts";
import { openRevisionDiffs } from "../../files/review/revision-diffs.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { revealTypedPathInFileManager } from "../../platform/files/reveal-in-file-manager.ts";
import { recordInventoryOpen, type InventoryFile } from "../../files/tree/file-inventory.ts";
import { createOverlayScopeFocusTrap, shellChromeInertTargets } from "../../platform/interaction/modal-focus-trap.ts";
import { searchQueryFreeText } from "../../search/search-query-model.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import {
  listRecentActionIds,
  listRecentQueries,
  recordRecentAction,
  recordRecentQuery,
} from "../../search/search-history.ts";
import { loadSearchMatchPrefs, saveSearchMatchPrefs, type SearchMatchPrefs } from "../../search/search-match-prefs.ts";
import {
  actionableCrossbarRows,
  buildCrossbarRows,
  canEscalateCrossbar,
  effectiveCrossbarMode,
  nextCrossbarIndex,
  crossbarArm,
  type CrossbarGotoTarget,
  type CrossbarRow,
  type CrossbarSymbolRow,
} from "../../search/crossbar-model.ts";
import {
  SEARCH_MODE_COMMAND_BY_MODE,
  CROSSBAR_MODE_LABEL,
  CROSSBAR_MODES,
  type CrossbarMode,
} from "../../search/crossbar-modes.ts";
import {
  ariaKeyShortcutsForHandler,
  bindingForHandler,
} from "../../shortcuts/display-binding-for.ts";
import { attachDispatcher, registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import {
  contributionCommandEnabled,
  dispatchContributionCommand,
  reportDispatchFailure,
  type CrossbarCommandState,
} from "../../contributions/dispatch.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { ContributionCommandFlow } from "./ContributionCommandFlow.tsx";
import { createTablistKeyboard } from "../../platform/interaction/roving-focus.ts";
import { sourceDisabledLabel, globIndicatorTitle } from "./CrossbarResults.tsx";
import { handleCrossbarPanelKey } from "../../search/crossbar-panel-keys.ts";
import { useCrossbarDataSources } from "./useCrossbarDataSources.ts";
import { CrossbarRowList, outsideProjectFolder } from "./CrossbarRowList.tsx";

export type CrossbarProps = {
  open: boolean;
  originProjectId: string | null;
  projectRoots?: readonly Pick<ProjectRoot, "id" | "label">[];
  originSessionId?: string | null;
  seed?: string;
  /** Reseeds an open surface when changed. */
  seedSerial?: number;
  initialMode?: CrossbarMode;
  initialCommandId?: string;
  /** Most-recent shell navigation targets. */
  gotoTargets?: readonly CrossbarGotoTarget[];
  /** One availability snapshot and its filtered commands. */
  contributionActions?: CrossbarCommandState;
  onClose: () => void;
  onEscalate: (args: {
    query: string;
    originProjectId: string | null;
    mode: CrossbarMode;
  }) => void;
  onNavigateHit: (hit: SearchHit, matchText?: string) => void;
  onNavigateTarget?: (target: CrossbarGotoTarget) => void;
  /** Proposes a folder outside every project as a new project. */
  onOpenFolderAsProject?: (path: string) => void;
};

export function Crossbar(props: CrossbarProps) {
  const [query, setQuery] = createSignal("");
  const [mode, setMode] = createSignal<CrossbarMode>("everything");
  const [activeIndex, setActiveIndex] = createSignal(0);
  const [recentQueries, setRecentQueries] = createSignal<string[]>([]);
  const [recentActionIds, setRecentActionIds] = createSignal<string[]>([]);
  const [dialogEl, setDialogEl] = createSignal<HTMLDivElement | undefined>();
  const [matchPrefs, setMatchPrefs] = createSignal<SearchMatchPrefs>(loadSearchMatchPrefs());
  const [flowCommand, setFlowCommand] = createSignal<ContributionCommand | null>(null);
  const [flowInitialAnswers, setFlowInitialAnswers] = createSignal<Record<string, unknown> | undefined>();
  const [flowAutoSubmit, setFlowAutoSubmit] = createSignal(false);
  const [awaitingActivation, setAwaitingActivation] = createSignal(false);

  /** The tab the query shows: a `>` or `#` prefix chooses Actions or Symbols. */
  const activeMode = () => effectiveCrossbarMode(query(), mode());

  const {
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
  } = useCrossbarDataSources({
    open: () => props.open,
    originProjectId: () => props.originProjectId,
    originSessionId: () => props.originSessionId,
    query,
    mode,
    activeMode,
    matchPrefs,
  });

  let seededForOpen = false;
  let seededSerial: number | undefined;
  let inputEl: HTMLInputElement | undefined;
  let modesEl: HTMLDivElement | undefined;
  let lastActiveRowId: string | undefined;
  let restoreFocusOnClose = true;

  const cancelAwaitingActivation = () => {
    setAwaitingActivation(false);
  };

  const resetActiveSelection = () => {
    lastActiveRowId = undefined;
    setActiveIndex(0);
  };

  const selectMode = (next: CrossbarMode) => {
    const arm = crossbarArm(query());
    // A tab replaces the `>` and `#` shortcuts.
    const prefixed = arm.kind === "actions" || (arm.kind === "search" && arm.mode !== undefined);
    const nextQuery = activeSource() ? activeSourceQuery() : prefixed ? arm.text : query();
    cancelAwaitingActivation();
    resetActiveSelection();
    batch(() => {
      setMode(next);
      setQuery(nextQuery);
    });
    scheduleSearch(nextQuery);
  };

  const patchMatchPrefs = (patch: Partial<SearchMatchPrefs>) => {
    cancelAwaitingActivation();
    const next = { ...matchPrefs(), ...patch };
    setMatchPrefs(next);
    saveSearchMatchPrefs(next);
    scheduleSearch(query());
  };

  createTablistKeyboard(() => modesEl);

  createEffect(() => {
    if (props.open) restoreFocusOnClose = true;
  });

  createOverlayScopeFocusTrap(
    () => props.open,
    dialogEl,
    {
      inertTarget: () => shellChromeInertTargets(),
      restoreFocus: () => restoreFocusOnClose,
    },
  );

  createEffect(() => {
    if (!props.open) {
      seededForOpen = false;
      cancelWhenClosed();
      setFlowCommand(null);
      setFlowInitialAnswers(undefined);
      setFlowAutoSubmit(false);
      setAwaitingActivation(false);
      setSearching(false);
      return;
    }
    // A new serial reseeds an open surface.
    if (!seededForOpen || seededSerial !== props.seedSerial) {
      seededForOpen = true;
      seededSerial = props.seedSerial;
      setFlowCommand(null);
      setFlowInitialAnswers(undefined);
      setFlowAutoSubmit(false);
      setAwaitingActivation(false);
      resetActiveSelection();
      setQuery(props.seed?.trim() ?? "");
      setMode(props.initialMode ?? "everything");
      resetSearchState();
      setMatchPrefs(loadSearchMatchPrefs());
      setRecentQueries(listRecentQueries().map((r) => r.query));
      setRecentActionIds(listRecentActionIds());
      if (props.initialCommandId) {
        const command = contributionFrame()?.commands.find((row) => row.id === props.initialCommandId);
        if (command) setFlowCommand(command);
      }
      if (props.seed?.trim()) scheduleSearch(props.seed.trim());
    }
    const detachUp = registerCommandHandler("list.up", () => {
      if (flowCommand()) return;
      moveActive(-1);
    });
    const detachDown = registerCommandHandler("list.down", () => {
      if (flowCommand()) return;
      moveActive(1);
    });
    const detachFirst = registerCommandHandler("list.first", () => {
      if (flowCommand()) return;
      if (selectionLength() > 0) setActiveTo(0);
    });
    const detachLast = registerCommandHandler("list.last", () => {
      if (flowCommand()) return;
      const len = selectionLength();
      if (len > 0) setActiveTo(len - 1);
    });
    const detachConfirm = registerCommandHandler("list.confirm", () => {
      if (flowCommand()) return;
      activateActive();
    });
    const detachListener = attachDispatcher();
    onCleanup(() => {
      detachUp();
      detachDown();
      detachFirst();
      detachLast();
      detachConfirm();
      detachListener();
    });
  });

  const rows = createMemo(() =>
    buildCrossbarRows({
      query: query(),
      mode: mode(),
      hits: hits(),
      includeEscalate: canEscalateCrossbar(query(), hits().length),
      recentQueries: recentQueries(),
      recentActionIds: recentActionIds(),
      gotoTargets: props.gotoTargets,
      originProjectId: props.originProjectId,
      inventoryFiles: inventoryFiles(),
      symbolRows: symbolRows(),
      lineTarget: lineTarget(),
      outside: indexPage().query === query().trim() ? indexPage().outside ?? null : null,
      canRevealOutside: canRevealOutside(),
      revisions: revisions().query === query().trim() ? revisions().comparisons : [],
      contributionCommands: props.contributionActions?.commands,
    }),
  );

  const selectable = createMemo(() => actionableCrossbarRows(rows()));

  // Selection follows the active result lane.
  const selectionLength = () =>
    activeSource() ? sourceResults().length : selectable().length;

  // Preserve selection as rows arrive.
  const setActiveTo = (index: number) => {
    lastActiveRowId = activeSource() ? undefined : selectable()[index]?.id;
    setActiveIndex(index);
  };
  const moveActive = (delta: 1 | -1) => {
    setActiveTo(nextCrossbarIndex(activeIndex(), selectionLength(), delta));
  };

  const handoff = (navigate: () => void) => {
    restoreFocusOnClose = false;
    props.onClose();
    navigate();
  };

  createEffect(() => {
    if (activeSource()) {
      // Lane results replace wholesale; reset with each result set.
      sourceResults();
      resetActiveSelection();
      return;
    }
    const list = selectable();
    if (list.length === 0) {
      resetActiveSelection();
      return;
    }
    // Follow the selected row when async rows shift it.
    if (lastActiveRowId) {
      const at = list.findIndex((row) => row.id === lastActiveRowId);
      if (at >= 0) {
        if (at !== activeIndex()) setActiveIndex(at);
        return;
      }
      lastActiveRowId = undefined;
    }
    if (activeIndex() >= list.length) setActiveIndex(list.length - 1);
  });

  const escalateTo = (target: CrossbarMode) => {
    if (activeSource()) return;
    if (!canEscalateCrossbar(query(), hits().length)) return;
    const args = {
      query: query(),
      originProjectId: props.originProjectId,
      mode: target,
    };
    handoff(() => props.onEscalate(args));
  };
  const escalate = () => escalateTo(activeMode());

  const runAction = (command: ContributionCommand) => {
    if (!contributionCommandEnabled(command)) return;
    recordRecentAction(command.id);
    if (!(command.input?.length || command.interaction || command.result_treatment === "output")) {
      props.onClose();
      // Report failures after the panel closes.
      const pid = projectId() || null;
      void dispatchContributionCommand(command.id, {
        client: getLycaonClient(),
        projectId: pid,
        sessionId: props.originSessionId ?? null,
      }).then((result) => reportDispatchFailure(result, pid, command.title));
      return;
    }
    setFlowInitialAnswers(undefined);
    setFlowAutoSubmit(false);
    setFlowCommand(command);
  };

  const activateSourceResult = (source: ContributionSearchSource, result: ContributionSearchResult) => {
    const command = contributionFrame()?.commands.find((row) => row.id === source.activation_command);
    if (!command || !contributionCommandEnabled(command)) return;
    recordRecentAction(command.id);
    setFlowInitialAnswers((result.arguments ?? {}) as Record<string, unknown>);
    setFlowAutoSubmit(true);
    setFlowCommand(command);
  };

  const openInventoryFile = (file: InventoryFile) => {
    const pid = projectId();
    if (!pid) return;
    if (query().trim()) recordRecentQuery(query(), props.originProjectId);
    recordInventoryOpen(pid, file);
    const location = indexPage().location;
    handoff(() => {
      void openSourceLocation({
        intent: "permanent",
        focus: true,
        projectId: pid,
        rootId: file.rootId,
        path: file.path,
        ...(location ? { line: location.line } : {}),
        ...(location?.column != null ? { column: location.column } : {}),
        ...(location?.end_line != null ? { endLine: location.end_line } : {}),
      });
    });
  };

  const goToLine = (row: Extract<CrossbarRow, { kind: "line" }>) => {
    const pid = projectId();
    if (!pid) return;
    handoff(() => {
      void openSourceLocation({
        intent: "permanent",
        focus: true,
        projectId: pid,
        rootId: row.target.rootId,
        path: row.target.path,
        line: row.line,
        ...(row.column != null ? { column: row.column } : {}),
        ...(row.target.jobId ? { jobId: row.target.jobId } : {}),
      });
    });
  };

  const actOnOutside = (row: Extract<CrossbarRow, { kind: "outside" }>) => {
    if (row.action === "reveal") {
      handoff(() => void revealTypedPathInFileManager(row.outside.path));
      return;
    }
    const folder = outsideProjectFolder(row.outside);
    handoff(() => props.onOpenFolderAsProject?.(folder));
  };

  const openSymbol = (symbol: CrossbarSymbolRow) => {
    const pid = projectId();
    if (!pid) return;
    handoff(() => {
      void openSourceLocation({
        intent: "permanent",
        focus: true,
        projectId: pid,
        rootId: symbol.rootId,
        path: symbol.path,
        line: symbol.line,
      });
    });
  };

  const openHit = (hit: SearchHit) => {
    if (query().trim()) recordRecentQuery(query(), props.originProjectId);
    const matchText = searchQueryFreeText(query());
    handoff(() => openSearchHit(hit, (h) => props.onNavigateHit(h, matchText)));
  };

  const rerunRecent = (recent: string) => {
    resetActiveSelection();
    setQuery(recent);
    scheduleSearch(recent);
    inputEl?.focus();
  };

  const activateRow = (row: Exclude<CrossbarRow, { kind: "section" }>) => {
    if (row.kind === "action") {
      runAction(row.command);
      return;
    }
    if (row.kind === "hit") {
      openHit(row.hit);
      return;
    }
    if (row.kind === "inventory-file") {
      openInventoryFile(row.file);
      return;
    }
    if (row.kind === "symbol") {
      openSymbol(row.symbol);
      return;
    }
    if (row.kind === "line") {
      goToLine(row);
      return;
    }
    if (row.kind === "outside") {
      actOnOutside(row);
      return;
    }
    if (row.kind === "revision") {
      const pid = projectId();
      if (!pid) return;
      if (query().trim()) recordRecentQuery(query(), props.originProjectId);
      handoff(() => openRevisionDiffs(pid, row.comparison));
      return;
    }
    if (row.kind === "recent") {
      rerunRecent(row.query);
      return;
    }
    if (row.kind === "goto") {
      handoff(() => props.onNavigateTarget?.(row.target));
      return;
    }
    escalate();
  };

  const activateActive = () => {
    const source = activeSource();
    if (source) {
      const result = sourceResults()[activeIndex()];
      if (result) activateSourceResult(source, result);
      return;
    }
    const currentRows = selectable();
    // Enter waits for an active search result.
    if (searchBusy() && currentRows.every((r) => r.kind === "escalate")) {
      setAwaitingActivation(true);
      return;
    }
    const row = currentRows[activeIndex()];
    if (row) activateRow(row);
  };

  createEffect(() => {
    if (!awaitingActivation() || searchBusy()) return;
    setAwaitingActivation(false);
    activateActive();
  });

  const onQueryInput = (value: string) => {
    cancelAwaitingActivation();
    resetActiveSelection();
    setQuery(value);
    scheduleSearch(value);
  };

  const onPanelKeyDown = (e: KeyboardEvent) =>
    handleCrossbarPanelKey(e, {
      dismiss: () => (flowCommand() ? setFlowCommand(null) : props.onClose()),
      activeMode,
      selectMode,
      toggleMatch: (key) => patchMatchPrefs({ [key]: !matchPrefs()[key] }),
      // A prefixed source query is meaningless in the depth view.
      escalate: () => {
        if (!activeSource()) escalate();
      },
    });

  const modeTooltip = (m: CrossbarMode): string => {
    return `${CROSSBAR_MODE_LABEL[m]} (${bindingForHandler(SEARCH_MODE_COMMAND_BY_MODE[m])})`;
  };

  return (
    <Show when={props.open}>
      <div
        class="den-dialog-backdrop den-crossbar-backdrop"
        data-testid="crossbar-backdrop"
        onClick={() => props.onClose()}
      >
        {/* Shell chrome is inert while open — keep window drag on this strip. */}
        <ChromeDragSurface class="den-crossbar__chrome-drag" />
        <div
          class="den-crossbar"
          role="dialog"
          aria-modal="true"
          aria-label="Crossbar"
          data-testid="crossbar"
          ref={setDialogEl}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={onPanelKeyDown}
        >
          <Show
            when={!flowCommand()}
            fallback={
              <Show when={flowCommand()} keyed>
                {(command) => (
                  <ContributionCommandFlow
                    command={command}
                    deps={{
                      client: getLycaonClient(),
                      projectId: projectId() || null,
                      sessionId: props.originSessionId ?? null,
                    }}
                    onBack={() => setFlowCommand(null)}
                    onClose={props.onClose}
                    initialAnswers={flowInitialAnswers()}
                    autoSubmit={flowAutoSubmit()}
                  />
                )}
              </Show>
            }
          >
            <div class="den-crossbar__search">
              <ThemeIcon slot="search" size={17} />
              <input
                autofocus
                class="den-crossbar__input"
                data-testid="crossbar-input"
                value={query()}
                placeholder="Crossbar…"
                autocomplete="off"
                spellcheck={false}
                ref={(el) => (inputEl = el)}
                onInput={(e) => onQueryInput(e.currentTarget.value)}
              />
              <div
                class="den-search-query__toggles"
                role="group"
                aria-label="Match options"
                data-testid="crossbar-match-toggles"
              >
                <button
                  type="button"
                  class="den-search-query__toggle"
                  data-tip={`Match case (${bindingForHandler("search.toggleMatchCase")})`}
                  aria-label="Match case"
                  aria-keyshortcuts={ariaKeyShortcutsForHandler("search.toggleMatchCase")}
                  aria-pressed={matchPrefs().caseSensitive}
                  data-testid="crossbar-toggle-case"
                  onClick={() =>
                    patchMatchPrefs({
                      caseSensitive: !matchPrefs().caseSensitive,
                    })
                  }
                >
                  Aa
                </button>
                <button
                  type="button"
                  class="den-search-query__toggle"
                  data-tip={`Whole word (${bindingForHandler("search.toggleWholeWord")})`}
                  aria-label="Whole word"
                  aria-keyshortcuts={ariaKeyShortcutsForHandler("search.toggleWholeWord")}
                  aria-pressed={matchPrefs().wholeWord}
                  data-testid="crossbar-toggle-word"
                  onClick={() => patchMatchPrefs({ wholeWord: !matchPrefs().wholeWord })}
                >
                  \b
                </button>
                <button
                  type="button"
                  class="den-search-query__toggle"
                  data-tip={`Regular expression (${bindingForHandler("search.toggleRegex")})`}
                  aria-label="Regular expression"
                  aria-keyshortcuts={ariaKeyShortcutsForHandler("search.toggleRegex")}
                  aria-pressed={matchPrefs().regex}
                  data-testid="crossbar-toggle-regex"
                  onClick={() => patchMatchPrefs({ regex: !matchPrefs().regex })}
                >
                  .*
                </button>
                {/* Active path filters narrow quick-search results. */}
                <Show when={matchPrefs().include.trim() || matchPrefs().exclude.trim()}>
                  <button
                    type="button"
                    class="den-search-query__toggle den-search-query__toggle--globs"
                    data-tip={globIndicatorTitle(matchPrefs())}
                    aria-label="Clear path filters"
                    data-testid="crossbar-glob-indicator"
                    onClick={() => patchMatchPrefs({ include: "", exclude: "" })}
                  >
                    Paths filtered ×
                  </button>
                </Show>
              </div>
              <Show when={searchBusy()}>
                <span
                  class="den-crossbar__spinner"
                  data-testid="crossbar-searching"
                  aria-hidden="true"
                />
              </Show>
            </div>

            <div
              ref={modesEl}
              class="den-crossbar__modes"
              role="tablist"
              aria-label="Search modes"
              data-testid="crossbar-modes"
            >
              <For each={[...CROSSBAR_MODES]}>
                {(m) => (
                  <button
                    type="button"
                    role="tab"
                    class="den-crossbar__mode"
                    classList={{ "den-crossbar__mode--active": !activeSource() && activeMode() === m }}
                    aria-selected={!activeSource() && activeMode() === m}
                    data-tip={modeTooltip(m)}
                    data-testid={`crossbar-mode-${m}`}
                    onClick={() => selectMode(m)}
                  >
                    {CROSSBAR_MODE_LABEL[m]}
                  </button>
                )}
              </For>
              <Show when={searchSources().length > 0}>
                <span class="den-crossbar__source-label" aria-hidden="true">
                  Sources
                </span>
              </Show>
              <For each={searchSources()}>
                {(source) => (
                  <button
                    type="button"
                    role="tab"
                    class="den-crossbar__mode"
                    classList={{ "den-crossbar__mode--active": activeSource()?.id === source.id }}
                    aria-selected={activeSource()?.id === source.id}
                    data-tip={source.ready ? undefined : sourceDisabledLabel(source)}
                    onClick={() =>
                      onQueryInput(`${source.prefix}: ${activeSource() ? activeSourceQuery() : query()}`)
                    }
                  >
                    {source.label}
                  </button>
                )}
              </For>
            </div>

            <CrossbarRowList
              query={query()}
              mode={mode()}
              activeMode={activeMode()}
              rows={rows()}
              selectable={selectable()}
              activeIndex={activeIndex()}
              searchBusy={searchBusy()}
              hitCount={hits().length}
              coverageNote={coverageNote()}
              lineTarget={lineTarget()}
              projectRoots={props.projectRoots}
              contributionActions={props.contributionActions}
              activeSource={activeSource()}
              activeSourceQuery={activeSourceQuery()}
              sourceResults={sourceResults()}
              sourceError={sourceError()}
              dialogEl={dialogEl}
              onSelectIndex={setActiveTo}
              onActivateRow={activateRow}
              onEscalateTo={escalateTo}
              onActivateSourceResult={activateSourceResult}
              onConfigureSource={() => {
                const settings = contributionFrame()?.commands.find(
                  (command) => command.handler_id === "settings.open",
                );
                if (settings) runAction(settings);
              }}
              renderStatusBanner={({ actionStatus, showHitStatus, coverageNote: note, hitStatusLabel }) => (
                <>
                  <Show when={actionStatus}>
                    {(label) => (
                      <li class="den-crossbar__status" role="status">
                        {label()}
                      </li>
                    )}
                  </Show>
                  <Show when={!showHitStatus && note}>
                    <li
                      class="den-crossbar__status den-crossbar__coverage"
                      role="status"
                      data-testid="crossbar-coverage"
                    >
                      {note}
                    </li>
                  </Show>
                  <Show when={showHitStatus}>
                    <li
                      class="den-crossbar__status"
                      data-testid="crossbar-hit-status"
                      aria-live="polite"
                    >
                      {hitStatusLabel}
                    </li>
                  </Show>
                </>
              )}
            />

            <div class="den-crossbar__footer" data-testid="crossbar-footer">
              <span class="den-crossbar__hint">
                <kbd class="den-crossbar__key">↑</kbd>
                <kbd class="den-crossbar__key">↓</kbd>
                Navigate
              </span>
              <span class="den-crossbar__hint">
                <kbd class="den-crossbar__key">{bindingForHandler("list.confirm")}</kbd>
                Open
              </span>
              <span class="den-crossbar__hint">
                <kbd class="den-crossbar__key">{bindingForHandler("search.mode.next")}</kbd>
                Modes
              </span>
              <span class="den-crossbar__hint den-crossbar__hint--dismiss">
                <kbd class="den-crossbar__key">{bindingForHandler("overlay.dismiss")}</kbd>
                Close
              </span>
            </div>
          </Show>
        </div>
      </div>
    </Show>
  );
}
