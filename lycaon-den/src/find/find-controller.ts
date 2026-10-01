import { createSignal } from "solid-js";
import type { EditorView } from "@codemirror/view";
import {
  applyFindHighlights,
  clearFindHighlights,
  refreshMatches,
  setActiveFindMark,
  type FindMatch,
} from "./find-match.ts";
import {
  collapsedRevealHostsContaining,
  getFindRevealHost,
  resetFindRevealHostsForTests,
  type FindRevealHost,
} from "./find-reveal.ts";
import { createCodeMirrorFindProvider } from "./find-provider-codemirror.ts";
import {
  FIND_MATCH_COUNT_CAP,
  type FindMatchCount,
  type FindProvider,
  type FindProviderKind,
} from "./find-provider.ts";
import { registerEscapeLadderLayer } from "./escape-ladder.ts";
import {
  closeGotoLine,
  gotoLineController,
  resetGotoLineForTests,
} from "./goto-line-controller.ts";

export type FindableView = {
  id: string;
  /** Matching provider. Defaults to DOM. */
  provider?: FindProviderKind;
  /** Scroll/search root element currently mounted. */
  rootEl: () => HTMLElement | null;
  /** Ensure the match's host is painted (virtualized rows) then scroll into view. */
  scrollMatchIntoView: (match: FindMatch) => void;
  /** Required for editor-backed providers. */
  getEditorView?: () => EditorView | null | undefined;
};

const FIND_QUERY_DEBOUNCE_MS = 120;
const FIND_MUTATION_DEBOUNCE_MS = 80;

const views = new Map<string, FindableView>();
/** Find reveal cleanup by host. */
const findRevealRestores = new Map<string, () => void>();

const [open, setOpen] = createSignal(false);
const [query, setQueryInternal] = createSignal("");
const [replacement, setReplacementInternal] = createSignal("");
const [replaceOpen, setReplaceOpenInternal] = createSignal(false);
const [caseSensitive, setCaseSensitiveInternal] = createSignal(false);
const [matches, setMatches] = createSignal<FindMatch[]>([]);
const [activeIndex, setActiveIndex] = createSignal(-1);
const [collapsedCount, setCollapsedCount] = createSignal(0);
const [activeViewId, setActiveViewId] = createSignal<string | null>(null);
const [primaryViewId, setPrimaryViewId] = createSignal<string | null>(null);
const [scopeActive, setScopeActive] = createSignal(false);
const [selectAllCapped, setSelectAllCapped] = createSignal(false);
const [cmCount, setCmCount] = createSignal<FindMatchCount>({
  total: 0,
  activeIndex: -1,
  capped: false,
});

const FIND_HISTORY_CAP = 10;
let queryHistory: string[] = [];
let historyCursor = -1;
/** Query preserved during history navigation. */
let historyAnchor: string | null = null;

let mutationObserver: MutationObserver | null = null;
let mutationDebounceTimer: ReturnType<typeof setTimeout> | 0 = 0;
let queryDebounceTimer: ReturnType<typeof setTimeout> | 0 = 0;
let focusListenerAttached = false;
let observedRoot: HTMLElement | null = null;
let resizeObserver: ResizeObserver | null = null;
let scrollListenerAttached = false;
let repaintTimer: ReturnType<typeof setTimeout> | 0 = 0;
/** Suppresses observer refreshes during highlight writes. */
let applyingHighlights = false;
let cmProvider: FindProvider | null = null;

function isVitest(): boolean {
  return import.meta.env.MODE === "test";
}

export const findController = {
  isOpen: open,
  query,
  replacement,
  replaceOpen,
  caseSensitive,
  matches,
  activeIndex,
  collapsedCount,
  activeViewId,
  primaryViewId,
  scopeActive,
  selectAllCapped,
  cmCount,
};

function viewProvider(view: FindableView | null): FindProviderKind {
  return view?.provider === "codemirror" ? "codemirror" : "dom";
}

function ensureCmProvider(view: FindableView): FindProvider {
  if (!cmProvider) {
    const id = view.id;
    cmProvider = createCodeMirrorFindProvider({
      getView: () => views.get(id)?.getEditorView?.() ?? null,
    });
  }
  return cmProvider;
}

