import { registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { createFilesTreeSource } from "./files-tree-source.ts";
import { createFilesTreeScrolling, type FilesTreeScrollRestoration } from "./files-tree-scrolling.ts";
import { createFilesTreeEditing } from "./files-tree-editing.ts";
import { createFilesTreeDrag } from "./files-tree-drag.ts";
import { createFilesTreeKeyboard } from "./files-tree-keyboard.ts";
import { editorTreeVisibleLevelsPref } from "../../settings/editor/editor-prefs.ts";

import { FilesTreeViewport, treeViewportAnchor, treeViewportLeadingRows } from "./files-tree-viewport.ts";
import type { SourceTreeFrame } from "../../api/types.ts";
import { type FilesTreeSession } from "./files-tree-paged-session.ts";
import { buildKnownRootsModel, buildPagedTreeModel, treeFramesCover } from "./files-tree-paged-model.ts";
import {
  prepareFilesTreeNavigation,
  type TreeNavigationViewport,
  type TreePresentation,
} from "./files-tree-navigation.ts";

import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";

import { batch, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from "solid-js";
import type { SourceChange } from "../../api/types.ts";
import { scrollportMotionForHost } from "../../platform/scrolling/scrollport-motion.ts";
import { type ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import type { FilesContextTarget } from "../commands/project-files-context-menu.ts";
import { FILES_TREE_ROW_HEIGHT_PX, type FlatTreeRow } from "./project-files-tree-flat.ts";
import type { DisplayRow } from "./files-tree-display-model.ts";
import { splitFilesTreeDirKey } from "./files-tree-keys.ts";
import { type RenderedRow, fixedVirtualRows, renderWindowRows, scrollTopToAlignRowAtStart, scrollTopToRevealRow } from "./files-tree-virtual-scroll.ts";
import { stickyStackFromIndex, treeLayoutRange } from "./files-tree-sticky.ts";
import { claimFilesTreeNavigation } from "./files-tree-view-state.ts";

import { useResidentPresence } from "../../ui/resident-presence-context.tsx";

import { type TreeOps, type FilesTreeProps, type FilesTreeSelection, type FilesTreeFolderScope } from "./files-tree-context.ts";
import { FilesTreeSurface } from "./FilesTreeSurface.tsx";

function displayRowIdentity(row: DisplayRow): string {
  return `${row.key}:${row.kind}:${row.kind === "entry" ? row.entry.isDir : ""}`;
}

export function FilesTree(props: FilesTreeProps) {
  let navEl: HTMLElement | undefined;
  const keyboardBinding: { current?: ReturnType<typeof createFilesTreeKeyboard> } = {};
  let filterEl: HTMLInputElement | undefined;
  let scrollEl: HTMLElement | undefined;
  const [scrollEpoch, setScrollEpoch] = createSignal(0);

  const {
    dirStates,
    setDirStates,
    renameError,
    renamingBusy,
    patchDir,
    dirState,
    startNaming,
    cancelNaming,
    submitName,
    submitRename,
    cancelRename,
    observeRename,
  } = createFilesTreeEditing({
    displayModel: () => displayModel(),
    disclose: (rootId, dir, open) => disclose(rootId, dir, open),
    createEntry: (kind, rootId, path) => props.createEntry(kind, rootId, path),
    confirmSourceChange: (change) => confirmSourceChange(change),
    openFile: (selection, intent) => openFile(selection, intent),
    renaming: () => props.renaming,
    commitRename: () => props.onCommitRename,
    cancelRename: () => props.onCancelRename,
  });

  const {
    rootLabel,
    dragSource,
    dropTarget,
    movePending,
    dragGhost,
    dropAssessment,
    onRowPointerDown,
    cancelDrag,
    clearHoverExpandTimer,
  } = createFilesTreeDrag({
    projectId: () => props.projectId,
    roots: () => props.roots,
    navEl: () => navEl,
    movePath: () => props.onMovePath,
    dirState,
    expandDir: (rootId, dir) => expandDir(rootId, dir),
    reportTreeError: (error) => reportTreeError(error),
  });
  const [rememberedTreeFocus, setRememberedTreeFocus] = createSignal<{
    rootId: string;
    path: string;
  } | null>(null);
  const [stickyHostEl, setStickyHostEl] = createSignal<HTMLElement | undefined>(
    undefined,
  );
  const residentPresence = useResidentPresence();
  const surfaceLive = () =>
    props.visible !== false && residentPresence() !== "idle";
  const viewStateKey = createMemo(
    () => props.workspaceId.trim() || props.projectId,
  );
  const navigationAim = createMemo<{ workspace: string; revision?: number; fresh: boolean }>((previous) => {
    const workspace = props.workspaceId;
    const revision = props.navigationRevision;
    if (previous?.workspace === workspace && previous.revision === revision) return previous;
    const fresh = Boolean(workspace && revision != null && claimFilesTreeNavigation(workspace, revision));
    return { workspace, revision, fresh };
  });
  const hasNewReaderNavigation = () => navigationAim().fresh &&
    props.navigationOrigin === "reader" && props.autoReveal !== false;
  const restoration: FilesTreeScrollRestoration = { activeReveal: null, pendingScroll: undefined };
  const scrolling = createFilesTreeScrolling({
    navElement: () => navEl, scrollElement: () => scrollEl, setScrollElement: (element) => { scrollEl = element; },
    scrollEpoch, setScrollEpoch, restoration,
  });
  const { virtualScrollTop, setVirtualScrollTop, virtualViewportHeight, geometry,
    navigatingViewport, setNavigatingViewport, paintOffset, scrollActive, scrollbarTarget, geometryVersion,
    cancelScrollbarNavigation, resolveScrollEl, currentScrollTop, currentViewportHeight } = scrolling;
  const [revealViewport, setRevealViewport] = createSignal<{ claimant: object; workspace: string; viewport: TreeNavigationViewport }>();
  const preparingNavigation = () => navigatingViewport() || !!scrollbarTarget() || !!revealViewport();
  const treeSource = createFilesTreeSource(props, {
    navElement: () => navEl, surfaceLive, restoration,
    virtualViewportHeight, virtualScrollTop, setVirtualScrollTop, setDirStates,
    presentedTree: () => presentedTree(), navigationAim, hasNewReaderNavigation,
    cancelFocusNavigation: () => keyboardBinding.current?.cancelNavigation(),
    revealTreePath: (...args) => revealTreePath(...args),
  });
  const { sourceView, viewVersion, state,
    cancelTreeReveal, reportTreeError, runCommand, scrollToPath } = treeSource;
  const expandAll = (scope?: FilesTreeFolderScope) => {
    cancelTreeReveal();
    const view = sourceView(); if (!view) return;
    const addresses = scope ? [{ root_id: scope.rootId, path: scope.dir }] : props.roots.map(root => ({ root_id: root.id, path: "." }));
    if (addresses.length) void view.expand(addresses, presentedTree().presentation?.id).catch(reportTreeError);
  };
  const disclose = (rootId: string, dir: string, open: boolean, recursive = false) =>
    runCommand({ kind: "disclose", disclosures: [{ address: { root_id: rootId, path: dir }, open, recursive }] });
  const confirmSourceChange = (_change: SourceChange) => { sourceView()?.invalidate(); };
  const toggleDir = (rootId: string, dir: string, opts?: { recursiveCollapse?: boolean }) =>
    runCommand({ kind: "toggle", address: { root_id: rootId, path: dir }, collapse_descendants: opts?.recursiveCollapse });
  const selectFolder = async (rootId: string, dir: string, forceOpen = false) => {
    beginSourceNavigation(); treeSource.advanceNavigation();
    const selected = props.selectedEntry?.kind === "folder" && props.selectedEntry.rootId === rootId && props.selectedEntry.path === dir;
    props.onSelectEntry({ rootId, path: dir, kind: "folder" });
    if (forceOpen || !selected) await disclose(rootId, dir, true);
    else await toggleDir(rootId, dir);
  };
  const collapseAll = (scope?: FilesTreeFolderScope) => {
    const targets = scope ? [scope] : props.roots.map(root => ({ rootId: root.id, dir: "." }));
    const disclosures = targets.flatMap(target => {
      const address = { root_id: target.rootId, path: target.dir };
      return target.dir === "."
        ? [{ address, open: false, recursive: true }, { address, open: true, recursive: false }]
        : [{ address, open: false, recursive: true }];
    });
    if (disclosures.length) void runCommand({ kind: "disclose", disclosures }).catch(reportTreeError);
  };

  const openFile = (
    selection: FilesTreeSelection,
    intent: "transient" | "permanent",
  ) => {
    props.onSelectEntry({
      rootId: selection.rootId,
      path: selection.path,
      kind: "file",
    });
    props.onOpenFile(selection, intent);
  };

  const expandDir = async (rootId: string, dir: string) => {
    if (dirState(rootId, dir).expanded) return;
    await disclose(rootId, dir, true);
  };

  treeSource.observeSession();
  treeSource.observeFailure();
  treeSource.observeReveal();
  const deferredFilterQuery = treeSource.observeFilter();
  const [positionedFrame, setPositionedFrame] = createSignal<SourceTreeFrame>();
  const presentedTree = createMemo<{ workspace: string; presentation: ReturnType<FilesTreeSession["presentation"]> | undefined }>(previous => {
    const workspace = props.workspaceId;
    const reveal = revealViewport();
    if (reveal?.workspace === workspace) return { workspace, presentation: reveal.viewport.presentation };
    if (props.retainPresentation && previous?.presentation?.frames.length) return previous;
    if ((!!scrollbarTarget() || paintOffset() !== 0) && previous?.workspace === workspace && previous?.presentation?.frames.length) return previous;
    viewVersion();
    const presentation = sourceView()?.presentation();
    const retained = previous?.workspace === workspace ? previous.presentation : undefined;
    if (!retained?.frames.length && presentation?.frames.length && presentation.state) {
      const height = untrack(virtualViewportHeight);
      const offset = Math.min(untrack(virtualScrollTop), Math.max(0, presentation.state.extent.rows * FILES_TREE_ROW_HEIGHT_PX - height));
      const context = treeLayoutRange(Math.floor(offset / FILES_TREE_ROW_HEIGHT_PX),
        Math.ceil((offset + height) / FILES_TREE_ROW_HEIGHT_PX), presentation.state.extent.rows);
      if (!treeFramesCover(presentation.frames, context.start, context.end)) return { workspace, presentation: retained };
    }
    const viewport = retained?.state && presentation?.state ? scrollEl ?? resolveScrollEl() : undefined;
    if (retained?.frames.length && presentation?.frames.length && !untrack(preparingNavigation)) {
      if (viewport) {
        const offset = untrack(virtualScrollTop);
        const start = Math.floor(offset / FILES_TREE_ROW_HEIGHT_PX);
        const end = Math.min(presentation.state?.extent.rows ?? 0, Math.ceil((offset + currentViewportHeight(viewport)) / FILES_TREE_ROW_HEIGHT_PX));
        const context = treeLayoutRange(start, end, presentation.state?.extent.rows ?? 0);
        const covers = treeFramesCover(presentation.frames, context.start, context.end);
        const anchor = retained.frames.flatMap(frame => frame.span.start <= start && start < frame.span.end ? [frame.rows[start - frame.span.start]] : []).find(Boolean)?.address;
        const nextAnchor = presentation.frames.flatMap(frame => frame.span.start <= start && start < frame.span.end ? [frame.rows[start - frame.span.start]] : []).find(Boolean)?.address;
        const displaced = anchor && (anchor.root_id !== nextAnchor?.root_id || anchor.path !== nextAnchor?.path);
        const positioned = positionedFrame();
        const changedCoordinates = retained.state?.id !== presentation.state?.id ||
          retained.state?.projection_revision !== presentation.state?.projection_revision ||
          retained.state?.intent_revision !== presentation.state?.intent_revision;
        const positionedSuccessor = changedCoordinates && positioned && presentation.frames.includes(positioned);
        if (previous && (!covers || displaced) && !positionedSuccessor) return previous;
      }
    }
    return previous?.workspace === workspace && previous.presentation === presentation ? previous : { workspace, presentation };
  });
  createEffect(() => {
    const view = sourceView();
    const id = presentedTree().presentation?.id;
    if (view && id && view.state()?.id === presentedTree().presentation?.state?.id) onCleanup(view.retainPresentation(id));
  });
  const modelForPresentation = (presentation: TreePresentation | undefined) => {
    if (!presentation?.frames.length && presentation?.state?.extent.rows !== 0) return buildKnownRootsModel(props.roots);
    return buildPagedTreeModel(props.roots, presentation?.state?.extent.rows ?? 0, presentation?.frames ?? [],
      Object.entries(dirStates()).flatMap(([key, draft]) => {
        const address = splitFilesTreeDirKey(key);
        return address && (draft.naming || draft.createError) ? [{ ...address, naming: draft.naming, createError: draft.createError }] : [];
      }));
  };
  const displayModel = createMemo(() => modelForPresentation(presentedTree().presentation));
  const preferredTreeFocus = createMemo(() => {
    const model = displayModel();
    const candidates = [
      rememberedTreeFocus(),
      props.selectedEntry,
      props.activeFile,
    ];
    for (const candidate of candidates) {
      if (
        candidate &&
        model.indexOfEntry(candidate.rootId, candidate.path) >= 0
      ) {
        return candidate;
      }
    }
    const first = model.rowAt(model.firstEntryIndex());
    return first?.kind === "entry"
      ? { rootId: first.entry.rootId, path: first.entry.path }
      : null;
  });

  const showFilterEmpty = createMemo(() => Boolean(deferredFilterQuery().trim()) && state()?.state === "ready" && displayModel().length === 0);

  const { virtualWindow, stickySlots, stickyTops, stickyHeight, stickyFold, stickyKeys, forwardStickyWheel, layoutViewport } = scrolling.bind({
    displayModel, state, surfaceLive, sourceView, presentedTree, reportTreeError,
    setStickyHostEl, revealViewport, navigatingViewport, cancelTreeReveal,
    cancelRebase: () => cancelRebase(), viewportReader: () => viewportReader(),
    setGestureVersion: (update) => setGestureVersion(update), viewStateKey, dirStates,
  });
  const treeFocusClaim = {};
  const bindNavEl = (el: HTMLElement | undefined) => {
    navEl = el;
    if (el) {
      el.tabIndex = -1;
      el.addEventListener("focus", () => el.querySelector<HTMLElement>('.den-files-tree__label[tabindex="0"]')?.focus());
      el.addEventListener("focusout", event => {
        if (!(event.relatedTarget instanceof Node) || !el.contains(event.relatedTarget)) {
          keyboardBinding.current?.clearTypeahead();
        }
      });
      registerFocusRegion("filesTree", el, treeFocusClaim);
    }
  };
  onCleanup(() => releaseFocusRegion("filesTree", treeFocusClaim));

  const virtualRows = createMemo(() =>
    fixedVirtualRows(virtualWindow(), FILES_TREE_ROW_HEIGHT_PX),
  );
  // A position whose page is missing keeps its last row while input or a destination is live.
  const renderedRows = createMemo<Map<string, RenderedRow<DisplayRow>>>(previous => {
    const model = displayModel();
    return renderWindowRows(virtualRows(), index => model.rowAt(index), displayRowIdentity, previous,
      scrollActive() || preparingNavigation());
  }, new Map());

  const treeTabStop = createMemo(() => {
    const preferred = preferredTreeFocus();
    const model = displayModel();
    const rendered = virtualRows();
    if (
      preferred &&
      rendered.some((item) => {
        const row = model.rowAt(item.index);
        return row?.kind === "entry" &&
          row.entry.rootId === preferred.rootId &&
          row.entry.path === preferred.path;
      })
    ) {
      return preferred;
    }
    for (const item of rendered) {
      const row = model.rowAt(item.index);
      if (row?.kind === "entry") {
        return { rootId: row.entry.rootId, path: row.entry.path };
      }
    }
    return null;
  });

  const isTreeTabStop = (row: FlatTreeRow) => {
    const focus = treeTabStop();
    return focus?.rootId === row.rootId && focus.path === row.path;
  };

  const rememberTreeFocus = (row: FlatTreeRow) => {
    setRememberedTreeFocus({ rootId: row.rootId, path: row.path });
  };

  const [viewportReader, setViewportReader] = createSignal<FilesTreeViewport>();
  let pendingRebase: { key: string; intent: string; start: number; end: number; abort: AbortController } | undefined;
  const cancelRebase = () => { pendingRebase?.abort.abort(); pendingRebase = undefined; };
  onCleanup(cancelRebase);
  createEffect(() => { if (!surfaceLive()) cancelRebase(); });
  const projectionIdentity = createMemo(() => { const current = state(); return current ? `${current.id}:${current.projection_revision}` : ""; });
  const presentedIdentity = createMemo(() => {
    const presented = presentedTree().presentation?.state;
    return presented ? `${presented.id}:${presented.projection_revision}` : "";
  });
  /** Settled input permits a pending presentation change. */
  const [gestureVersion, setGestureVersion] = createSignal(0);

  // Publish complete coordinates between direct scroll gestures.
  createEffect(() => {
    const view = sourceView();
    projectionIdentity();
    virtualWindow();
    gestureVersion();
    const previous = presentedTree().presentation;
    const current = untrack(state);
    if (!view || !current || !surfaceLive() || preparingNavigation()) return;
    if (!previous?.state || previous.state.id !== current.id) return;
    const viewport = scrollEl ?? resolveScrollEl();
    if (!viewport) return;
    // Cached offsets avoid forcing layout after row updates.
    const startingOffset = currentScrollTop(viewport);
    const logicalOffset = geometry.logicalOffset(startingOffset);
    const headDiffers = previous.state.projection_revision !== current.projection_revision || previous.state.intent_revision !== current.intent_revision;
    if (!headDiffers || current.state !== "ready") return;
    const host = navEl?.closest<HTMLElement>(".den-files-tree-scroll-frame");
    const motion = host ? scrollportMotionForHost(host) : undefined;
    if (previous.state.intent_revision === current.intent_revision && motion?.input.isDirectInputActive()) return;
    const intent = `${current.id}:${current.intent_revision}`;
    const key = `${intent}:${current.projection_revision}:${logicalOffset}`;
    if (pendingRebase?.intent === intent && (pendingRebase.key === key ||
      (logicalOffset >= pendingRebase.start && logicalOffset + virtualViewportHeight() <= pendingRebase.end))) return;
    const abort = new AbortController();
    const anchor = untrack(() => {
      if (props.retainPresentation || restoration.pendingScroll != null) return null;
      const model = displayModel();
      const start = Math.floor(logicalOffset / FILES_TREE_ROW_HEIGHT_PX);
      const anchor = treeViewportAnchor(model, start);
      return anchor;
    });
    if (!anchor) { cancelRebase(); return; }
    cancelRebase();
    const leading = treeViewportLeadingRows(anchor.before, Math.ceil(virtualViewportHeight() / FILES_TREE_ROW_HEIGHT_PX));
    const start = Math.max(0, Math.floor(logicalOffset / FILES_TREE_ROW_HEIGHT_PX) - leading) * FILES_TREE_ROW_HEIGHT_PX;
    pendingRebase = { key, intent, start, end: start + 200 * FILES_TREE_ROW_HEIGHT_PX, abort };
    const navigation = treeSource.navigationGeneration();
    void view.viewport(anchor, Math.ceil(virtualViewportHeight() / FILES_TREE_ROW_HEIGHT_PX), abort.signal).then(({ frame, start }) => {
      // Superseded frames cannot reposition the viewport.
      if (abort.signal.aborted || sourceView() !== view || !surfaceLive() || navigation !== treeSource.navigationGeneration() || !view.presentation().frames.includes(frame)) return;
      const el = scrollEl ?? resolveScrollEl();
      const host = navEl?.closest<HTMLElement>(".den-files-tree-scroll-frame");
      const motion = host ? scrollportMotionForHost(host) : undefined;
      if (!el || !motion || el !== viewport) return;
      const currentOffset = geometry.logicalOffset(currentScrollTop(el));
      const delta = Math.floor(currentOffset / FILES_TREE_ROW_HEIGHT_PX) - Math.floor(logicalOffset / FILES_TREE_ROW_HEIGHT_PX);
      const destination = start + delta;
      const visible = Math.ceil(virtualViewportHeight() / FILES_TREE_ROW_HEIGHT_PX);
      const context = treeLayoutRange(destination, destination + visible, frame.extent.rows);
      if (destination < 0 || !treeFramesCover(view.presentation().frames, context.start, context.end)) return;
      const physical = batch(() => {
        setPositionedFrame(frame);
        const model = displayModel();
        return layoutViewport(model.displayIndex(destination) * FILES_TREE_ROW_HEIGHT_PX + currentOffset % FILES_TREE_ROW_HEIGHT_PX);
      });
      // The extent reaches the DOM before scroll clamping.
      motion.commit(physical, "restore_anchor");
    }).catch(error => {
      if ((error as { code?: string }).code === "source_view_revision_changed") void view.refresh().catch(reportTreeError);
      else reportTreeError(error);
    }).finally(() => { if (pendingRebase?.abort === abort) pendingRebase = undefined; });
  });

  createEffect(() => {
    const view = sourceView();
    if (!view || !surfaceLive()) { setViewportReader(undefined); return; }
    const reader = new FilesTreeViewport(view);
    setViewportReader(reader);
    onCleanup(() => reader.clear());
  });
  // A painting from a replaced view only bridges the gap; reads follow the view that replaced it.
  const readerPresentation = createMemo(() => {
    const presented = presentedTree().presentation?.state;
    const current = state();
    return presented && current && presented.id !== current.id ? undefined : presented;
  });
  // Read-ahead fills the presented coordinates; its window resets only when they change.
  createEffect(() => {
    const reader = viewportReader();
    const window = virtualWindow();
    const presented = readerPresentation();
    const revision = presented ? presentedIdentity() : projectionIdentity();
    if (!reader || !revision || preparingNavigation()) return;
    const model = untrack(displayModel);
    const start = model.sourceIndex(Math.min(window?.startIndex ?? 0, model.length));
    const end = model.sourceIndex(Math.min(window ? window.endIndex + 1 : Math.min(model.length, 200), model.length));
    const extent = untrack(() => (presented ?? state())?.extent.rows ?? 0);
    reader.seek(start, Math.max(start + 1, end), extent, revision);
  });

  const scrollToDisplayIndex = (
    index: number,
    align: "nearest" | "start" = "nearest",
    complete?: () => void,
  ): boolean => {
    const el = resolveScrollEl();
    if (!el) return false;
    const H = FILES_TREE_ROW_HEIGHT_PX;
    const sticky = displayModel();
    const viewportH = virtualViewportHeight();
    const stickyHeightAt = (scrollTop: number) =>
      stickyStackFromIndex(sticky, scrollTop, viewportH, editorTreeVisibleLevelsPref()).height;
    const contentHeight = displayModel().length * H;
    const top =
      align === "start"
        ? scrollTopToAlignRowAtStart({
            index,
            rowHeight: H,
            viewportHeight: viewportH,
            contentHeight,
            stickyHeightAt,
          })
        : scrollTopToRevealRow({
            index,
            rowHeight: H,
            scrollTop: untrack(virtualScrollTop),
            viewportHeight: viewportH,
            contentHeight,
            stickyHeightAt,
          });
    const host = navEl?.closest<HTMLElement>(".den-files-tree-scroll-frame");
    const motion = host ? scrollportMotionForHost(host) : undefined;
    if (top == null) { motion?.cancelReveal(); complete?.(); return true; }
    if (!motion) return false;
    const destination = layoutViewport(top);
    motion.commit(destination, "reveal");
    complete?.();
    return true;
  };

  const revealTreePath = async (rootId: string, path: string, isDir: boolean,
    align: "nearest" | "start", signal: AbortSignal, follow = true) => {
    const view = sourceView();
    const viewport = scrollEl ?? resolveScrollEl();
    const host = navEl?.closest<HTMLElement>(".den-files-tree-scroll-frame");
    const motion = host ? scrollportMotionForHost(host) : undefined;
    if (!view || !viewport || !motion) return;
    motion.cancelReveal(); cancelScrollbarNavigation(); cancelRebase(); viewportReader()?.clear();
    const claimant = {};
    const workspace = props.workspaceId;
    const previous = { presentation: presentedTree().presentation ?? view.presentation(),
      offset: !follow && restoration.pendingScroll != null ? restoration.pendingScroll : untrack(virtualScrollTop) };
    setRevealViewport({ claimant, workspace, viewport: previous });
    const cancel = () => { if (revealViewport()?.claimant === claimant) motion.cancelReveal(); };
    signal.addEventListener("abort", cancel, { once: true });
    const publish = (prepared: TreeNavigationViewport) => {
      signal.throwIfAborted();
      if (revealViewport()?.claimant !== claimant || sourceView() !== view || !surfaceLive()) {
        throw new DOMException("The tree reveal was superseded.", "AbortError");
      }
      const physical = batch(() => {
        setRevealViewport({ claimant, workspace, viewport: prepared });
        return layoutViewport(prepared.offset);
      });
      motion.commit(physical, "reveal");
    };
    try {
      const { navigation, target } = await prepareFilesTreeNavigation({ view, model: modelForPresentation,
        height: virtualViewportHeight, address: { root_id: rootId, path }, isDir, align, previous, signal });
      try {
        publish(navigation.viewport());
        restoration.pendingScroll = undefined; restoration.activeReveal = null;
        props.onRevealMotionStart?.();
        if (follow && !await motion.revealOffset(target, { signal, coordinates: {
          read: () => untrack(virtualScrollTop),
          prepare: (offset, signal) => navigation.prepare(offset, signal),
          publish: () => publish(navigation.viewport()),
        } })) return false;
        signal.throwIfAborted();
        return revealViewport()?.claimant === claimant;
      } finally { navigation.dispose(); }
    } finally {
      signal.removeEventListener("abort", cancel);
      if (revealViewport()?.claimant === claimant) setRevealViewport(undefined);
    }
  };

  const openRowMenuAt = (
    anchor: ContextMenuAnchor,
    target: FilesContextTarget,
  ) => {
    props.onRowMenu?.(anchor, target);
  };

  const openRowMenu = (e: MouseEvent, target: FilesContextTarget) => {
    e.preventDefault();
    e.stopPropagation();
    openRowMenuAt({ x: e.clientX, y: e.clientY }, target);
  };

  observeRename();

  onMount(() => {
    props.onReady?.({
      startCreate: (kind, rootId, dir) => void startNaming(rootId, dir, kind).catch(reportTreeError),
      confirmChange: confirmSourceChange,
      revealPath: scrollToPath,
      collapseAll,
      expandAll,
    });
  });

  createEffect(() => {
    if (props.filterOpen) filterEl?.focus();
  });

  onCleanup(() => {
    clearHoverExpandTimer();
    cancelDrag();
  });

  const keyboard = createFilesTreeKeyboard({
    navElement: () => navEl, roots: () => props.roots, surfaceLive,
    displayModel, sourceView, virtualViewportHeight,
    advanceNavigation: () => treeSource.advanceNavigation(),
    navigationGeneration: () => treeSource.navigationGeneration(),
    cancelTreeReveal, cancelScrollbarNavigation, setNavigatingViewport, cancelRebase,
    clearViewport: () => viewportReader()?.clear(), scrollToDisplayIndex, reportTreeError,
    filterOpen: () => props.filterOpen ?? false,
    clearFilter: () => { props.onFilterQueryChange?.(""); props.onFilterClose?.(); },
    openRowMenuAt,
  });
  const activateStickyFolder = (
    displayIndex: number,
    rootId: string,
    dir: string,
  ) => {
    void selectFolder(rootId, dir).catch(reportTreeError);
    void keyboard?.focusEntryByDisplayIndex(displayIndex).catch(reportTreeError);
  };

  const ops: TreeOps = {
    fold: stickyFold,
    props,
    dirState,
    toggleDir: (...args) => toggleDir(...args).catch(reportTreeError),
    selectFolder: (...args) => selectFolder(...args).catch(reportTreeError),
    openFile,
    startNaming: (...args) => startNaming(...args).catch(reportTreeError),
    cancelNaming,
    setNamingDraft: (rootId, dir, draft) =>
      patchDir(rootId, dir, { namingDraft: draft }),
    submitName,
    onRowPointerDown,
    dragSource,
    dropTarget,
    dropAssessment,
    movePending,
    openRowMenu,
    isTreeTabStop,
    rememberTreeFocus,
    submitRename,
    renameError,
    renamingBusy,
    cancelRename,
    activateStickyFolder,
  };

  keyboardBinding.current = keyboard;
  return (
    <FilesTreeSurface
      ops={ops}
      bindNavEl={bindNavEl}
      bindFilterEl={element => { filterEl = element; }}
      onKeyDown={keyboard.onKeyDown}
      forwardStickyWheel={forwardStickyWheel}
      stickyHost={stickyHostEl}
      stickyHeight={stickyHeight}
      stickyKeys={stickyKeys}
      stickySlots={stickySlots}
      stickyTops={stickyTops}
      stickyFold={stickyFold}
      physicalRowOffset={offset => (geometryVersion(), geometry.physicalOffset(offset))}
      extentHeight={() => geometry.extent(displayModel().length)}
      paintOffset={paintOffset}
      renderedRows={renderedRows}
      showFilterEmpty={showFilterEmpty}
      dragGhost={dragGhost}
      dropAssessment={dropAssessment}
      movePending={movePending}
      rootLabel={rootLabel}
    />
  );
}
