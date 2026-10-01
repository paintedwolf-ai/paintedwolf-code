import { readEditorScrollPosition, setEditorScrollTop } from "../../components/source/editor/editor-scroll-position.ts";
import { createSignal } from "solid-js";
import type { EditorView } from "@codemirror/view";
import type { SourceEditorHandlers } from "../../components/source/editor/codemirror-theme.ts";
import { clearEditorViewportPool, releaseEditorViewport } from "../../components/source/editor/editor-viewport-pool.ts";
import {
  fileBufferKey,
  parseFileBufferKey,
  retargetPathUnderPath,
  type FileBufferKey,
} from "../components/project-files-model.ts";
import { captureBufferViewState } from "./editor-session-fidelity.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

type LiveEntry = {
  view: EditorView;
  projectId: string;
  key: FileBufferKey;
  path: string;
  handlers: SourceEditorHandlers;
  documentValid: boolean;
  /** Live input has not reached the cached text yet. */
  viewDocDirty: boolean;
  /** Alive without a host element. */
  detached: boolean;
  presented: boolean;
  /** Scroll offset captured before detaching. */
  heldScrollTop: number | null;
};

const live = new Map<string, LiveEntry>();
const [editorViewRevision, setEditorViewRevision] = createSignal(0);

/** Selection anchor changes invalidate command coordinates. */
export function notifyFilesEditorViewChanged(): void {
  setEditorViewRevision((revision) => revision + 1);
}

/** Detached editors retain state, but cannot supply an invocation target. */
export function mountedFilesEditorView(projectId: string, key: FileBufferKey): EditorView | null {
  editorViewRevision();
  const entry = live.get(id(projectId, key));
  return entry && !entry.detached && entry.presented ? entry.view : null;
}

export function setFilesEditorPresented(projectId: string, key: FileBufferKey, presented: boolean): void {
  const entry = live.get(id(projectId, key));
  if (!entry || entry.presented === presented) return;
  entry.presented = presented;
  notifyFilesEditorViewChanged();
}
/** Stable callbacks retained across editor lifetimes. */
const handlerBags = new Map<string, SourceEditorHandlers>();

/** Detached views beyond this limit release their editor after capturing view state. */
export const RETAINED_DETACHED_VIEWS = 6;
export const RETAINED_EDITOR_BYTE_BUDGET = 16 * 1024 * 1024;
/** Detached entry ids, coldest first. */
const detachedOrder: string[] = [];

function forgetDetached(k: string): void {
  const at = detachedOrder.indexOf(k);
  if (at >= 0) detachedOrder.splice(at, 1);
}

function retainDetached(k: string): void {
  forgetDetached(k);
  detachedOrder.push(k);
  const retainedBytes = () => detachedOrder.reduce((total, id) => {
    const view = live.get(id)?.view;
    return total + (view ? 65536 + view.state.doc.length * 4 + view.state.doc.lines * 128 : 0);
  }, 0);
  while (detachedOrder.length > RETAINED_DETACHED_VIEWS || retainedBytes() > RETAINED_EDITOR_BYTE_BUDGET) {
    const coldest = detachedOrder.shift();
    if (coldest === undefined) break;
    const entry = live.get(coldest);
    if (entry) destroyLiveEntry(entry, { preserveViewState: true, dropHandlers: false });
  }
}

function id(projectId: string, key: FileBufferKey): string {
  return `${projectId}\0${key}`;
}

function sameIdentity(
  entry: LiveEntry,
  projectId: string,
  key: FileBufferKey,
  path: string,
): boolean {
  return (
    entry.projectId === projectId && entry.key === key && entry.path === path
  );
}

function filesEditorHandlers(
  projectId: string,
  key: FileBufferKey,
): SourceEditorHandlers {
  const k = id(projectId, key);
  let bag = handlerBags.get(k);
  if (!bag) {
    bag = {};
    handlerBags.set(k, bag);
  }
  return bag;
}

export function getFilesEditorView(
  projectId: string,
  key: FileBufferKey,
): EditorView | undefined {
  return live.get(id(projectId, key))?.view;
}

export function getFilesEditorViewDoc(
  projectId: string,
  key: FileBufferKey,
): string | undefined {
  const entry = live.get(id(projectId, key));
  return entry?.documentValid ? entry.view.state.doc.toString() : undefined;
}

export function settleFilesEditorView(
  projectId: string,
  key: FileBufferKey,
): void {
  const entry = live.get(id(projectId, key));
  if (entry) {
    entry.documentValid = true;
    entry.viewDocDirty = false;
  }
}

/** Local input marks the cached text dirty without copying it. */
export function markFilesEditorViewDocDirty(
  projectId: string,
  key: FileBufferKey,
): void {
  const entry = live.get(id(projectId, key));
  if (entry) entry.viewDocDirty = true;
}

export function isFilesEditorViewDocDirty(
  projectId: string,
  key: FileBufferKey,
): boolean {
  return live.get(id(projectId, key))?.viewDocDirty === true;
}

/** Invalidated text allows draft sync to accept external writes. */
export function invalidateFilesEditorViewDoc(
  projectId: string,
  key: FileBufferKey,
): void {
  const entry = live.get(id(projectId, key));
  if (entry) {
    entry.documentValid = false;
    entry.viewDocDirty = false;
  }
}