function disposeCmProvider(): void {
  cmProvider?.dispose();
  cmProvider = null;
  setCmCount({ total: 0, activeIndex: -1, capped: false });
  setScopeActive(false);
  setSelectAllCapped(false);
}

function syncCmCount(): void {
  if (!cmProvider) {
    setCmCount({ total: 0, activeIndex: -1, capped: false });
    return;
  }
  setCmCount(cmProvider.count());
}

export function findMatchCount(): FindMatchCount & { collapsed: number } {
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    const c = cmCount();
    return { ...c, collapsed: 0 };
  }
  const list = matches();
  const capped = list.length >= FIND_MATCH_COUNT_CAP;
  return {
    total: capped ? FIND_MATCH_COUNT_CAP : list.length,
    activeIndex: activeIndex(),
    capped,
    collapsed: collapsedCount(),
  };
}

/** Render `3 of 47` · `No results` · `999+` (and collapsed suffix for DOM). */
export function formatFindCountLabel(count: {
  total: number;
  activeIndex: number;
  capped: boolean;
  collapsed?: number;
}): string {
  if (count.capped) return "999+";
  if (count.total === 0) return "No results";
  const n = count.activeIndex >= 0 ? count.activeIndex + 1 : 0;
  const base = `${n} of ${count.total}`;
  const collapsed = count.collapsed ?? 0;
  if (collapsed <= 0) return base;
  return `${base} · ${collapsed} in collapsed`;
}

export function findSupportsScope(): boolean {
  return viewProvider(resolveActiveView()) === "codemirror";
}

export function findSelectionAllowsScope(): boolean {
  const view = resolveActiveView();
  if (viewProvider(view) !== "codemirror") return false;
  return ensureCmProvider(view!).selectionSpansMultipleLines();
}

export function setFindScopeToSelection(on: boolean): boolean {
  const view = resolveActiveView();
  if (viewProvider(view) !== "codemirror") return false;
  const ok = ensureCmProvider(view!).scopeToSelection(on);
  if (ok) {
    setScopeActive(on);
    if (query()) {
      ensureCmProvider(view!).setQuery(query(), {
        caseSensitive: caseSensitive(),
      });
      syncCmCount();
    }
  }
  return ok;
}

export function selectAllFindMatches(): boolean {
  const view = resolveActiveView();
  if (viewProvider(view) !== "codemirror") return false;
  const result = ensureCmProvider(view!).selectAllMatches();
  if (!result) return false;
  rememberFindQuery(query());
  closeFind();
  // Preserve the cap result after closing.
  setSelectAllCapped(result.capped);
  return true;
}

function rememberFindQuery(q: string): void {
  const t = q.trim();
  if (!t) return;
  queryHistory = [t, ...queryHistory.filter((h) => h !== t)].slice(
    0,
    FIND_HISTORY_CAP,
  );
  historyCursor = -1;
  historyAnchor = null;
}

export function findHistoryUp(current: string): string | null {
  if (queryHistory.length === 0) return null;
  if (historyAnchor == null) historyAnchor = current;
  if (current !== historyAnchor && historyCursor < 0) return null;
  const next = Math.min(historyCursor + 1, queryHistory.length - 1);
  historyCursor = next;
  return queryHistory[next] ?? null;
}

export function findHistoryDown(current: string): string | null {
  if (historyAnchor == null || historyCursor < 0) return null;
  if (current !== queryHistory[historyCursor]) return null;
  if (historyCursor === 0) {
    historyCursor = -1;
    const anchor = historyAnchor;
    historyAnchor = null;
    return anchor;
  }
  historyCursor -= 1;
  return queryHistory[historyCursor] ?? null;
}

export function isChatScopedFindable(
  viewId: string | null | undefined,
): boolean {
  if (!viewId) return false;
  if (viewId === "session-transcript") return true;
  if (viewId.startsWith("tool-body:")) return true;
  return false;
}

/** Find is open over chat content. */
export function findSearchesChat(): boolean {
  return open() && isChatScopedFindable(activeViewId() ?? primaryViewId());
}

