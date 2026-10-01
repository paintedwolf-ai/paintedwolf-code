import { createMemo, createSignal } from "solid-js";
import {
  cancelPointerChatAttachmentDrag,
  finishPointerChatAttachmentDrag,
  updatePointerChatAttachmentDrag,
} from "../../chat/composer/chat-attachment-drag.ts";
import type {
  DirState,
  DragSource,
  DropTarget,
  DropAssessment,
  PendingMove,
  FilesTreeProps,
} from "./files-tree-context.ts";

const DRAG_THRESHOLD_PX = 4;
const HOVER_EXPAND_MS = 600;

export function parentDir(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "." : path.slice(0, i) || ".";
}

export function assessFilesTreeDrop(
  source: { rootId: string; path: string } | null,
  target: DropTarget | null,
  destination: string,
): DropAssessment | null {
  if (!source || !target) return null;
  if (source.rootId !== target.rootId) {
    return { valid: false, destination, reason: "Choose a folder in the same root." };
  }
  if (target.dir === source.path || target.dir.startsWith(`${source.path}/`)) {
    return { valid: false, destination, reason: "A folder cannot move into itself." };
  }
  if (parentDir(source.path) === target.dir) {
    return { valid: false, destination, reason: "Already in this folder." };
  }
  return { valid: true, destination };
}


type TreeDragOptions = {
  projectId: () => string;
  roots: () => FilesTreeProps["roots"];
  navEl: () => HTMLElement | undefined;
  movePath: () => FilesTreeProps["onMovePath"];
  dirState: (rootId: string, dir: string) => DirState;
  expandDir: (rootId: string, dir: string) => Promise<unknown>;
  reportTreeError: (error: unknown) => void;
};

export function createFilesTreeDrag(options: TreeDragOptions) {
  const { projectId, roots, navEl, dirState, expandDir, reportTreeError } = options;
  const callbacks = { get onMovePath() { return options.movePath(); } };
  const [dragSource, setDragSource] = createSignal<DragSource | null>(null);
  const [dropTarget, setDropTarget] = createSignal<DropTarget | null>(null);
  const [movePending, setMovePending] = createSignal<PendingMove | null>(null);
  const [dragGhost, setDragGhost] = createSignal<{
    x: number;
    y: number;
    label: string;
  } | null>(null);
  let hoverExpandTimer: ReturnType<typeof setTimeout> | undefined;
  const cancelDrag = () => {
    if (hoverExpandTimer) clearTimeout(hoverExpandTimer);
    hoverExpandTimer = undefined;
    setDragSource(null);
    setDropTarget(null);
    setDragGhost(null);
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
  };

  const rootLabel = (rootId: string) =>
    roots().find((root) => root.id === rootId)?.label ?? "root";

  const assessDrop = (
    source: DragSource | null,
    target: DropTarget | null,
  ): DropAssessment | null => {
    if (!source || !target) return null;
    const destination = target.dir === "."
      ? `@${rootLabel(target.rootId)}`
      : target.dir;
    return assessFilesTreeDrop(source, target, destination);
  };

  const dropAssessment = createMemo(() =>
    assessDrop(dragSource(), dropTarget()),
  );

  const dropTargetFromPoint = (x: number, y: number): DropTarget | null => {
    const el = document.elementFromPoint(x, y);
    if (!el || !navEl()?.contains(el)) return null;
    const row = el.closest<HTMLElement>("[data-files-ctx='tree-row']");
    if (!row) {
      const root = roots()[0];
      return root ? { rootId: root.id, dir: "." } : null;
    }
    const rootId = row.dataset.root?.trim() ?? "";
    if (!rootId) return null;
    const isDir = row.dataset.isdir === "true";
    const path = row.dataset.path?.trim() || ".";
    return { rootId, dir: isDir ? path : parentDir(path) };
  };

  const onRowPointerDown = (
    rootId: string,
    path: string,
    name: string,
    isDir: boolean,
    e: PointerEvent,
  ) => {
    if (e.button !== 0 || !callbacks.onMovePath) return;
    if (
      e.target instanceof HTMLElement &&
      e.target.closest(
        ".den-files-tree__more, .den-files-tree__add, .den-files-tree__twisty, input",
      )
    ) {
      return;
    }
    const startX = e.clientX;
    const startY = e.clientY;
    let dragging = false;
    const move = (ev: PointerEvent) => {
      if (!dragging) {
        if (
          Math.abs(ev.clientX - startX) < DRAG_THRESHOLD_PX &&
          Math.abs(ev.clientY - startY) < DRAG_THRESHOLD_PX
        ) {
          return;
        }
        dragging = true;
        setDragSource({ rootId, path, name, isDir });
        document.body.style.cursor = "grabbing";
        document.body.style.userSelect = "none";
      }
      setDragGhost({ x: ev.clientX + 12, y: ev.clientY + 8, label: name });
      const overChat = updatePointerChatAttachmentDrag(ev.clientX, ev.clientY);
      if (overChat) {
        setDropTarget(null);
        if (hoverExpandTimer) clearTimeout(hoverExpandTimer);
        hoverExpandTimer = undefined;
        return;
      }
      const target = dropTargetFromPoint(ev.clientX, ev.clientY);
      setDropTarget(target);
      if (hoverExpandTimer) clearTimeout(hoverExpandTimer);
      hoverExpandTimer = undefined;
      if (target && target.dir !== ".") {
        const st = dirState(target.rootId, target.dir);
        if (!st?.expanded) {
          const { rootId: rid, dir: d } = target;
          hoverExpandTimer = setTimeout(() => {
            void expandDir(rid, d).catch(reportTreeError);
          }, HOVER_EXPAND_MS);
        }
      }
    };
    const finish = (commit: boolean, event?: PointerEvent) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      window.removeEventListener("keydown", onKey, true);
      if (!dragging) return;
      const src = dragSource();
      const target = dropTarget();
      const assessment = assessDrop(src, target);
      const claimedByChat =
        src && event
          ? finishPointerChatAttachmentDrag(
              {
                kind: src.isDir ? "path-folder" : "path-file",
                projectId: projectId(),
                rootId: src.rootId,
                path: src.path,
                name: src.name,
              },
              event.clientX,
              event.clientY,
            )
          : (cancelPointerChatAttachmentDrag(), false);
      cancelDrag();
      if (claimedByChat) return;
      if (!commit || !src || !target || !assessment?.valid || !callbacks.onMovePath) return;
      setMovePending({ source: src, target });
      void callbacks.onMovePath(src.rootId, src.path, target.dir, src.isDir)
        .catch(reportTreeError)
        .finally(() => setMovePending(null));
    };
    const up = (event: PointerEvent) => finish(true, event);
    const cancel = () => finish(false);
    const onKey = (ke: KeyboardEvent) => {
      if (ke.key === "Escape" && dragging) {
        ke.stopPropagation();
        finish(false);
      }
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", cancel);
    window.addEventListener("keydown", onKey, true);
  };

  const clearHoverExpandTimer = () => { if (hoverExpandTimer) clearTimeout(hoverExpandTimer); };
  return {
    rootLabel,
    dragSource,
    dropTarget,
    movePending,
    dragGhost,
    dropAssessment,
    onRowPointerDown,
    cancelDrag,
    clearHoverExpandTimer,
  };
}