/** Moves live editor registrations across a path rename. */
export function rekeyFilesEditorHostsUnderPath(
  projectId: string,
  rootId: string,
  fromPath: string,
  toPath: string,
): void {
  const moves: Array<{ from: string; to: string; entry: LiveEntry }> = [];
  for (const [from, entry] of live) {
    if (entry.projectId !== projectId) continue;
    const nextPath = retargetPathUnderPath(entry.path, fromPath, toPath);
    const parsed = parseFileBufferKey(entry.key);
    // Stable file identities retain their key across moves.
    if (
      nextPath != null &&
      parsed?.kind === "file" &&
      !parsed.jobId
    ) {
      entry.path = nextPath;
      continue;
    }
    if (
      nextPath == null ||
      parsed?.kind !== "path" ||
      parsed.rootId !== rootId ||
      parsed.path !== entry.path ||
      parsed.jobId
    ) {
      continue;
    }
    const nextKey = fileBufferKey(rootId, nextPath);
    entry.key = nextKey;
    entry.path = nextPath;
    moves.push({ from, to: id(projectId, nextKey), entry });
  }
  for (const move of moves) {
    live.delete(move.from);
    live.set(move.to, move.entry);
    const handlers = handlerBags.get(move.from);
    if (handlers) {
      handlerBags.delete(move.from);
      handlerBags.set(move.to, handlers);
    }
  }
}

export type FilesEditorAttachPlan =
  | {
      kind: "reused";
      view: EditorView;
      handlers: SourceEditorHandlers;
      scrollTop: number;
    }
  | { kind: "fresh"; handlers: SourceEditorHandlers };

export function beginFilesEditorAttach(args: {
  projectId: string;
  key: FileBufferKey;
  path: string;
  draft: string;
  host: HTMLElement;
}): FilesEditorAttachPlan {
  const k = id(args.projectId, args.key);
  const handlers = filesEditorHandlers(args.projectId, args.key);
  const existing = live.get(k);
  if (existing && sameIdentity(existing, args.projectId, args.key, args.path)) {
    const scrollTop =
      existing.heldScrollTop ?? readEditorScrollPosition(existing.view).top;
    if (existing.view.dom.parentElement !== args.host) {
      args.host.appendChild(existing.view.dom);
    }
    existing.detached = false;
    notifyFilesEditorViewChanged();
    forgetDetached(k);
    existing.heldScrollTop = null;
    // Reattachment restores scrolling before focus and measurement.
    if (scrollTop > 0) {
      setEditorScrollTop(existing.view, scrollTop);
    }
    existing.view.requestMeasure();
    return {
      kind: "reused",
      view: existing.view,
      handlers: existing.handlers,
      scrollTop,
    };
  }

  // Replacement preserves the current buffer's editor state.
  if (existing) destroyLiveEntry(existing, { preserveViewState: true, dropHandlers: false });

  return { kind: "fresh", handlers };
}

export function completeFilesEditorAttach(args: {
  projectId: string;
  key: FileBufferKey;
  path: string;
  view: EditorView;
  handlers: SourceEditorHandlers;
  host: HTMLElement;
}): void {
  const k = id(args.projectId, args.key);
  if (args.view.dom.parentElement !== args.host) {
    args.host.appendChild(args.view.dom);
  }
  live.set(k, {
    view: args.view,
    projectId: args.projectId,
    key: args.key,
    path: args.path,
    handlers: args.handlers,
    documentValid: true,
    viewDocDirty: false,
    detached: false,
    presented: true,
    heldScrollTop: null,
  });
  notifyFilesEditorViewChanged();
}

/** Retains a detached view for its next attachment. */
export function softDetachFilesEditor(
  projectId: string,
  key: FileBufferKey,
  host: HTMLElement,
): boolean {
  const k = id(projectId, key);
  const entry = live.get(k);
  if (!entry || entry.detached || entry.view.dom.parentElement !== host) return false;
  entry.heldScrollTop = readEditorScrollPosition(entry.view).top;
  if (entry.view.dom.parentElement === host) {
    host.removeChild(entry.view.dom);
  }
  entry.detached = true;
  notifyFilesEditorViewChanged();
  retainDetached(k);
  return true;
}

export function destroyFilesEditor(
  projectId: string,
  key: FileBufferKey,
  opts?: { preserveViewState?: boolean; dropHandlers?: boolean },
): void {
  const entry = live.get(id(projectId, key));
  if (!entry) {
    if (opts?.dropHandlers) handlerBags.delete(id(projectId, key));
    return;
  }
  destroyLiveEntry(entry, {
    preserveViewState: opts?.preserveViewState === true,
    dropHandlers: opts?.dropHandlers === true,
  });
}

function destroyLiveEntry(
  entry: LiveEntry,
  opts: { preserveViewState: boolean; dropHandlers: boolean },
): void {
  const k = id(entry.projectId, entry.key);
  live.delete(k);
  notifyFilesEditorViewChanged();
  forgetDetached(k);
  if (opts.preserveViewState) {
    const buffer = projectFilesState(entry.projectId).byKey[entry.key];
    if (buffer) captureBufferViewState(buffer, entry.view);
  }
  releaseEditorViewport(entry.view);
  if (opts.dropHandlers) handlerBags.delete(k);
}

export function dropFilesEditorHandlers(
  projectId: string,
  key: FileBufferKey,
): void {
  handlerBags.delete(id(projectId, key));
}

export function resetFilesEditorHostForTests(): void {
  for (const entry of [...live.values()]) {
    try {
      entry.view.destroy();
    } catch {
      // The view may already be destroyed.
    }
  }
  live.clear();
  handlerBags.clear();
  detachedOrder.length = 0;
  clearEditorViewportPool();
}
