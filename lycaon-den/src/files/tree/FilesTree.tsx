import { focusRegion, registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { createFilesTreeSource } from "./files-tree-source.ts";
import { createFilesTreeScrolling, type FilesTreeScrollRestoration } from "./files-tree-scrolling.ts";
import { createFilesTreeEditing } from "./files-tree-editing.ts";
import { createFilesTreeDrag, parentDir } from "./files-tree-drag.ts";
import { findTreePrefix } from "./tree-typeahead.ts";
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

import { For, Show, batch, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from "solid-js";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import type { SourceChange } from "../../api/types.ts";
import { scrollportMotionForHost } from "../../platform/scrolling/scrollport-motion.ts";
import { type ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import type { FilesContextTarget } from "../commands/project-files-context-menu.ts";
import { FILES_TREE_ROW_HEIGHT_PX, type FlatTreeRow } from "./project-files-tree-flat.ts";
import type { DisplayRow } from "./files-tree-display-model.ts";
import { splitFilesTreeDirKey } from "./files-tree-keys.ts";
import { type RenderedRow, fixedVirtualRows, renderWindowRows, scrollTopToAlignRowAtStart, scrollTopToRevealRow } from "./files-tree-virtual-scroll.ts";
import { stickyStackFromIndex, treeLayoutRange } from "./files-tree-sticky.ts";
import { FilesTreeStickyOverlay } from "./FilesTreeStickyOverlay.tsx";
import { claimFilesTreeNavigation } from "./files-tree-view-state.ts";

import { useResidentPresence } from "../../ui/resident-presence-context.tsx";

import { TreeContext, type TreeOps, type FilesTreeProps, type FilesTreeSelection, type FilesTreeFolderScope } from "./files-tree-context.ts";
import { StickySlotBox, SwitchDisplayRow } from "./FilesTreeRows.tsx";

function displayRowIdentity(row: DisplayRow): string {
  return `${row.key}:${row.kind}:${row.kind === "entry" ? row.entry.isDir : ""}`;
}

export function FilesTree(props: FilesTreeProps) {
  let navEl: HTMLElement | undefined;
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
    cancelFocusNavigation: () => focusNavigation?.abort(),
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
          typeaheadRequest?.abort();
          typeahead = "";
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
    if (previous.state.intent_revision === current.intent_revision && motion?.isDirectInputActive()) return;
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

  let focusNavigation: AbortController | undefined;
  onCleanup(() => focusNavigation?.abort());
  createEffect(() => { if (!surfaceLive()) focusNavigation?.abort(); });
  const focusEntryByDisplayIndex = async (
    displayIndex: number,
    align: "nearest" | "start" = "nearest",
    end = false,
  ) => {
    // Capture the navigation root before the scroll completes.
    const nav = navEl;
    if (displayIndex < 0 || !nav) return;
    const view = sourceView();
    if (!view) return;
    cancelTreeReveal();
    cancelScrollbarNavigation();
    const abort = new AbortController(); focusNavigation = abort;
    const navigation = treeSource.advanceNavigation();
    setNavigatingViewport(true);
    cancelRebase();
    viewportReader()?.clear();
    try {
    let row = displayModel().rowAt(displayIndex);
    if (end) {
      const root = props.roots.at(-1);
      if (!root) return;
      const frame = await view.frameAt({ root_id: root.id, path: "." }, abort.signal, 199, Number.MAX_SAFE_INTEGER);
      displayIndex = displayModel().displayIndex(frame.span.end - 1);
    } else if (row?.kind !== "entry") {
      const at = displayModel().sourceIndex(displayIndex);
      await view.range(at, at + 1, abort.signal);
    }
    if (abort.signal.aborted || navigation !== treeSource.navigationGeneration() || view !== sourceView() || nav !== navEl || !surfaceLive()) return;
    row = displayModel().rowAt(displayIndex);
    if (row?.kind !== "entry") {
      displayIndex = displayModel().nextEntryIndex(displayIndex, displayIndex === displayModel().length - 1 ? -1 : 1);
      row = displayModel().rowAt(displayIndex);
    }
    if (!row || row.kind !== "entry") return;
    const entry = row.entry;
    const visibleRows = Math.min(99, Math.ceil(virtualViewportHeight() / FILES_TREE_ROW_HEIGHT_PX) + 8);
    const before = align === "start" ? 0 : visibleRows;
    const model = displayModel();
    const first = Math.max(0, displayIndex - before);
    const last = Math.min(model.length, displayIndex + visibleRows + 1);
    const context = treeLayoutRange(first, last, model.length);
    let missing = false;
    for (let index = context.start; index < context.end; index++) {
      if (!model.rowAt(index)) { missing = true; break; }
    }
    if (missing) {
      // Destination context uses the displayed coordinates.
      await view.range(model.sourceIndex(first), model.sourceIndex(last), abort.signal);
      if (abort.signal.aborted || navigation !== treeSource.navigationGeneration() || view !== sourceView() || nav !== navEl || !surfaceLive()) return;
      displayIndex = displayModel().indexOfEntry(entry.rootId, entry.path);
      if (displayIndex < 0) return;
    }
    const selector = row.entry.isDir ? ".den-files-tree__label--dir" : ".den-files-tree__label--file";
    const focus = () => queueMicrotask(() => {
      const button = [...nav.querySelectorAll<HTMLButtonElement>(selector)].find(
        (candidate) => candidate.dataset.root === entry.rootId && candidate.dataset.path === entry.path,
      );
      button?.focus({ preventScroll: true });
    });
    if (!scrollToDisplayIndex(displayIndex, align, focus)) focus();
    } finally { if (focusNavigation === abort) setNavigatingViewport(false); }
  };

  const focusEntryOffset = (target: HTMLButtonElement, delta: number) => {
    const rootId = target.dataset.root ?? "";
    const path = target.dataset.path ?? ".";
    const model = displayModel();
    const displayIndex = model.indexOfEntry(rootId, path);
    if (displayIndex < 0) return;
    const next = model.nextEntryIndex(displayIndex, delta < 0 ? -1 : 1);
    if (next < 0) return;
    void focusEntryByDisplayIndex(next).catch(reportTreeError);
  };

  let typeahead = "";
  let typedAt = 0;
  let typeaheadRequest: AbortController | undefined;
  onCleanup(() => typeaheadRequest?.abort());
  const onKeyDown = (e: KeyboardEvent) => {
    if (props.filterOpen && e.key === "Escape") {
      props.onFilterQueryChange?.("");
      props.onFilterClose?.();
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    if (!navEl) return;
    const target =
      e.target instanceof HTMLElement
        ? e.target.closest<HTMLButtonElement>(".den-files-tree__label")
        : null;
    if (!target) return;
    const isDir = target.classList.contains("den-files-tree__label--dir");
    const expanded = target.getAttribute("aria-expanded") === "true";
    const rootId = target.dataset.root ?? "";
    const path = target.dataset.path ?? ".";
    const model = displayModel();
    typeaheadRequest?.abort();
    if (e.key.length === 1 && e.key !== " " && !e.ctrlKey && !e.metaKey && !e.altKey && !e.isComposing) {
      e.preventDefault();
      const now = performance.now();
      typeahead = now - typedAt < 750 ? typeahead + e.key : e.key;
      typedAt = now;
      let repeated = true;
      for (const key of typeahead) {
        if (key.toLocaleLowerCase() !== e.key.toLocaleLowerCase()) {
          repeated = false;
          break;
        }
      }
      const prefix = repeated ? e.key : typeahead;
      const view = sourceView();
      if (!view) return;
      const abort = new AbortController(); typeaheadRequest = abort;
      const current = model.sourceIndex(model.indexOfEntry(rootId, path));
      void findTreePrefix({ prefix, from: current + (prefix.length === 1 ? 1 : 0),
        count: view.presentation().state?.extent.rows ?? 0, signal: abort.signal,
        frame: (offset, limit, signal) => view.frame(offset, limit, signal),
      }).then(index => {
        if (index == null || abort.signal.aborted || view !== sourceView() || document.activeElement !== target) return;
        return focusEntryByDisplayIndex(displayModel().displayIndex(index));
      }).catch(error => { if (!abort.signal.aborted) reportTreeError(error); });
      return;
    }
    if (
      (e.key === "F10" && e.shiftKey) ||
      e.key === "ContextMenu"
    ) {
      const row = target.closest<HTMLElement>("[data-files-ctx='tree-row']");
      if (!row) return;
      e.preventDefault();
      e.stopPropagation();
      const rect = target.getBoundingClientRect();
      openRowMenuAt(
        { x: rect.left, y: rect.bottom },
        {
          surface: "tree-row",
          rootId,
          path,
          name: row.dataset.name ?? path,
          isDir,
          isRoot: row.dataset.isroot === "true",
          deleted: row.dataset.deleted === "true",
        },
      );
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        focusEntryOffset(target, 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        focusEntryOffset(target, -1);
        break;
      case "Home":
        e.preventDefault();
        void focusEntryByDisplayIndex(model.firstEntryIndex()).catch(reportTreeError);
        break;
      case "End":
        e.preventDefault();
        void focusEntryByDisplayIndex(model.lastEntryIndex(), "nearest", true).catch(reportTreeError);
        break;
      case "Enter":
        if (!isDir) {
          e.preventDefault();
          target.click();
          focusRegion("files");
        }
        break;
      case "ArrowRight":
        e.preventDefault();
        if (isDir && !expanded) target.click();
        else if (isDir) focusEntryOffset(target, 1);
        else focusRegion("files");
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (isDir && expanded) {
          target.click();
          break;
        }
        if (rootId) {
          const parent = parentDir(path);
          const view = sourceView();
          if (view) void view.locate({ root_id: rootId, path: parent }).then(index => {
            if (view === sourceView() && surfaceLive() && index >= 0) return focusEntryByDisplayIndex(index);
          }).catch(reportTreeError);
        }
        break;
    }
  };

  const activateStickyFolder = (
    displayIndex: number,
    rootId: string,
    dir: string,
  ) => {
    void selectFolder(rootId, dir).catch(reportTreeError);
    void focusEntryByDisplayIndex(displayIndex).catch(reportTreeError);
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

  return (
    <>
      <Show when={props.filterOpen}>
        <div class="den-files-tree-pane-header">
          <input
            ref={filterEl}
            type="search"
            class="den-files-tree__filter"
            data-testid="files-tree-filter"
            data-files-ctx="no-menu"
            placeholder="Filter files"
            value={props.filterQuery ?? ""}
            onInput={(e) => props.onFilterQueryChange?.(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                props.onFilterQueryChange?.("");
                props.onFilterClose?.();
                e.preventDefault();
                e.stopPropagation();
              }
            }}
          />
        </div>
      </Show>
      <TreeContext.Provider value={ops}>
        <FilesTreeStickyOverlay
          host={stickyHostEl()}
          height={stickyHeight()}
          onKeyDown={onKeyDown}
          onWheel={forwardStickyWheel}
        >
          <For each={stickyKeys()}>
            {key => <StickySlotBox slotKey={key} slots={stickySlots} tops={stickyTops} />}
          </For>
        </FilesTreeStickyOverlay>
      <nav
        ref={bindNavEl}
        class="den-files-tree"
        data-testid="files-tree"
        data-files-ctx="tree-background"
        aria-label="Project files"
        data-fold-levels={stickyFold().levels}
        data-fold-floor={stickyFold().floor}
        onKeyDown={onKeyDown}
      >
          <div
            class="den-files-tree-virtual"
            style={{ height: `${geometry.extent(displayModel().length)}px` }}
          >
            <div class="den-files-tree-virtual__inner" style={{ transform: paintOffset() ? `translateY(${paintOffset()}px)` : undefined }}>
              <For each={[...renderedRows().keys()]}>
                {key => (
                  <ShowLatest when={renderedRows().get(key)}>
                    {item => (
                      <div
                        class="den-files-tree-virtual__row"
                        classList={{
                          "den-files-tree-virtual__row--lead":
                            item().position.index === 0 && editorTreeVisibleLevelsPref() !== 0 &&
                            stickySlots().length === 0 && paintOffset() === 0,
                        }}
                        data-display-key={item().row.key}
                        data-row-kind={item().row.kind}
                        style={{
                          height: `${item().position.size}px`,
                          transform: `translateY(${(geometryVersion(), geometry.physicalOffset(item().position.start))}px)`,
                        }}
                      >
                        <SwitchDisplayRow row={item().row} />
                      </div>
                    )}
                  </ShowLatest>
                )}
              </For>
            </div>
          </div>
          <Show when={showFilterEmpty()}>
            <p
              class="den-files-tree__hint"
              data-testid="files-tree-filter-empty"
            >
              No files match "{(props.filterQuery ?? "").trim()}"
            </p>
          </Show>
      </nav>
      </TreeContext.Provider>
      <Show when={dragGhost()} keyed>
        {(g) => (
          <div
            class="den-files-tree__drag-ghost"
            classList={{
              "den-files-tree__drag-ghost--invalid": dropAssessment()?.valid === false,
            }}
            style={{ left: `${g.x}px`, top: `${g.y}px` }}
            role="status"
          >
            <strong class="den-files-tree__drag-ghost-title">{g.label}</strong>
            <Show when={dropAssessment()} keyed>
              {(assessment) => (
                <span>
                  {assessment.valid ? `Move to ${assessment.destination}` : assessment.reason}
                </span>
              )}
            </Show>
          </div>
        )}
      </Show>
      <Show when={movePending()} keyed>
        {(move) => (
          <div class="den-files-tree__move-status" role="status">
            Moving {move.source.name} to {move.target.dir === "." ? `@${rootLabel(move.target.rootId)}` : move.target.dir}…
          </div>
        )}
      </Show>
    </>
  );
}

/** Row affordances mount on hover or focus; scrolled-in rows stay light. */
