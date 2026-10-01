import { createSignal, onCleanup, onMount } from "solid-js";
import { cancelPointerChatAttachmentDrag, finishPointerChatAttachmentDrag, updatePointerChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import { clearFileTabDropTarget, listenFileTabWindowDrag, updateFileTabDropTarget, type FileTabWindowDrag, type FileTabWindowDragEvent, type ItemWindowDropPosition } from "../../platform/windows/item-windows.ts";
import { isShellLayoutUnstable, onShellLayoutSettled } from "../../shell/shell-layout-busy.ts";
import { publishEditorDocumentDraft } from "../documents/editor-document.ts";
import { scheduleSessionFidelityPersist } from "../editor/editor-session-fidelity.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { scheduleFilesHotExitPersist } from "../documents/files-hot-exit.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { moveFilesBuffer, openFilesBuffer, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";

type TabDragDependencies = Pick<FilesScope, "projectId" | "state" | "filesRoots" | "tabsElement" | "scrollerElement" | "closeBuffer"> & {
  fileTabWindowDrag: () => FileTabWindowDrag | undefined;
  openInNewWindow: () => ((key?: FileBufferKey, position?: ItemWindowDropPosition) => void | Promise<void>) | undefined;
  onFileTabMovedOut: () => ((key: FileBufferKey) => void | Promise<void>) | undefined;
};

export function createFilesTabDrag({ projectId, state, filesRoots, tabsElement, scrollerElement,
  fileTabWindowDrag, openInNewWindow, onFileTabMovedOut, closeBuffer }: TabDragDependencies) {
  const [dragKey, setDragKey] = createSignal<FileBufferKey | null>(null);
  const [dropAt, setDropAt] = createSignal<{
    key: FileBufferKey;
    after: boolean;
  } | null>(null);
  const [externalDrop, setExternalDrop] = createSignal<{
    dragLabel: string;
    target: { key: FileBufferKey; after: boolean } | null;
  } | null>(null);
  const visibleDropAt = () => dropAt() ?? externalDrop()?.target ?? null;
  const [pullOffPreview, setPullOffPreview] = createSignal<{
    name: string;
    x: number;
    y: number;
  } | null>(null);
  const dropTargetFromPoint = (
    x: number,
  ): { key: FileBufferKey; after: boolean } | null => {
    const tabsEl = tabsElement();
    if (!tabsEl) return null;
    let last: { key: FileBufferKey; after: boolean } | null = null;
    for (const tab of tabsEl.querySelectorAll<HTMLElement>(".den-files-tab")) {
      const key = tab.dataset.key;
      if (!key) continue;
      const rect = tab.getBoundingClientRect();
      if (x < rect.left) return { key, after: false };
      if (x <= rect.right) {
        return { key, after: x > rect.left + rect.width / 2 };
      }
      last = { key, after: true };
    }
    return last;
  };

  const localClientX = (screenX: number) => screenX - window.screenX;

  const acceptExternalTabDrop = (
    event: FileTabWindowDragEvent,
    target: { key: FileBufferKey; after: boolean } | null,
  ) => {
    if (event.projectId !== projectId()) return;
    const root = filesRoots().find((candidate) => candidate.id === event.rootId);
    if (!root) return;
    const before = projectFilesState(projectId());
    const targetIndex = target
      ? before.order.indexOf(target.key) + (target.after ? 1 : 0)
      : before.order.length;
    const key = openFilesBuffer(projectId(), {
      rootId: event.rootId,
      rootLabel: root.label,
      path: event.path,
      intent: "permanent",
    });
    const order = projectFilesState(projectId()).order;
    const from = order.indexOf(key);
    let to = targetIndex;
    if (from >= 0 && target?.key !== key) {
      if (from < to) to -= 1;
      moveFilesBuffer(projectId(), key, to);
    }
    setFilesActiveBuffer(projectId(), key);
    scheduleFilesHotExitPersist(projectId());
    scheduleSessionFidelityPersist();
  };

  let fileTabDropReady = false;
  let fileTabDropTargetFingerprint: string | null = null;
  let fileTabDropSyncScheduled = false;
  const scheduleFileTabDropTargetSync = () => {
    if (fileTabDropSyncScheduled) return;
    fileTabDropSyncScheduled = true;
    queueMicrotask(() => {
      fileTabDropSyncScheduled = false;
      syncFileTabDropTarget();
    });
  };
  const syncFileTabDropTarget = () => {
    if (!fileTabDropReady || isShellLayoutUnstable()) return;
    const tabsScrollerEl = scrollerElement();
    const rect = tabsScrollerEl?.isConnected
      ? tabsScrollerEl.getBoundingClientRect()
      : null;
    if (!rect || rect.width <= 0 || rect.height <= 0) {
      if (fileTabDropTargetFingerprint === "clear") return;
      fileTabDropTargetFingerprint = "clear";
      void clearFileTabDropTarget().catch((err) => {
        if (fileTabDropTargetFingerprint === "clear") {
          fileTabDropTargetFingerprint = null;
        }
        console.debug("[item-window] clear tab drop target failed", err);
      });
      return;
    }
    const fingerprint = [
      projectId(),
      rect.left,
      rect.top,
      rect.width,
      rect.height,
    ].join("\0");
    if (fileTabDropTargetFingerprint === fingerprint) return;
    fileTabDropTargetFingerprint = fingerprint;
    void updateFileTabDropTarget(projectId(), rect).catch((err) => {
      if (fileTabDropTargetFingerprint === fingerprint) {
        fileTabDropTargetFingerprint = null;
      }
      console.debug("[item-window] update tab drop target failed", err);
    });
  };

  // Resize and layout settle are the only drop-target geometry changes.

  const onTabPointerDown = (key: FileBufferKey, e: PointerEvent) => {
    if (e.button !== 0) return;
    if (
      e.target instanceof HTMLElement &&
      e.target.closest(".den-files-tab__close, .den-files-tab__header")
    ) {
      return;
    }
    const pointerTarget =
      e.currentTarget instanceof HTMLElement ? e.currentTarget : null;
    const pointerId = e.pointerId;
    const startX = e.clientX;
    const startY = e.clientY;
    let dragging = false;
    let pulledOff = false;
    type NativePull = {
      mode: "active" | "finish" | "cancel";
      label: string | null;
      queuedPosition: ItemWindowDropPosition | null;
      moving: Promise<void> | null;
      promise: Promise<string>;
    };
    let nativePull: NativePull | null = null;

    const sendNativePullMove = (pull: NativePull) => {
      const controller = fileTabWindowDrag();
      const label = pull.label;
      const position = pull.queuedPosition;
      if (!controller || !label || !position || pull.moving) return;
      pull.queuedPosition = null;
      pull.moving = controller
        .move(label, position)
        .then(
          () => undefined,
          (err) => console.debug("[item-window] move drag window failed", err),
        )
        .finally(() => {
          pull.moving = null;
          if (pull.mode === "active") sendNativePullMove(pull);
        });
    };

    const cancelNativePull = (pull: NativePull) => {
      pull.mode = "cancel";
      pull.queuedPosition = null;
      if (nativePull === pull) nativePull = null;
      void pull.promise
        .then(
          async (label) => {
            await pull.moving;
            await fileTabWindowDrag()?.cancel(label);
          },
          (err) => console.debug("[item-window] begin drag failed", err),
        )
        .catch((err) => console.debug("[item-window] cancel drag failed", err));
    };

    const updateNativePull = (position: ItemWindowDropPosition) => {
      const controller = fileTabWindowDrag();
      const dragged = state().byKey[key];
      if (!controller || !dragged || isComposedBufferKind(dragged.kind)) return;
      if (!nativePull) {
        const pull: NativePull = {
          mode: "active",
          label: null,
          queuedPosition: position,
          moving: null,
          promise: controller.begin(key, position),
        };
        nativePull = pull;
        void pull.promise.then(
          (label) => {
            pull.label = label;
            if (pull.mode !== "active" || nativePull !== pull) return;
            setPullOffPreview(null);
            sendNativePullMove(pull);
          },
          () => {
            if (nativePull === pull) nativePull = null;
          },
        );
        return;
      }
      nativePull.queuedPosition = position;
      sendNativePullMove(nativePull);
    };

    const finishNativePull = (
      pull: NativePull,
      position: ItemWindowDropPosition,
    ) => {
      const controller = fileTabWindowDrag();
      if (!controller) return;
      pull.mode = "finish";
      pull.queuedPosition = null;
      if (nativePull === pull) nativePull = null;
      void pull.promise.then(
        async (label) => {
          try {
            flushFilesDraftSyncFor(projectId(), key);
            const buffer = projectFilesState(projectId()).byKey[key];
            if (!buffer)
              throw new Error("The dragged file tab is no longer open.");
            if (
              buffer.dirty &&
              !(await publishEditorDocumentDraft(projectId(), buffer))
            ) {
              throw new Error("The latest file draft could not be handed off.");
            }
            await pull.moving;
            if (!(await controller.finish(label, position))) {
              throw new Error("The native drag window could not be finalized.");
            }
            const released = closeBuffer(key);
            if (!released || (await released) === "restored") return;
            scheduleSessionFidelityPersist();
            void onFileTabMovedOut()?.(key);
          } catch (err) {
            void controller
              .cancel(label)
              .catch((cancelErr) =>
                console.debug("[item-window] cancel drag failed", cancelErr),
              );
            console.debug("[item-window] move file failed", err);
          }
        },
        (err) => console.debug("[item-window] begin drag failed", err),
      );
    };

    const move = (ev: PointerEvent) => {
      if (!dragging) {
        if (
          Math.abs(ev.clientX - startX) < 5 &&
          Math.abs(ev.clientY - startY) < 5
        ) {
          return;
        }
        // Capturing a press would redirect the label's click to the container.
        if (pointerTarget && Number.isFinite(pointerId)) {
          try {
            pointerTarget.setPointerCapture(pointerId);
          } catch {
            // The pointer may have ended before this move was delivered.
          }
        }
        dragging = true;
        setDragKey(key);
        document.body.style.cursor = "grabbing";
        document.body.style.userSelect = "none";
      }
      if (updatePointerChatAttachmentDrag(ev.clientX, ev.clientY)) {
        pulledOff = false;
        if (nativePull) cancelNativePull(nativePull);
        setPullOffPreview(null);
        setDropAt(null);
        return;
      }
      const stripRect = scrollerElement()?.getBoundingClientRect();
      pulledOff = Boolean(
        (fileTabWindowDrag() || openInNewWindow()) &&
        stripRect &&
        (ev.clientX < stripRect.left - 24 ||
          ev.clientX > stripRect.right + 24 ||
          ev.clientY < stripRect.top - 24 ||
          ev.clientY > stripRect.bottom + 24),
      );
      if (pulledOff) {
        updateNativePull({ x: ev.screenX, y: ev.screenY });
        if (!nativePull?.label) {
          setPullOffPreview({
            name: state().byKey[key]?.name ?? "File",
            x: ev.clientX,
            y: ev.clientY,
          });
        }
        setDropAt(null);
      } else {
        if (nativePull) cancelNativePull(nativePull);
        setPullOffPreview(null);
        setDropAt(dropTargetFromPoint(ev.clientX));
      }
    };
    const finish = (commit: boolean, ev?: PointerEvent) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      window.removeEventListener("keydown", onKey, true);
      if (
        pointerTarget &&
        Number.isFinite(pointerId) &&
        pointerTarget.hasPointerCapture(pointerId)
      ) {
        pointerTarget.releasePointerCapture(pointerId);
      }
      if (!dragging) return;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      // Suppress the click emitted after a tab drag.
      const squash = (ce: MouseEvent) => {
        ce.stopPropagation();
        ce.preventDefault();
      };
      window.addEventListener("click", squash, { capture: true, once: true });
      setTimeout(() => window.removeEventListener("click", squash, true), 0);
      const target = dropAt();
      setDragKey(null);
      setDropAt(null);
      setPullOffPreview(null);
      const buffer = state().byKey[key];
      const claimedByChat =
        commit && ev && buffer?.rootId.trim() && buffer.path.trim()
          ? finishPointerChatAttachmentDrag(
              {
                kind: "path-file",
                projectId: projectId(),
                rootId: buffer.rootId,
                path: buffer.path,
                name: buffer.name,
              },
              ev.clientX,
              ev.clientY,
            )
          : (cancelPointerChatAttachmentDrag(), false);
      if (claimedByChat) {
        if (nativePull) cancelNativePull(nativePull);
        return;
      }
      // Composed panes depend on this window's stage state.
      if (commit && pulledOff && buffer && !isComposedBufferKind(buffer.kind)) {
        const position = ev ? { x: ev.screenX, y: ev.screenY } : undefined;
        if (nativePull && position) {
          finishNativePull(nativePull, position);
          return;
        }
        void Promise.resolve(openInNewWindow()?.(key, position)).then(
          async () => {
            // Pull-off moves the source tab.
            const released = closeBuffer(key);
            if (!released || (await released) === "restored") return;
            scheduleSessionFidelityPersist();
            void onFileTabMovedOut()?.(key);
          },
          (err) => console.debug("[item-window] move file failed", err),
        );
        return;
      }
      if (nativePull) cancelNativePull(nativePull);
      if (!commit || !target) return;
      const order = state().order;
      const from = order.indexOf(key);
      let to = order.indexOf(target.key) + (target.after ? 1 : 0);
      if (from < 0 || to < 0) return;
      if (from < to) to -= 1;
      moveFilesBuffer(projectId(), key, to);
    };
    const up = (ev: PointerEvent) => finish(true, ev);
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

  const observeDrops = () => {
  onMount(() => {
    const tabsScrollerEl = scrollerElement();
    let disposed = false;
    let stopDragEvents: (() => void) | null = null;
    scheduleFileTabDropTargetSync();
    const observer =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(scheduleFileTabDropTargetSync);
    const stopLayoutSettle = onShellLayoutSettled(scheduleFileTabDropTargetSync);
    if (tabsScrollerEl) observer?.observe(tabsScrollerEl);
    window.addEventListener("resize", scheduleFileTabDropTargetSync);
    void listenFileTabWindowDrag((event) => {
      if (event.projectId !== projectId()) return;
      if (event.phase === "leave") {
        if (externalDrop()?.dragLabel === event.dragLabel)
          setExternalDrop(null);
        return;
      }
      const target = dropTargetFromPoint(localClientX(event.positionX));
      if (event.phase === "hover") {
        setExternalDrop({ dragLabel: event.dragLabel, target });
        return;
      }
      acceptExternalTabDrop(event, target);
      if (externalDrop()?.dragLabel === event.dragLabel) setExternalDrop(null);
    })
      .then((stop) => {
        if (disposed) stop();
        else {
          stopDragEvents = stop;
          fileTabDropReady = true;
          scheduleFileTabDropTargetSync();
        }
      })
      .catch((err) =>
        console.debug("[item-window] listen for tab drag failed", err),
      );
    onCleanup(() => {
      disposed = true;
      fileTabDropReady = false;
      fileTabDropTargetFingerprint = null;
      observer?.disconnect();
      stopLayoutSettle();
      window.removeEventListener("resize", scheduleFileTabDropTargetSync);
      stopDragEvents?.();
      setExternalDrop(null);
      void clearFileTabDropTarget().catch((err) =>
        console.debug("[item-window] clear tab drop target failed", err),
      );
    });
  });

  };
  return { dragKey, visibleDropAt, pullOffPreview, onTabPointerDown, observeDrops };
}
