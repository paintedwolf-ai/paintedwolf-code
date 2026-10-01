import { createCrossbarRequests, SEARCH_DEBOUNCE_MS } from "./crossbar-requests.ts";
import { searchCoverageNote } from "../../search/search-status.ts";
import { createSourceSearchRefresh } from "../../search/source-search-refresh.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { For, Show, batch, createEffect, on, createMemo, createSignal, onCleanup } from "solid-js";
import type {
  ContributionSearchResult,
  ContributionSearchSource,
  ProjectRoot,
  SearchHit,
  SearchResponse,
  SourceIndexRootCoverage,
  SourceRevisionComparison,
  SourceSearchLocation,
  SourceSearchOutside,
} from "../../api/types.ts";
import { looksLikeRevisionSpec, openRevisionDiffs, resolveRevisionComparisons } from "../../files/review/revision-diffs.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { basenameOfPath } from "../../api/project-path.ts";
import { revealTypedPathInFileManager } from "../../platform/files/reveal-in-file-manager.ts";
import { localPathDestinationLabel } from "../../platform/navigation/open-local-path.ts";
import { hostSharesDevice } from "../../platform/connection/host-identity.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import {
  inventoryMru,
  orderIdleInventory,
  recordInventoryOpen,
  watchIndexedFiles,
  type InventoryFile,
} from "../../files/tree/file-inventory.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { fetchCachedSourceSymbols } from "../../files/source/source-symbols-cache.ts";
import { createOverlayScopeFocusTrap, shellChromeInertTargets } from "../../platform/interaction/modal-focus-trap.ts";
import { ATTENTION_CLASS_LABEL } from "../../attention/attention-model.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
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
  CROSSBAR_GOTO_KIND_LABEL,
  actionableCrossbarRows,
  buildCrossbarRows,
  canEscalateCrossbar,
  cycleCrossbarMode,
  effectiveCrossbarMode,
  nextCrossbarIndex,
  crossbarArm,
  crossbarAwaitsSymbolName,
  crossbarHitStatus,
  crossbarNoMatchLabel,
  type CrossbarGotoTarget,
  type CrossbarLineTarget,
  type CrossbarRow,
  type CrossbarSymbolRow,
} from "../../search/crossbar-model.ts";
import {
  SEARCH_MODE_COMMAND_BY_MODE,
  SEARCH_MODE_JUMP_COMMANDS,
  CROSSBAR_MODE_LABEL,
  CROSSBAR_MODES,
  type CrossbarMode,
} from "../../search/crossbar-modes.ts";
import {
  ariaKeyShortcutsForHandler,
  bindingForHandler,
  displayBindingFor,
  handlerMatchesEvent,
} from "../../shortcuts/display-binding-for.ts";
import { attachDispatcher, registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import type { ContributionCommand } from "../../api/types.ts";
import {
  contributionCommandEnabled,
  dispatchContributionCommand,
  reportDispatchFailure,
} from "../../contributions/dispatch.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import {
  ActionCommandIcon,
  EscalateArrowIcon,
  GotoTargetIcon,
  PaletteSlotIcon,
  RecentQueryIcon,
  SearchKindIcon,
} from "./SearchIcons.tsx";
import { ContributionCommandFlow } from "./ContributionCommandFlow.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { createTablistKeyboard } from "../../platform/interaction/roving-focus.ts";
import {
  commandDisabledReason,
  ContributionSearchLane,
  sourceDisabledLabel,
  globIndicatorTitle,
  renderMatchUnderline,
} from "./CrossbarResults.tsx";

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
  /** Availability-filtered contribution commands from the hydrated frame. */
  contributionCommands?: readonly ContributionCommand[];
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

type IndexPage = { query: string; files: InventoryFile[]; location?: SourceSearchLocation; outside?: SourceSearchOutside };
const EMPTY_INDEX_PAGE: IndexPage = { query: "", files: [] };