export function editorCommandBarPlacement():
  | "composer"
  | "files-editor"
  | "shell"
  | "none" {
  if (gotoLineController.isOpen()) return "files-editor";
  if (!open()) return "none";
  const id = activeViewId() ?? primaryViewId();
  if (isChatScopedFindable(id)) return "composer";
  return id === "files-editor" ? "files-editor" : "shell";
}

export function registerFindableView(view: FindableView): () => void {
  views.set(view.id, view);
  ensureFocusListener();
  return () => unregisterFindableView(view.id, view);
}

export function unregisterFindableView(id: string, expected?: FindableView): void {
  const registered = views.get(id);
  // Ignore cleanup from a superseded registration.
  if (!registered || (expected && registered !== expected)) return;
  if (activeViewId() === id) {
    clearFindHighlights(registered.rootEl());
    detachViewObservers();
    setActiveViewId(null);
    setMatches([]);
    setActiveIndex(-1);
    setCollapsedCount(0);
  }
  views.delete(id);
  if (primaryViewId() === id) setPrimaryViewId(null);
  if (views.size === 0) teardownFocusListener();
}

export function setPrimaryFindableView(id: string | null): void {
  setPrimaryViewId(id);
}

export function activateFindableViewForEditor(view: EditorView): boolean {
  const activate = (id: string) => {
    if (activeViewId() === id) return;
    clearActiveHighlights();
    disposeCmProvider();
    setActiveViewId(id);
  };
  for (const [id, fv] of views) {
    if (viewProvider(fv) !== "codemirror") continue;
    if (fv.getEditorView?.() === view) {
      activate(id);
      return true;
    }
  }
  if (views.has("files-editor")) {
    activate("files-editor");
    return true;
  }
  return false;
}

function resolveActiveView(): FindableView | null {
  const id = activeViewId() ?? primaryViewId();
  if (!id) return null;
  return views.get(id) ?? null;
}

function clearActiveHighlights(): void {
  const view = resolveActiveView();
  clearFindHighlights(view?.rootEl() ?? null);
}

function countCollapsed(list: FindMatch[]): number {
  let n = 0;
  for (const m of list) if (m.collapsed) n++;
  return n;
}

function restoreAllFindReveals(): void {
  for (const restore of findRevealRestores.values()) {
    try {
      restore();
    } catch {
      /* The host may unmount. */
    }
  }
  findRevealRestores.clear();
}

/** Restore expanded hosts except those still needed for the active match. */
function syncFindReveals(keepIds: ReadonlySet<string>): void {
  for (const [id, restore] of [...findRevealRestores]) {
    if (keepIds.has(id)) continue;
    try {
      restore();
    } catch {
      /* The host may unmount. */
    }
    findRevealRestores.delete(id);
  }
}

/** Expand collapsed hosts and report whether rematching is needed. */
function revealForActiveMatch(match: FindMatch | undefined): boolean {
  if (!match) {
    syncFindReveals(new Set());
    return false;
  }

  // Synthetic ranges may anchor outside their reveal host.
  const needed: FindRevealHost[] = [
    ...collapsedRevealHostsContaining(match.range.startContainer),
  ];
  let syntheticPrimary: FindRevealHost | null = null;
  if (match.revealHostId) {
    const primary = getFindRevealHost(match.revealHostId);
    if (primary && !needed.some((h) => h.id === primary.id)) {
      needed.push(primary);
      syntheticPrimary = primary;
    }
  }

  // Retain already-open ancestors while revealing a nested match.
  for (const id of findRevealRestores.keys()) {
    const ancestor = getFindRevealHost(id);
    if (ancestor?.hostEl()?.contains(match.range.startContainer) && !needed.some(host => host.id === id)) {
      needed.unshift(ancestor);
    }
  }

  const keepIds = new Set(needed.map((h) => h.id));
  syncFindReveals(keepIds);

  let needsRematch = false;
  if (match.remoteIndex != null) getFindRevealHost(match.revealHostId ?? "")?.revealRemoteMatch?.(match.remoteIndex);
  for (const host of needed) {
    if (!host.isCollapsed()) {
      if (match.remoteIndex == null && syntheticPrimary && host.id === syntheticPrimary.id) {
        needsRematch = true;
      }
      continue;
    }
    if (!findRevealRestores.has(host.id)) {
      findRevealRestores.set(host.id, host.revealForFind());
      needsRematch = true;
    }
  }
  return match.remoteIndex == null && needsRematch;
}

