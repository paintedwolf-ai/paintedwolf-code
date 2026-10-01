import { createPresentationWaiting } from "../../ui/presentation.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { useResidentLive } from "../../ui/resident-activity.ts";
import {
  Show,
  batch,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { copyPathMenuItems } from "../../components/copy-path-menu-items.ts";
import { bindChromeContextMenu } from "../../components/context-menu-open.ts";
import { BrowseSegmented } from "../../components/browse/BrowseSegmented.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../../components/ContextMenu.tsx";
import {
  CheckIcon,
  CopyIcon,
} from "../../components/source/viewer-icons.tsx";
import { formatSourceBytes } from "../../components/source/editor/source-editor-model.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { absolutePathForBuffer } from "../components/project-files-model.ts";
import { FilesPathCrumbs } from "./FilesPathCrumbs.tsx";

type Props = {
  projectId: string;
  sessionId?: string;
  buffer: FileBuffer;
  roots: ProjectRoot[];
  client?: LycaonClient | null;
  onInnerLayerChange: (open: boolean) => void;
  onRevealSegment: (rootId: string, path: string, isDir: boolean) => void;
};

type ZoomMode = "fit" | "actual";

const ZOOM_SESSION_KEY = "den.files.imageZoomMode";

function readZoomMode(): ZoomMode {
  try {
    const raw = sessionStorage.getItem(ZOOM_SESSION_KEY);
    return raw === "actual" ? "actual" : "fit";
  } catch {
    return "fit";
  }
}

function writeZoomMode(mode: ZoomMode): void {
  try {
    sessionStorage.setItem(ZOOM_SESSION_KEY, mode);
  } catch {
    /* sessionStorage unavailable */
  }
}

export function FilesImageViewer(props: Props) {
  const live = useResidentLive();
  const [copiedPath, setCopiedPath] = createSignal(false);
  const [toolbarMenu, setToolbarMenu] = createSignal<{
    anchor: ContextMenuAnchor;
  } | null>(null);
  const [zoomMode, setZoomMode] = createSignal<ZoomMode>(readZoomMode());
  const [blobUrl, setBlobUrl] = createSignal<string | null>(null);
  const [loadError, setLoadError] = createSignal<string | null>(null);
  const [loadingRaw, setLoadingRaw] = createSignal(false);
  const [dimensions, setDimensions] = createSignal<{ w: number; h: number } | null>(
    null,
  );
  let stageEl: HTMLDivElement | undefined;
  const showLoading = createPresentationWaiting(() => !blobUrl() && (props.buffer.loading || loadingRaw()));
  observeSurfaceFailure(
    { code: "files_image_unavailable", title: "Could not show the image", suggestedAction: "Reopen the image from the file tree once the file is readable." },
    () => props.buffer.loadError || loadError(),
    () => props.projectId,
  );
  usePresentationParticipant("file-image", () => Boolean(loadError() || props.buffer.loadError || !props.client || dimensions()));

  createEffect(() => {
    props.onInnerLayerChange(toolbarMenu() != null);
  });

  const root = () => props.roots.find((r) => r.id === props.buffer.rootId);
  const absolutePath = (): string | undefined => {
    const r = root();
    return r ? absolutePathForBuffer(r.path, props.buffer.path) : undefined;
  };

  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  let stopDrag: (() => void) | undefined;
  onCleanup(() => {
    stopDrag?.();
    if (copiedTimer) clearTimeout(copiedTimer);
    const url = blobUrl();
    if (url) URL.revokeObjectURL(url);
  });

  const source = createMemo(() => {
    const buf = props.buffer;
    const c = props.client;
    if (buf.loading || !c) return null;
    // A successful source refresh can replace bytes at the same path.
    void buf.editRevision;
    return {
      client: c, projectId: props.projectId, path: buf.path,
      rootId: buf.rootId || undefined, jobId: buf.jobId, sessionId: props.sessionId,
    };
  });
  let loadedSource: ReturnType<typeof source> | undefined;

  createEffect(() => {
    if (!live()) return;
    const request = source();
    if (!request || request === loadedSource) return;
    let cancelled = false;
    let candidateUrl: string | undefined;
    const abort = new AbortController();
    setLoadingRaw(true);
    setLoadError(null);
    void request.client.getProjectSourceRaw(request.projectId, request.path, {
      rootId: request.rootId,
      workerId: request.jobId,
      sessionId: request.sessionId,
      signal: abort.signal,
    })
      .then((blob) => {
        if (cancelled) return;
        const url = URL.createObjectURL(blob);
        candidateUrl = url;
        const img = new Image();
        img.decoding = "async";
        const publish = () => {
          if (cancelled) return;
          loadedSource = request;
          batch(() => {
            setBlobUrl((prev) => {
              if (prev) URL.revokeObjectURL(prev);
              return url;
            });
            candidateUrl = undefined;
            setDimensions({ w: img.naturalWidth, h: img.naturalHeight });
            setLoadingRaw(false);
          });
        };
        const failed = () => {
          if (cancelled) return;
          URL.revokeObjectURL(url);
          candidateUrl = undefined;
          setLoadingRaw(false);
          setLoadError("Failed to decode image.");
        };
        img.onload = () => {
          void img.decode().then(publish, failed);
        };
        img.onerror = failed;
        img.src = url;
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setLoadingRaw(false);
        setLoadError(
          err instanceof Error ? err.message : "Failed to load image.",
        );
      });
    onCleanup(() => {
      cancelled = true;
      abort.abort();
      if (candidateUrl) URL.revokeObjectURL(candidateUrl);
    });
  });

  const flashCopied = () => {
    setCopiedPath(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => setCopiedPath(false), 1400);
  };

  const onCopyPath = () => {
    const abs = absolutePath();
    if (!abs) return;
    void copyTextToClipboard(abs);
    flashCopied();
  };

  const onCopyRelativePath = () => {
    void copyTextToClipboard(props.buffer.path);
    flashCopied();
  };

  const onCopyFileName = () => {
    void copyTextToClipboard(props.buffer.name);
    flashCopied();
  };

  const copyMenuItems = (): ContextMenuItem[] =>
    copyPathMenuItems({
      copyAbsolute: absolutePath() ? onCopyPath : null,
      copyRelative: onCopyRelativePath,
      copyFileName: onCopyFileName,
      absoluteTestId: "files-image-copy-path",
      relativeTestId: "files-image-copy-relative-path",
      fileNameTestId: "files-image-copy-file-name",
    });

  const copyMenuBind = bindChromeContextMenu((anchor) =>
    setToolbarMenu({ anchor }),
  );
  const selectZoom = (mode: ZoomMode) => {
    setZoomMode(mode);
    writeZoomMode(mode);
  };

  const onStageKeyDown = (e: KeyboardEvent) => {
    if (zoomMode() !== "actual" || !stageEl) return;
    const step = 40;
    let handled = true;
    switch (e.key) {
      case "ArrowLeft":
        stageEl.scrollLeft -= step;
        break;
      case "ArrowRight":
        stageEl.scrollLeft += step;
        break;
      case "ArrowUp":
        stageEl.scrollTop -= step;
        break;
      case "ArrowDown":
        stageEl.scrollTop += step;
        break;
      case "Home":
        stageEl.scrollLeft = 0;
        stageEl.scrollTop = 0;
        break;
      case "End":
        stageEl.scrollLeft = stageEl.scrollWidth;
        stageEl.scrollTop = stageEl.scrollHeight;
        break;
      case "PageUp":
        stageEl.scrollTop -= stageEl.clientHeight;
        break;
      case "PageDown":
        stageEl.scrollTop += stageEl.clientHeight;
        break;
      default:
        handled = false;
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  const onStageDoubleClick = (e: MouseEvent) => {
    e.preventDefault();
    if (zoomMode() === "actual") {
      selectZoom("fit");
    } else {
      selectZoom("actual");
    }
  };

  const onStageMouseDown = (e: MouseEvent) => {
    if (zoomMode() !== "actual") return;
    if (e.button !== 0) return;
    stopDrag?.();
    const startX = e.clientX;
    const startY = e.clientY;
    const startScrollLeft = stageEl?.scrollLeft ?? 0;
    const startScrollTop = stageEl?.scrollTop ?? 0;
    const target = e.currentTarget as HTMLDivElement;
    target.classList.add("den-files-image__stage--dragging");

    const onMove = (moveEvent: MouseEvent) => {
      if (!stageEl) return;
      stageEl.scrollLeft = startScrollLeft - (moveEvent.clientX - startX);
      stageEl.scrollTop = startScrollTop - (moveEvent.clientY - startY);
    };

    const onUp = () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      target.classList.remove("den-files-image__stage--dragging");
      stopDrag = undefined;
    };

    stopDrag = onUp;
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  };

  const statusLabel = createMemo(() => {
    const dim = dimensions();
    const size = formatSourceBytes(props.buffer.sizeBytes);
    if (dim) return `${dim.w}×${dim.h} · ${size}`;
    return size;
  });

  return (
    <div
      class="den-files-image"
      data-testid="files-image-viewer"
      data-files-ctx="image-viewer"
    >
      <div class="den-files-toolbar">
        <FilesPathCrumbs
          rootId={props.buffer.rootId}
          rootLabel={props.buffer.rootLabel}
          path={props.buffer.path}
          onRevealSegment={props.onRevealSegment}
        />
        <span class="den-files-editor__toolbar-actions">
          <BrowseSegmented
            class="den-files-editor__seg den-files-image__zoom"
            ariaLabel="Image zoom"
            value={zoomMode()}
            onChange={(id) => {
              if (id === "fit" || id === "actual") selectZoom(id);
            }}
            options={[
              { id: "fit", label: "Fit", testId: "files-image-zoom-fit" },
              { id: "actual", label: "100%", testId: "files-image-zoom-actual" },
            ]}
          />
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            classList={{ "den-files-editor__tool--ok": copiedPath() }}
            data-testid="files-image-copy-toolbar"
            data-tip="Copy path"
            data-tip-pos="below"
            aria-label="Copy path"
            aria-haspopup="menu"
            aria-expanded={toolbarMenu() != null}
            onClick={onCopyPath}
            onContextMenu={copyMenuBind.onContextMenu}
            onKeyDown={copyMenuBind.onKeyDown}
          >
            <Show when={copiedPath()} fallback={<CopyIcon />}>
              <CheckIcon />
            </Show>
          </button>
        </span>
      </div>
      <Show when={showLoading()}>
        <p class="den-files-editor__notice" data-testid="files-image-loading">
          Loading…
        </p>
      </Show>
      <Scrollport
        class="den-files-image__stage"
        classList={{
          "den-files-image__stage--fit": zoomMode() === "fit",
          "den-files-image__stage--actual": zoomMode() === "actual",
        }}
        axis="both"
        contentClass="den-files-image__canvas"
        viewportRef={(el) => { stageEl = el; }}
        viewport={{
          tabIndex: 0,
          role: "img",
          "aria-label": props.buffer.name,
          onKeyDown: onStageKeyDown,
        }}
        data-testid="files-image-stage"
        onMouseDown={onStageMouseDown}
        onDblClick={onStageDoubleClick}
      >
        <Show when={blobUrl()}>
          {(url) => (
            <img
              class="den-files-image__img"
              src={url()}
              alt={props.buffer.name}
              decoding="async"
              draggable={false}
              width={dimensions()?.w}
              height={dimensions()?.h}
              data-testid="files-image-img"
            />
          )}
        </Show>
      </Scrollport>
      <div class="den-files-editor__status den-files-image__status" data-files-ctx="no-menu">
        <span data-testid="files-image-status">{statusLabel()}</span>
      </div>
      <Show when={toolbarMenu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={copyMenuItems()}
            onDismiss={() => setToolbarMenu(null)}
          />
        )}
      </Show>
    </div>
  );
}