export function Crossbar(props: CrossbarProps) {
  const [query, setQuery] = createSignal("");
  const [mode, setMode] = createSignal<CrossbarMode>("everything");
  const [hits, setHits] = createSignal<SearchHit[]>([]);
  const [searching, setSearching] = createSignal(false);
  const [activeIndex, setActiveIndex] = createSignal(0);
  const [recentQueries, setRecentQueries] = createSignal<string[]>([]);
  const [recentActionIds, setRecentActionIds] = createSignal<string[]>([]);
  /** The newest file page and the query it answered. */
  const [indexPage, setIndexPage] = createSignal<IndexPage>(EMPTY_INDEX_PAGE);
  const [indexing, setIndexing] = createSignal(false);
  const [indexError, setIndexError] = createSignal("");
  const [indexCoverage, setIndexCoverage] = createSignal<SourceIndexRootCoverage[]>([]);
  const [indexRevision, setIndexRevision] = createSignal(0);
  const searchBusy = () => searching() || indexing();
  const [symbolRows, setSymbolRows] = createSignal<CrossbarSymbolRow[]>([]);
  const [dialogEl, setDialogEl] = createSignal<HTMLDivElement | undefined>();
  const [matchPrefs, setMatchPrefs] = createSignal<SearchMatchPrefs>(
    loadSearchMatchPrefs(),
  );
  const [flowCommand, setFlowCommand] = createSignal<ContributionCommand | null>(null);
  const [flowInitialAnswers, setFlowInitialAnswers] = createSignal<Record<string, unknown> | undefined>();
  const [flowAutoSubmit, setFlowAutoSubmit] = createSignal(false);
  const [sourceResults, setSourceResults] = createSignal<ContributionSearchResult[]>([]);
  const [sourceError, setSourceError] = createSignal("");
  const [searchErrorNote, setSearchErrorNote] = createSignal("");
  const [searchCoverage, setSearchCoverage] = createSignal<Pick<SearchResponse, "issues" | "exhaustive"> | null>(null);

  const searchSources = () => contributionFrame()?.search_sources ?? [];
  const activeSource = createMemo<ContributionSearchSource | null>(() => {
    // Accept auto-capitalized source prefixes.
    const prefix = query()
      .trimStart()
      .match(/^([a-z0-9]+(?:-[a-z0-9]+)*):/i)?.[1]
      ?.toLowerCase();
    return searchSources().find((source) => source.prefix === prefix) ?? null;
  });
  const activeSourceQuery = () => {
    const raw = query().trimStart();
    const separator = raw.indexOf(":");
    return separator < 0 ? "" : raw.slice(separator + 1).trim();
  };

  /** The tab the query shows: a `>` or `#` prefix chooses Actions or Symbols. */
  const activeMode = () => effectiveCrossbarMode(query(), mode());

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

  const { runSearch, scheduleSearch, cancelWhenClosed, dispose: disposeSearchRequests } = createCrossbarRequests({
    open: () => props.open, originProjectId: () => props.originProjectId, mode,
    hasLocalFileArm: () => hasLocalFileArm(), activeSource, matchPrefs,
    setHits, setSearchCoverage, setSearchErrorNote, setSearching,
  });
  let seededForOpen = false;
  let seededSerial: number | undefined;
  let inputEl: HTMLInputElement | undefined;
  let modesEl: HTMLDivElement | undefined;
  let lastActiveRowId: string | undefined;
  let restoreFocusOnClose = true;
  const [awaitingActivation, setAwaitingActivation] = createSignal(false);

  const resetActiveSelection = () => {
    lastActiveRowId = undefined;
    setActiveIndex(0);
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

  const hasLocalFileArm = () => projectId().length > 0;

  createSourceSearchRefresh(
    () => props.open && !activeSource(),
    async () => {
      setIndexRevision(value => value + 1);
      await runSearch(query(), "background");
    },
  );

  const projectId = () => props.originProjectId?.trim() ?? "";
  const sessionId = () => props.originSessionId?.trim() || undefined;

  createEffect(on(projectId, () => {
    if (props.open) {
      setHits([]);
      setSearchCoverage(null);
      scheduleSearch(query());
    }
  }, { defer: true }));

  createEffect(() => {
    const source = activeSource();
    const sourceQuery = activeSourceQuery();
    const pid = projectId();
    const client = getLycaonClient();
    if (!props.open || !source) {
      setSourceResults([]);
      setSourceError("");
      if (!props.open || !activeSource()) setSearching(false);
      return;
    }
    if (!source.ready) { setSourceResults([]); setSourceError(sourceDisabledLabel(source)); setSearching(false); return; }
    if (!pid || !client || sourceQuery.length < source.min_query_length) {
      setSourceResults([]); setSourceError(""); setSearching(false); return;
    }
    const controller = new AbortController();
    setSearching(false); setSourceError("");
    const timer = setTimeout(() => {
      const frame = contributionFrame();
      if (!frame) return;
      setSearching(true);
      void client.searchContributionSource(pid, source.id, { frame_revision: frame.frame_revision, query: sourceQuery }, controller.signal)
        .then((response) => { if (!controller.signal.aborted) setSourceResults(response.results); })
        .catch((cause: unknown) => { if (!controller.signal.aborted) { setSourceResults([]); setSourceError(cause instanceof Error ? cause.message : String(cause)); } })
        .finally(() => { if (!controller.signal.aborted) setSearching(false); });
    }, SEARCH_DEBOUNCE_MS);
    onCleanup(() => { clearTimeout(timer); controller.abort(); });
  });

  // The host ranks typed queries; only the idle list orders by local recency.
  const inventoryFiles = createMemo((): InventoryFile[] => {
    const pid = projectId();
    if (!pid) return [];
    const q = query().trim();
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
    projectId() && !activeSource() && crossbarArm(query()).kind === "search" &&
    (activeMode() === "everything" || activeMode() === "files");
  const coverageNote = () => {
    const errors = [searchErrorNote(), showsInventory() ? indexError() : ""].filter(Boolean);
    return [...new Set(errors)].join(" ") ||
      searchCoverageNote(searchCoverage(), showsInventory() ? indexCoverage() : []);
  };
  createEffect(() => {
    if (!props.open || activeSource() || (activeMode() !== "everything" && activeMode() !== "files")) return;
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
    const sourceQuery = query().trim();
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
      void watchIndexedFiles(client, pid, sourceQuery, (entries, page) => {
        if (controller.signal.aborted) return;
        inventoryHasResponse = true;
        batch(() => {
          setIndexPage({ query: sourceQuery, files: [...entries], location: page.location, outside: page.outside });
          setIndexCoverage(page.coverage);
          setIndexError(page.state === "failed" ? "The file index could not be loaded. Try again." : "");
          setIndexing(false);
        });
      }, { sessionId: sid, signal: controller.signal }).catch(() => {
        if (!controller.signal.aborted) {
          setIndexError("File results could not refresh. Type to search again.");
        }
      }).finally(() => {
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
    if (crossbarArm(query()).kind !== "file-symbols") {
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
  const [revisions, setRevisions] = createSignal<{ query: string; comparisons: SourceRevisionComparison[] }>({ query: "", comparisons: [] });
  createEffect(() => {
    const q = query().trim();
    const pid = projectId();
    const client = getLycaonClient();
    if (!props.open || activeSource() || !pid || !client || activeMode() !== "everything" ||
      crossbarArm(q).kind !== "search" || !looksLikeRevisionSpec(q)) return;
    const sid = sessionId();
    const controller = new AbortController();
    const timer = setTimeout(() => void (async () => {
      let comparisons: SourceRevisionComparison[] = [];
      try {
        comparisons = await resolveRevisionComparisons(client, pid, q, { sessionId: sid, signal: controller.signal });
      } catch {
        // Not a revision here; other results still answer the query.
      }
      if (!controller.signal.aborted) setRevisions({ query: q, comparisons });
    })(), SEARCH_DEBOUNCE_MS);
    onCleanup(() => {
      clearTimeout(timer);
      controller.abort();
    });
  });

  const cancelAwaitingActivation = () => {
    setAwaitingActivation(false);
  };

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
      setHits([]);
      setSearchErrorNote("");
      setSearchCoverage(null);
      setSearching(false);
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

  onCleanup(disposeSearchRequests);

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
      contributionCommands: props.contributionCommands,
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

  // Escalate renders pinned below the list; everything else scrolls.
  const listRows = createMemo(() =>
    rows().filter(
      (r): r is Exclude<CrossbarRow, { kind: "escalate" }> =>
        r.kind !== "escalate",
    ),
  );

  // End the visible list on a row boundary.
  let listFrame: HTMLDivElement | undefined;
  let listEl: HTMLDivElement | undefined;
  let listContent: HTMLElement | undefined;
  const [listEdges, setListEdges] = createSignal({ top: false, bottom: false });
  const syncListEdges = () => {
    const el = listEl;
    if (!el) return;
    const overflow = el.scrollHeight > el.clientHeight + 1;
    const top = overflow && el.scrollTop > 1;
    const bottom =
      overflow && el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setListEdges((prev) =>
      prev.top === top && prev.bottom === bottom ? prev : { top, bottom },
    );
  };
  const fitListRows = () => {
    const frame = listFrame;
    const el = listEl;
    const content = listContent;
    if (!frame || !el || !content) return;
    frame.style.maxHeight = "";
    const available = el.clientHeight;
    if (el.scrollHeight > available + 1) {
      const fit = wholeRowHeight(content, available);
      if (fit != null) frame.style.maxHeight = `${fit}px`;
    }
    syncListEdges();
  };
  const mountList = (frame: HTMLDivElement) => {
    listFrame = frame;
    if (typeof ResizeObserver === "undefined") return;
    // Refit when the dialog resizes; the frame is the wrap's only in-flow child.
    const observer = new ResizeObserver(fitListRows);
    observer.observe(frame);
    onCleanup(() => {
      observer.disconnect();
      if (listFrame === frame) {
        listFrame = undefined;
        listEl = undefined;
        listContent = undefined;
      }
    });
  };
  createEffect(() => {
    listRows();
    showHitStatusRow();
    // Rows land before their geometry does.
    const frame = requestAnimationFrame(fitListRows);
    onCleanup(() => cancelAnimationFrame(frame));
  });

  const escalateRow = createMemo(() =>
    rows().find((r): r is Extract<CrossbarRow, { kind: "escalate" }> => r.kind === "escalate"),
  );

  const hitStatus = createMemo(() =>
    crossbarHitStatus({
      query: query(),
      mode: activeMode(),
      searching: searchBusy(),
      hitCount: hits().length,
    }),
  );

  // Show status when escalation is the only result.
  const showHitStatusRow = createMemo(() => {
    const arm = crossbarArm(query()).kind;
    if (arm === "file-symbols") return !rows().some((r) => r.kind === "symbol");
    if (crossbarAwaitsSymbolName(query(), mode())) return true;
    if (arm === "line") return !rows().some((r) => r.kind === "line");
    const status = hitStatus();
    if (status !== "searching" && status !== "empty") return false;
    return !rows().some(
      (r) =>
        r.kind === "action" ||
        r.kind === "hit" ||
        r.kind === "goto" ||
        r.kind === "inventory-file" ||
        r.kind === "symbol" ||
        r.kind === "revision" ||
        r.kind === "outside",
    );
  });

  const hitStatusLabel = createMemo(() => {
    const arm = crossbarArm(query());
    if (arm.kind === "file-symbols") return lineTarget() ? "No symbols in this file" : "Open a file to list its symbols";
    if (crossbarAwaitsSymbolName(query(), mode())) return "Type a symbol name";
    if (arm.kind === "line") return lineTarget() ? "Type a line number" : "Open a file to go to a line";
    const status = hitStatus();
    return coverageNote() || (status === "searching" ? "Searching…" : crossbarNoMatchLabel(activeMode()));
  });

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

  createEffect(() => {
    const idx = activeIndex();
    if (idx < 0) return;
    queueMicrotask(() => {
      const activeEl = (listEl ?? dialogEl())?.querySelector<HTMLElement>(
        ".den-crossbar__row--active",
      );
      activeEl?.scrollIntoView({ block: "nearest" });
    });
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
    const rows = selectable();
    // Enter waits for an active search result.
    if (searchBusy() && rows.every((r) => r.kind === "escalate")) {
      setAwaitingActivation(true);
      return;
    }
    const row = rows[activeIndex()];
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

  const onPanelKeyDown = (e: KeyboardEvent) => {
    if (e.isComposing) return;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (flowCommand()) {
        setFlowCommand(null);
      } else {
        props.onClose();
      }
      return;
    }
    const modeStep = handlerMatchesEvent("search.mode.next", e)
      ? 1
      : handlerMatchesEvent("search.mode.prev", e)
        ? -1
        : 0;
    if (modeStep !== 0) {
      e.preventDefault();
      selectMode(cycleCrossbarMode(activeMode(), modeStep));
      return;
    }

    if (handlerMatchesEvent("search.toggleMatchCase", e)) {
      e.preventDefault();
      patchMatchPrefs({ caseSensitive: !matchPrefs().caseSensitive });
      return;
    }
    if (handlerMatchesEvent("search.toggleWholeWord", e)) {
      e.preventDefault();
      patchMatchPrefs({ wholeWord: !matchPrefs().wholeWord });
      return;
    }
    if (handlerMatchesEvent("search.toggleRegex", e)) {
      e.preventDefault();
      patchMatchPrefs({ regex: !matchPrefs().regex });
      return;
    }

    const modeMatch = SEARCH_MODE_JUMP_COMMANDS.find(({ commandId }) =>
      handlerMatchesEvent(commandId, e),
    );
    if (modeMatch) {
      e.preventDefault();
      selectMode(modeMatch.mode);
      return;
    }

    if (handlerMatchesEvent("search.escalate", e)) {
      e.preventDefault();
      // A prefixed source query is meaningless in the depth view.
      if (!activeSource()) escalate();
    }
  };

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
                aria-keyshortcuts={ariaKeyShortcutsForHandler(
                  "search.toggleMatchCase",
                )}
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
                aria-keyshortcuts={ariaKeyShortcutsForHandler(
                  "search.toggleWholeWord",
                )}
                aria-pressed={matchPrefs().wholeWord}
                data-testid="crossbar-toggle-word"
                onClick={() =>
                  patchMatchPrefs({ wholeWord: !matchPrefs().wholeWord })
                }
              >
                \b
              </button>
              <button
                type="button"
                class="den-search-query__toggle"
                data-tip={`Regular expression (${bindingForHandler("search.toggleRegex")})`}
                aria-label="Regular expression"
                aria-keyshortcuts={ariaKeyShortcutsForHandler(
                  "search.toggleRegex",
                )}
                aria-pressed={matchPrefs().regex}
                data-testid="crossbar-toggle-regex"
                onClick={() =>
                  patchMatchPrefs({ regex: !matchPrefs().regex })
                }
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
              <span class="den-crossbar__source-label" aria-hidden="true">Sources</span>
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
                  onClick={() => onQueryInput(`${source.prefix}: ${activeSource() ? activeSourceQuery() : query()}`)}
                >
                  {source.label}
                </button>
              )}
            </For>
          </div>

          <Show when={!activeSource()} fallback={
            <Show when={activeSource()} keyed>
              {(source) => (
                <ContributionSearchLane
                  source={source}
                  query={activeSourceQuery()}
                  results={sourceResults()}
                  error={sourceError()}
                  searching={searchBusy()}
                  activeIndex={activeIndex()}
                  onHover={setActiveTo}
                  onActivate={(result) => activateSourceResult(source, result)}
                  onConfigure={() => {
                    const settings = contributionFrame()?.commands.find((command) => command.handler_id === "settings.open");
                    if (settings) runAction(settings);
                  }}
                />
              )}
            </Show>
          }>
          <div
            class="den-crossbar__list-wrap"
            data-fade-top={listEdges().top ? "" : undefined}
            data-fade-bottom={listEdges().bottom ? "" : undefined}
          >
          <Scrollport
            ref={mountList}
            viewportRef={(el) => (listEl = el)}
            contentRef={(el) => (listContent = el)}
            class="den-crossbar__list"
            contentAs="ul"
            contentClass="den-crossbar__list-content"
            data-testid="crossbar-list"
            viewport={{ onScroll: syncListEdges }}
          >
            <Show when={!showHitStatusRow() && coverageNote()}>
              <li class="den-crossbar__status den-crossbar__coverage" role="status" data-testid="crossbar-coverage">
                {coverageNote()}
              </li>
            </Show>
            <Show when={showHitStatusRow()}>
              <li
                class="den-crossbar__status"
                data-testid="crossbar-hit-status"
                aria-live="polite"
              >
                {hitStatusLabel()}
              </li>
            </Show>
            <For each={listRows()}>
              {(row) => {
                if (row.kind === "section") {
                  return (
                    <li
                      class="den-crossbar__section"
                      data-testid={`crossbar-section-${row.id}`}
                    >
                      <span class="den-crossbar__section-label">{row.label}</span>
                      <Show when={row.more} keyed>
                        {(more) => (
                          <button
                            type="button"
                            class="den-crossbar__section-more"
                            data-testid={`crossbar-see-all-${more}`}
                            aria-label={`See all ${CROSSBAR_MODE_LABEL[more].toLowerCase()} results`}
                            tabIndex={-1}
                            onClick={() => escalateTo(more)}
                          >
                            See all
                          </button>
                        )}
                      </Show>
                    </li>
                  );
                }
                const selIdx = () =>
                  selectable().findIndex((r) => r.id === row.id);
                const active = () => selIdx() === activeIndex();
                if (row.kind === "action") {
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid={`crossbar-action-${row.command.id}`}
                        aria-disabled={!contributionCommandEnabled(row.command)}
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <ActionCommandIcon icon={row.command.icon} />
                        </span>
                        <span class="den-crossbar__primary">
                          {row.command.title}
                        </span>
                        <span class="den-crossbar__secondary">
                          {row.command.category || "Other"} · {row.command.provider}
                        </span>
                        <Show when={!contributionCommandEnabled(row.command)}>
                          <span class="den-crossbar__disabled-reason">
                            {commandDisabledReason(row.command)}
                          </span>
                        </Show>
                        <span class="den-crossbar__chord">
                          {displayBindingFor(row.command.id)}
                        </span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "recent") {
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-recent"
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <RecentQueryIcon />
                        </span>
                        <span class="den-crossbar__primary">{row.query}</span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "goto") {
                  const attention = () =>
                    row.target.kind === "session" ? row.target.attention : undefined;
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-goto"
                        data-goto-kind={row.target.kind}
                        data-attention={attention()?.class}
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <GotoTargetIcon kind={row.target.kind} />
                        </span>
                        <span class="den-crossbar__primary">
                          {row.target.label}
                        </span>
                        <Show when={row.target.context}>
                          {(ctx) => (
                            <span class="den-crossbar__secondary">{ctx()}</span>
                          )}
                        </Show>
                        {/* Attention status replaces the generic target kind. */}
                        <Show
                          when={attention()}
                          fallback={
                            <span class="den-crossbar__chip">
                              {CROSSBAR_GOTO_KIND_LABEL[row.target.kind]}
                            </span>
                          }
                        >
                          {(att) => (
                            <span
                              class="den-crossbar__chip den-attention-chip"
                              data-attention-class={att().class}
                            >
                              <span class="den-attention-chip__dot" aria-hidden="true" />
                              {ATTENTION_CLASS_LABEL[att().class]}
                            </span>
                          )}
                        </Show>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "inventory-file") {
                  const file = () => row.file;
                  const dirPrefix = () => {
                    const roots = props.projectRoots ?? [];
                    if (roots.length < 2) return "";
                    const label = roots.find((root) => root.id === file().rootId)?.label || file().rootId;
                    return file().dir ? `@${label}/` : `@${label}`;
                  };
                  const dir = () => dirPrefix() + file().dir || ".";
                  const dirIndexes = () => {
                    const offset = dirPrefix().length;
                    return file().matchIndexes.filter((index) => index < file().dir.length).map((index) => index + offset);
                  };
                  const baseIndexes = () => {
                    const offset = file().path.length - file().basename.length;
                    return file().matchIndexes.filter((index) => index >= offset).map((index) => index - offset);
                  };
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-inventory-file"
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <SearchKindIcon kind="file" />
                        </span>
                        <span class="den-crossbar__primary">
                          {renderMatchUnderline(file().basename, baseIndexes())}
                        </span>
                        <span class="den-crossbar__secondary">{renderMatchUnderline(dir(), dirIndexes())}</span>
                        <span class="den-crossbar__chip">File</span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "symbol") {
                  const sym = () => row.symbol;
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-symbol"
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <SearchKindIcon kind="code" />
                        </span>
                        <span class="den-crossbar__primary">
                          {renderMatchUnderline(sym().name, sym().matchIndexes ?? [])}
                        </span>
                        <span class="den-crossbar__secondary">:{sym().line}</span>
                        <span class="den-crossbar__chip">{sym().kind}</span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "line") {
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-line"
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <PaletteSlotIcon slot="goto-line" />
                        </span>
                        <span class="den-crossbar__primary">
                          {row.column != null ? `Go to line ${row.line}, column ${row.column}` : `Go to line ${row.line}`}
                        </span>
                        <span class="den-crossbar__secondary">{row.target.path}</span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "revision") {
                  const root = () => (props.projectRoots ?? []).length > 1
                    ? props.projectRoots?.find((r) => r.id === row.comparison.root_id)?.label
                    : undefined;
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-revision"
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <PaletteSlotIcon slot="diff" />
                        </span>
                        <span class="den-crossbar__primary">{row.comparison.label}</span>
                        <Show when={root()}>
                          {(label) => <span class="den-crossbar__secondary">@{label()}</span>}
                        </Show>
                        <span class="den-crossbar__chip">
                          {row.comparison.kind === "commit" ? "Commit" : row.comparison.kind === "branch" ? "Branch" : "Range"}
                        </span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "outside") {
                  const label = () => row.action === "reveal"
                    ? `Reveal in ${localPathDestinationLabel("file-manager")}`
                    : row.outside.kind === "directory"
                      ? `Open ${basenameOfPath(row.outside.path)} as a project`
                      : `Open ${basenameOfPath(outsideProjectFolder(row.outside))} as a project`;
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid={`crossbar-outside-${row.action}`}
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <PaletteSlotIcon slot={row.action === "reveal" ? "folder-open" : "open-folder"} />
                        </span>
                        <span class="den-crossbar__primary">{label()}</span>
                        <span class="den-crossbar__secondary">{row.outside.path}</span>
                      </button>
                    </li>
                  );
                }
                if (row.kind === "hit") {
                  const display = () => searchHitDisplay(row.hit);
                  const secondary = () =>
                    display().context ?? row.hit.project_name?.trim();
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-crossbar__row"
                        classList={{ "den-crossbar__row--active": active() }}
                        data-testid="crossbar-hit"
                        data-hit-kind={row.hit.hit_kind}
                        onMouseEnter={() => setActiveTo(selIdx())}
                        onClick={() => activateRow(row)}
                      >
                        <span class="den-crossbar__glyph" aria-hidden="true">
                          <SearchKindIcon kind={row.hit.hit_kind} />
                        </span>
                        <span class="den-crossbar__primary">
                          {renderMatchUnderline(display().title, display().titleMatches ?? [])}
                        </span>
                        <Show when={secondary()}>
                          {(ctx) => (
                            <span class="den-crossbar__secondary">{ctx()}</span>
                          )}
                        </Show>
                        <span class="den-crossbar__chip">
                          {display().kindLabel}
                        </span>
                      </button>
                    </li>
                  );
                }
                return null;
              }}
            </For>
            <Show when={rows().length === 0 && !showHitStatusRow()}>
              <li class="den-crossbar__empty" data-testid="crossbar-empty">
                {query().trim()
                  ? "No matching actions or results."
                  : "Type to search or run an action."}
              </li>
            </Show>
          </Scrollport>
          <span
            class="den-crossbar__list-edge den-crossbar__list-edge--top"
            aria-hidden="true"
          />
          <span
            class="den-crossbar__list-edge den-crossbar__list-edge--bottom"
            aria-hidden="true"
          />
          </div>

          {/* Keep escalation stationary while hits stream in. */}
          <Show when={escalateRow()}>
            {(row) => {
              const selIdx = () =>
                selectable().findIndex((r) => r.id === row().id);
              const active = () => selIdx() === activeIndex();
              return (
                <div class="den-crossbar__escalate-slot">
                  <button
                    type="button"
                    class="den-crossbar__row den-crossbar__row--escalate"
                    classList={{ "den-crossbar__row--active": active() }}
                    data-testid="crossbar-escalate"
                    onMouseEnter={() => setActiveTo(selIdx())}
                    onClick={() => activateRow(row())}
                  >
                    <span class="den-crossbar__glyph" aria-hidden="true">
                      <EscalateArrowIcon />
                    </span>
                    <span class="den-crossbar__primary">Show all results</span>
                    <span class="den-crossbar__chord">
                      {bindingForHandler("search.escalate")}
                    </span>
                  </button>
                </div>
              );
            }}
          </Show>
          </Show>

          <div class="den-crossbar__footer" data-testid="crossbar-footer">
            <span class="den-crossbar__hint">
              <kbd class="den-crossbar__key">↑</kbd>
              <kbd class="den-crossbar__key">↓</kbd>
              Navigate
            </span>
            <span class="den-crossbar__hint">
              <kbd class="den-crossbar__key">
                {bindingForHandler("list.confirm")}
              </kbd>
              Open
            </span>
            <span class="den-crossbar__hint">
              <kbd class="den-crossbar__key">
                {bindingForHandler("search.mode.next")}
              </kbd>
              Modes
            </span>
            <span class="den-crossbar__hint den-crossbar__hint--dismiss">
              <kbd class="den-crossbar__key">
                {bindingForHandler("overlay.dismiss")}
              </kbd>
              Close
            </span>
          </div>
          </Show>
        </div>
      </div>
    </Show>
  );
}

/** Fits complete rows without leaving a section header last. */
function wholeRowHeight(list: HTMLElement, available: number): number | null {
  const listTop = list.getBoundingClientRect().top - list.scrollTop;
  const paddingBottom = parseFloat(getComputedStyle(list).paddingBottom) || 0;
  let fit: number | null = null;
  let previous: number | null = null;
  for (const child of list.children) {
    // Only rows bound the fit.
    if (!(child instanceof HTMLLIElement)) continue;
    const bottom = child.getBoundingClientRect().bottom - listTop + paddingBottom;
    if (bottom > available) break;
    const isHeader = child.classList.contains("den-crossbar__section");
    fit = isHeader ? previous : bottom;
    previous = bottom;
  }
  return fit;
}

/** A directory opens as itself; a file opens its containing folder. */
function outsideProjectFolder(outside: SourceSearchOutside): string {
  if (outside.kind === "directory") return outside.path;
  const cut = Math.max(outside.path.lastIndexOf("/"), outside.path.lastIndexOf("\\"));
  return cut > 0 ? outside.path.slice(0, cut) : outside.path;
}