function pickActiveAfterRematch(
  list: FindMatch[],
  preferHostId: string | undefined,
  previousIndex: number,
): number {
  if (list.length === 0) return -1;
  if (preferHostId) {
    const inHost = list.findIndex(
      (m) => m.revealHostId === preferHostId || hostContainsMatch(preferHostId, m),
    );
    if (inHost >= 0) return inHost;
  }
  if (previousIndex >= 0 && previousIndex < list.length) return previousIndex;
  return 0;
}

function hostContainsMatch(hostId: string, match: FindMatch): boolean {
  const host = getFindRevealHost(hostId);
  const el = host?.hostEl();
  if (!el) return match.revealHostId === hostId;
  try {
    const node = match.range.startContainer;
    const target = node instanceof Element ? node : node.parentElement;
    return Boolean(target && el.contains(target));
  } catch {
    return match.revealHostId === hostId;
  }
}

function ensureFocusListener(): void {
  if (focusListenerAttached || typeof document === "undefined") return;
  document.addEventListener("focusin", onFocusIn);
  focusListenerAttached = true;
}

function teardownFocusListener(): void {
  if (!focusListenerAttached || typeof document === "undefined") return;
  document.removeEventListener("focusin", onFocusIn);
  focusListenerAttached = false;
}

function onFocusIn(event: FocusEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  for (const view of views.values()) {
    const root = view.rootEl();
    const cmView = view.getEditorView?.();
    const inCm = Boolean(cmView?.dom.contains(target as Node));
    if ((root && root.contains(target)) || inCm) {
      if (activeViewId() !== view.id) {
        clearActiveHighlights();
        setActiveViewId(view.id);
        if (open() && query()) {
          if (viewProvider(view) === "codemirror") {
            // Rebind the provider when the active view changes.
            disposeCmProvider();
            ensureCmProvider(view).setQuery(query(), {
              caseSensitive: caseSensitive(),
            });
            syncCmCount();
          } else {
            disposeCmProvider();
            recompute({ scroll: false });
          }
        }
      }
      return;
    }
  }
}

function cancelMutationDebounce(): void {
  if (mutationDebounceTimer) {
    clearTimeout(mutationDebounceTimer);
    mutationDebounceTimer = 0;
  }
}

function cancelQueryDebounce(): void {
  if (queryDebounceTimer) {
    clearTimeout(queryDebounceTimer);
    queryDebounceTimer = 0;
  }
}

function scheduleMutationRefresh(): void {
  if (applyingHighlights) return;
  cancelMutationDebounce();
  mutationDebounceTimer = setTimeout(() => {
    mutationDebounceTimer = 0;
    if (applyingHighlights || !open() || !query()) return;
    recompute({ scroll: false });
  }, FIND_MUTATION_DEBOUNCE_MS);
}

function attachMutationObserver(root: HTMLElement): void {
  detachMutationObserver();
  mutationObserver = new MutationObserver((records) => {
    if (applyingHighlights) return;
    const onlyOverlay = records.every((record) => {
      if (
        record.target instanceof Element &&
        record.target.closest("[data-den-find-overlay]")
      ) {
        return true;
      }
      const nodes = [...record.addedNodes, ...record.removedNodes];
      return (
        nodes.length > 0 &&
        nodes.every(
          (n) =>
            n instanceof Element &&
            (n.hasAttribute("data-den-find-overlay") ||
              n.closest("[data-den-find-overlay]")),
        )
      );
    });
    if (onlyOverlay) return;
    scheduleMutationRefresh();
  });
  mutationObserver.observe(root, {
    subtree: true,
    childList: true,
    characterData: true,
  });
}

function detachMutationObserver(): void {
  mutationObserver?.disconnect();
  mutationObserver = null;
  cancelMutationDebounce();
}

