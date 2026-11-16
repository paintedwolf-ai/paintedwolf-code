import {
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem } from "../../api/types.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import {
  artifactPreview,
  type ArtifactPreview,
} from "../../chat/visual/frame-archive-preview.ts";
import { isVisualVideoMime } from "../../chat/visual/visual-artifact-model.ts";
import { referenceImpactSentence } from "../../files/components/artifact-reference-impact.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { contextAction } from "../context-actions.ts";
import { ContextMenu, type ContextMenuAnchor } from "../ContextMenu.tsx";
import { artifactSourceLabel, shortChatLabel } from "./project-artifacts-filter.ts";

export type ArtifactGalleryTileProps = {
  item: ArtifactListItem;
  index: number;
  total: number;
  projectId: string;
  client: LycaonClient;
  onOpen: (index: number) => void;
  onSrc: (artifactId: string, src: string | null) => void;
  onRequestDelete: (item: ArtifactListItem) => void;
};

export function ArtifactGalleryTile(props: ArtifactGalleryTileProps) {
  const caption = () => props.item.caption?.trim() || "";
  const artifactId = () => props.item.id.trim();
  const isVideo = () => isVisualVideoMime(props.item.mime);
  let disposed = false;
  let mediaRequest = 0;
  let activePreview: ArtifactPreview | null = null;
  let reportedId = "";

  onCleanup(() => {
    disposed = true;
    mediaRequest++;
    if (activePreview) {
      activePreview.release();
      activePreview = null;
    }
    if (reportedId) {
      props.onSrc(reportedId, null);
      reportedId = "";
    }
  });

  const [revealed, setRevealed] = createSignal(false);
  const [menu, setMenu] = createSignal<ContextMenuAnchor | null>(null);
  const resourceKey = createMemo(() => {
    const id = artifactId();
    const sid = props.item.session_id.trim();
    const hash = props.item.content_hash?.trim() ?? "";
    return sid && id ? `${sid}\0${id}\0${hash}` : null;
  });

  const [blob] = createResource(resourceKey, async (key) => {
    const request = ++mediaRequest;
    const [sid = "", id = ""] = key.split("\0");
    if (!sid || !id) throw new Error("artifact fetch missing session or id");
    const body = await props.client.getSessionArtifact(sid, id);
    const preview = await artifactPreview(body);
    if (disposed || request !== mediaRequest) {
      preview.release();
      return null;
    }
    return preview;
  });

  const [src, setSrc] = createSignal<string | null>(null);
  const [decodeFailed, setDecodeFailed] = createSignal(false);

  createEffect(() => {
    const hasError = blob.error !== undefined;
    const loading = blob.loading;
    const next = hasError ? null : (blob() ?? null);
    if (hasError) setDecodeFailed(false);

    // Keep the current preview visible until a replacement settles or fails.
    if (loading && activePreview !== null && !hasError) {
      return;
    }
    if (next === activePreview) {
      return;
    }

    const previous = activePreview;
    activePreview = next;
    const id = untrack(artifactId);

    if (!next || !next.src) {
      next?.release();
      activePreview = null;
      setSrc(null);
      setRevealed(false);
      setDecodeFailed(!hasError && next !== null);
      if (reportedId || id) {
        props.onSrc(reportedId || id, null);
        reportedId = "";
      }
      previous?.release();
      return;
    }

    setDecodeFailed(false);
    if (previous === null) {
      setRevealed(false);
    }
    setSrc(next.src);
    reportedId = id;
    props.onSrc(id, next.src);
    previous?.release();
  });

  const markReady = () => setRevealed(true);
  const markDecodeFailed = () => {
    setDecodeFailed(true);
    setRevealed(false);
  };
  const createdLabel = () => {
    const ms = Date.parse(props.item.created_at);
    return Number.isFinite(ms) ? formatRelativeTime(ms) : "";
  };

  const chatRef = () => {
    const projectId = props.projectId.trim();
    const id = artifactId();
    if (!projectId || !id || isVideo()) return null;
    return {
      kind: "artifact" as const,
      projectId,
      artifactId: id,
      name: caption() || id,
      previewUrl: src() ?? undefined,
    };
  };

  const onAddToChat = () => {
    const ref = chatRef();
    if (ref) void addToChat(ref);
  };

  return (
    <figure
      class="den-transcript-visual project-artifacts-tile"
      classList={{ "den-transcript-visual--ready": revealed() }}
      data-testid="project-artifact-tile"
      data-artifact-id={artifactId()}
      data-artifact-source={props.item.source}
      draggable={chatRef() ? true : undefined}
      onDragStart={(event) => startChatAttachmentDrag(event, chatRef())}
      onContextMenu={(e) => {
        const projectId = props.projectId.trim();
        if (!projectId || !artifactId()) return;
        e.preventDefault();
        e.stopPropagation();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <button
        type="button"
        class="den-transcript-visual__frame"
        aria-label={
          caption()
            ? `Enlarge ${caption()}`
            : `Enlarge visual ${props.index + 1} of ${props.total}`
        }
        aria-haspopup="dialog"
        aria-busy={!revealed() && !blob.error && !decodeFailed()}
        disabled={!src() || !revealed()}
        onClick={() => props.onOpen(props.index)}
      >
        <Show when={src()}>
          {(url) => (
            <Show
              when={isVideo()}
              fallback={
                <img
                  src={url()}
                  alt={caption() || `Visual ${props.index + 1}`}
                  class="den-transcript-visual__img"
                  ref={(el) => {
                    if (el?.complete && el.naturalWidth > 0) markReady();
                  }}
                  onLoad={markReady}
                  onError={markDecodeFailed}
                />
              }
            >
              <video
                src={url()}
                class="den-transcript-visual__img"
                muted
                preload="metadata"
                onLoadedData={markReady}
                onError={markDecodeFailed}
              />
            </Show>
          )}
        </Show>
      </button>
      <Show when={blob.error || decodeFailed()}>
        <p role="status" data-testid="project-artifact-load-error">
          This artifact could not be loaded.
        </p>
      </Show>
      <figcaption class="project-artifacts-tile__meta">
        <Show when={caption()}>
          {(text) => (
            <span class="project-artifacts-tile__caption">{text()}</span>
          )}
        </Show>
        <span
          class="project-artifacts-tile__source"
          data-testid="project-artifact-source-badge"
        >
          {artifactSourceLabel(props.item.source)}
        </span>
        <span class="project-artifacts-tile__chat">
          {shortChatLabel(props.item.session_id)}
        </span>
        <Show when={createdLabel()}>
          {(label) => (
            <span class="project-artifacts-tile__when">{label()}</span>
          )}
        </Show>
      </figcaption>
      <Show when={menu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={[
              {
                label: caption() || "Artifact",
                header: true,
                description:
                  referenceImpactSentence(props.item) || "Nothing refers to it.",
                testId: "menu-artifact-header",
              },
              contextAction("addToChat", {
                testId: "menu-add-to-chat",
                disabled: isVideo(),
                onSelect: onAddToChat,
              }),
              { separator: true },
              contextAction("deleteArtifact", {
                testId: "menu-delete-artifact",
                onSelect: () => props.onRequestDelete(props.item),
              }),
            ]}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
    </figure>
  );
}