function cancelRepaint(): void {
  if (!repaintTimer) return;
  clearTimeout(repaintTimer);
  repaintTimer = 0;
}

function scheduleHighlightRepaint(): void {
  if (applyingHighlights || repaintTimer) return;
  repaintTimer = setTimeout(() => {
    repaintTimer = 0;
    if (applyingHighlights || !open() || !query()) return;
    const view = resolveActiveView();
    const root = view?.rootEl();
    if (!root || viewProvider(view) !== "dom") return;
    applyingHighlights = true;
    try {
      applyFindHighlights(root, matches(), activeIndex());
    } finally {
      applyingHighlights = false;
    }
  }, 0);
}

function onDocumentScroll(event: Event): void {
  const root = observedRoot;
  const target = event.target;
  if (!root || !(target instanceof Node)) return;
  if (target === document || root.contains(target) || target.contains(root)) {
    scheduleHighlightRepaint();
  }
}

function attachViewObservers(root: HTMLElement): void {
  if (observedRoot === root) return;
  detachViewObservers();
  observedRoot = root;
  attachMutationObserver(root);
  if (typeof document !== "undefined") {
    document.addEventListener("scroll", onDocumentScroll, true);
    scrollListenerAttached = true;
  }
  if (typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(() => scheduleHighlightRepaint());
    resizeObserver.observe(root);
  }
}

function detachViewObservers(): void {
  detachMutationObserver();
  resizeObserver?.disconnect();
  resizeObserver = null;
  if (scrollListenerAttached && typeof document !== "undefined") {
    document.removeEventListener("scroll", onDocumentScroll, true);
  }
  scrollListenerAttached = false;
  observedRoot = null;
  cancelRepaint();
}

function recompute(opts?: {
  scroll?: boolean;
  preferHostId?: string;
  /** When true, expand active match hosts then scroll (next/prev / settle). */
  activate?: boolean;
}): void {
  cancelQueryDebounce();
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    // Editor matching does not walk rendered text.
    return;
  }
  const root = view?.rootEl() ?? null;
  const q = query();
  if (!root || !q) {
    clearFindHighlights(root);
    setMatches([]);
    setActiveIndex(-1);
    setCollapsedCount(0);
    return;
  }
  const prev = activeIndex();
  applyingHighlights = true;
  try {
    const next = refreshMatches(root, q, caseSensitive(), prev);
    let idx = next.activeIndex;
    if (opts?.preferHostId) {
      idx = pickActiveAfterRematch(next.matches, opts.preferHostId, prev);
    }
    setMatches(next.matches);
    setActiveIndex(idx);
    setCollapsedCount(countCollapsed(next.matches));

    if (opts?.activate) {
      const match = next.matches[idx];
      const preferHostId =
        opts.preferHostId ?? match?.revealHostId ?? undefined;
      const needsRematch = revealForActiveMatch(match);
      if (needsRematch) {
        const after = refreshMatches(root, q, caseSensitive(), idx);
        const afterIdx = pickActiveAfterRematch(
          after.matches,
          preferHostId,
          idx,
        );
        setMatches(after.matches);
        setActiveIndex(afterIdx);
        setCollapsedCount(countCollapsed(after.matches));
        if (opts?.scroll !== false) {
          const m = after.matches[afterIdx];
          if (m && view) view.scrollMatchIntoView(m);
        }
        attachViewObservers(root);
        return;
      }
    }

    if (opts?.scroll !== false) {
      const match = next.matches[idx];
      if (match && view) view.scrollMatchIntoView(match);
    }
  } finally {
    applyingHighlights = false;
  }
  attachViewObservers(root);
}

function paintActive(revealed = new Set<string>()): void {
  const view = resolveActiveView();
  const root = view?.rootEl();
  if (!root) return;
  const list = matches();
  const idx = activeIndex();
  if (list.length === 0 || idx < 0) return;

  const preferHostId = list[idx]?.revealHostId;
  const needsRematch = revealForActiveMatch(list[idx]);
  if (needsRematch) {
    if (preferHostId) revealed.add(preferHostId);
    recompute({ scroll: true, activate: false, preferHostId });
    const nested = matches()[activeIndex()]?.revealHostId;
    if (nested && !revealed.has(nested)) paintActive(revealed);
    return;
  }

  applyingHighlights = true;
  try {
    const hasOverlay = root.querySelector(`[data-den-find-overlay]`);
    const hasActiveMark =
      hasOverlay &&
      root.querySelector(
        `[data-den-find-mark][data-den-find-index="${idx}"]`,
      );
    if (!hasActiveMark) {
      try {
        applyFindHighlights(root, list, idx);
      } catch {
        /* Navigation survives paint failure. */
      }
    } else {
      setActiveFindMark(root, idx);
    }
    const match = list[idx];
    if (match && view) view.scrollMatchIntoView(match);
  } finally {
    applyingHighlights = false;
  }
}

export function openFind(): void {
  if (!activeViewId()) {
    const primary = primaryViewId();
    if (primary && views.has(primary)) setActiveViewId(primary);
  }
  const view = resolveActiveView();
  if (!view) return;
  closeGotoLine(false);
  setOpen(true);
  setSelectAllCapped(false);
  if (viewProvider(view) === "codemirror") {
    if (query()) {
      ensureCmProvider(view).setQuery(query(), {
        caseSensitive: caseSensitive(),
      });
      syncCmCount();
    }
    return;
  }
  if (query()) recompute({ scroll: true, activate: true });
  else {
    const root = view.rootEl();
    if (root) attachViewObservers(root);
  }
}

export function toggleFind(): void {
  if (open()) {
    closeFind();
    return;
  }
  openFind();
}

export function closeFind(): void {
  cancelQueryDebounce();
  rememberFindQuery(query());
  clearActiveHighlights();
  detachViewObservers();
  restoreAllFindReveals();
  disposeCmProvider();
  setOpen(false);
  setMatches([]);
  setActiveIndex(-1);
  setCollapsedCount(0);
  setScopeActive(false);
}

export function setFindQuery(next: string): void {
  historyCursor = -1;
  historyAnchor = null;
  setQueryInternal(next);
  if (!open()) return;
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    cancelQueryDebounce();
    if (!next) {
      ensureCmProvider(view!).setQuery("", { caseSensitive: caseSensitive() });
      syncCmCount();
      return;
    }
    const apply = () => {
      if (!open() || !query()) return;
      ensureCmProvider(view!).setQuery(query(), {
        caseSensitive: caseSensitive(),
      });
      syncCmCount();
    };
    if (isVitest()) {
      apply();
      return;
    }
    queryDebounceTimer = setTimeout(() => {
      queryDebounceTimer = 0;
      apply();
    }, FIND_QUERY_DEBOUNCE_MS);
    return;
  }
  if (!next) {
    cancelQueryDebounce();
    clearActiveHighlights();
    setMatches([]);
    setActiveIndex(-1);
    setCollapsedCount(0);
    return;
  }
  setActiveIndex(0);
  if (isVitest()) {
    // Typing updates matches without expanding hosts.
    recompute({ scroll: true, activate: false });
    return;
  }
  cancelQueryDebounce();
  queryDebounceTimer = setTimeout(() => {
    queryDebounceTimer = 0;
    if (!open() || !query()) return;
    recompute({ scroll: true, activate: false });
  }, FIND_QUERY_DEBOUNCE_MS);
}

export function setFindReplacement(next: string): void {
  setReplacementInternal(next);
}

/** The replace lane stays collapsed until asked for, and only where it applies. */
export function setFindReplaceOpen(next: boolean): void {
  if (next && !findSupportsReplacement()) return;
  setReplaceOpenInternal(next);
}

export function toggleFindReplace(): void {
  if (replaceOpen()) {
    setReplaceOpenInternal(false);
    return;
  }
  if (!open()) openFind();
  setFindReplaceOpen(true);
}

/** Replacement is only available for the active, writable editor buffer. */
export function findSupportsReplacement(): boolean {
  const view = resolveActiveView();
  return (
    viewProvider(view) === "codemirror" &&
    !view?.getEditorView?.()?.state.readOnly
  );
}

export function replaceCurrentFindMatch(): boolean {
  const view = resolveActiveView();
  if (
    viewProvider(view) !== "codemirror" ||
    view?.getEditorView?.()?.state.readOnly ||
    !query()
  ) return false;
  const replaced = ensureCmProvider(view!).replaceCurrent(replacement());
  syncCmCount();
  return replaced;
}

export function replaceAllFindMatches(): { replaced: number; capped: boolean } {
  const view = resolveActiveView();
  if (
    viewProvider(view) !== "codemirror" ||
    view?.getEditorView?.()?.state.readOnly ||
    !query()
  ) {
    return { replaced: 0, capped: false };
  }
  const result = ensureCmProvider(view!).replaceAll(replacement());
  syncCmCount();
  return result;
}

function flushFindQuery(): void {
  if (!queryDebounceTimer) return;
  cancelQueryDebounce();
  if (!open() || !query()) return;
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    ensureCmProvider(view!).setQuery(query(), {
      caseSensitive: caseSensitive(),
    });
    syncCmCount();
    return;
  }
  recompute({ scroll: false, activate: false });
}

export function setFindCaseSensitive(next: boolean): void {
  setCaseSensitiveInternal(next);
  if (open() && query()) {
    cancelQueryDebounce();
    const view = resolveActiveView();
    if (viewProvider(view) === "codemirror") {
      ensureCmProvider(view!).setQuery(query(), { caseSensitive: next });
      syncCmCount();
      return;
    }
    setActiveIndex(0);
    recompute({ scroll: true, activate: false });
  }
}

export function findNext(): void {
  if (!open()) openFind();
  flushFindQuery();
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    ensureCmProvider(view!).next();
    syncCmCount();
    return;
  }
  const list = matches();
  if (list.length === 0) {
    if (query()) recompute({ scroll: true, activate: true });
    return;
  }
  // Expand a collapsed hit before advancing.
  const cur = list[activeIndex()];
  if (cur?.collapsed) {
    paintActive();
    return;
  }
  const next = (activeIndex() + 1) % list.length;
  setActiveIndex(next);
  paintActive();
}

export function findPrev(): void {
  if (!open()) openFind();
  flushFindQuery();
  const view = resolveActiveView();
  if (viewProvider(view) === "codemirror") {
    ensureCmProvider(view!).prev();
    syncCmCount();
    return;
  }
  const list = matches();
  if (list.length === 0) {
    if (query()) recompute({ scroll: true, activate: true });
    return;
  }
  const cur = list[activeIndex()];
  if (cur?.collapsed) {
    paintActive();
    return;
  }
  const next = (activeIndex() - 1 + list.length) % list.length;
  setActiveIndex(next);
  paintActive();
}

export function findEverywhereSeed(): string {
  return query().trim();
}

export function resetFindControllerForTests(): void {
  cancelQueryDebounce();
  detachViewObservers();
  teardownFocusListener();
  for (const view of views.values()) {
    clearFindHighlights(view.rootEl());
  }
  views.clear();
  restoreAllFindReveals();
  resetFindRevealHostsForTests();
  disposeCmProvider();
  queryHistory = [];
  historyCursor = -1;
  historyAnchor = null;
  setOpen(false);
  setQueryInternal("");
  setReplacementInternal("");
  setReplaceOpenInternal(false);
  setCaseSensitiveInternal(false);
  setMatches([]);
  setActiveIndex(-1);
  setCollapsedCount(0);
  setActiveViewId(null);
  setPrimaryViewId(null);
  setScopeActive(false);
  setSelectAllCapped(false);
  resetGotoLineForTests();
}

registerEscapeLadderLayer("findBar", {
  isOpen: () => open() || gotoLineController.isOpen(),
  dismiss: () => {
    if (gotoLineController.isOpen()) closeGotoLine();
    else closeFind();
  },
});

/** Refresh async source results without changing the query or selection. */
export function refreshFindResults(): void { if (open()) recompute({ scroll: false, activate: true }); }

export function isInFindScope(element: HTMLElement | null | undefined): boolean {
 const root = resolveActiveView()?.rootEl();
 return !!element && !!root && (root === element || root.contains(element));
}
