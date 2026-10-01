import { contextAction } from "../context-actions.ts";
import {
  Show,
  createEffect,
  createResource,
  createSignal,
  onCleanup,
} from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import type { VisualArtifact } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import {
  artifactFetchPath,
  isVisualVideoMime,
} from "../../chat/visual/visual-artifact-model.ts";
import {
  markVisualArtifactIntroDone,
  visualArtifactIntroDone,
} from "../../chat/visual/visual-artifact-reveal.ts";
import {
  visualArtifactSrc,
  visualArtifactSrcSync,
} from "../../chat/visual/visual-artifact-src.ts";
import { isFilmstripMime } from "../../chat/visual/filmstrip-zip.ts";
import { isTimelineMime } from "../../chat/visual/timeline-archive.ts";
import type { LycaonClient } from "../../api/client.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import { DenOverlay } from "../overlay/DenOverlay.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ContextMenu, type ContextMenuAnchor } from "../ContextMenu.tsx";
import { TranscriptVisualFilmstrip } from "./TranscriptVisualFilmstrip.tsx";
import { TranscriptVisualTimeline } from "./TranscriptVisualTimeline.tsx";
import { LiveToolRecordingPlayer } from "./LiveToolRecordingPlayer.tsx";
import { artifactWasDeleted, useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";
import { LycaonApiError } from "../../api/http.ts";

type Props = {
  artifact: VisualArtifact;
  sessionId?: string | null;
  projectId?: string;
  client?: LycaonClient | null;
  entryKey?: string;
  prominent?: boolean;
  toolOutput?: string | null;
};

export function TranscriptVisualArtifact(props: Props) {
  return <Show when={!artifactWasDeleted(props.artifact.id)} fallback={
    <p role="status" data-artifact-id={props.artifact.id}>This artifact was deleted.</p>
  }><TranscriptVisualMedia {...props} /></Show>;
}

function TranscriptVisualMedia(props: Props) {
  if (isFilmstripMime(props.artifact.mime)) {
    return (
      <TranscriptVisualFilmstrip
        artifact={props.artifact}
        sessionId={props.sessionId}
        client={props.client}
        entryKey={props.entryKey}
        prominent={props.prominent}
        toolOutput={props.toolOutput}
      />
    );
  }
  if (isTimelineMime(props.artifact.mime)) {
    return (
      <TranscriptVisualTimeline
        artifact={props.artifact}
        sessionId={props.sessionId}
        client={props.client}
        entryKey={props.entryKey}
        prominent={props.prominent}
      />
    );
  }
  if (isVisualVideoMime(props.artifact.mime)) {
    const sessionId = props.sessionId?.trim() ?? "";
    if (!props.client || !sessionId) return null;
    return (
      <LiveToolRecordingPlayer
        client={props.client}
        sessionId={sessionId}
        artifactId={props.artifact.id}
        label={props.artifact.caption}
      />
    );
  }
  return <TranscriptVisualRaster {...props} />;
}

function TranscriptVisualRaster(props: Props) {
  const chatDestination = useChatDestinationScope();
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined,
  );
  const dedup = useArtifactDedup();
  const [lightbox, setLightbox] = createSignal(false);
  const [menu, setMenu] = createSignal<ContextMenuAnchor | null>(null);
  const sessionId = () => props.sessionId?.trim() ?? "";
  const artifactId = () => props.artifact.id.trim();
  // Mount-time state controls the intro animation.
  const skipIntro = visualArtifactIntroDone(artifactId());
  const [revealed, setRevealed] = createSignal(skipIntro);
  // Host-stamped pixel size reserves the frame box before the img decodes.
  const reservedAspect = () => {
    const { width, height } = props.artifact;
    return width && height ? `${width} / ${height}` : undefined;
  };
  const caption = () => props.artifact.caption?.trim() || "";
  createEffect(() => {
    const id = artifactId();
    const key = props.entryKey?.trim() ?? "";
    if (!dedup || !id || !key) return;
    const unregister = dedup.registerCanonical(id, key, () => setLightbox(true));
    onCleanup(unregister);
  });
  // Stable keys and cached URLs preserve images across stream updates.
  const cachedSrc = visualArtifactSrcSync(sessionId(), artifactId());
  const [srcResource] = createResource(
    () => {
      const sid = sessionId();
      const id = artifactId();
      return props.client && sid && id ? `${sid}\0${id}` : null;
    },
    async (key) => {
      const client = props.client;
      if (!client) {
        throw new Error("artifact fetch missing client");
      }
      const [sid, id] = key.split("\0");
      if (!sid || !id) {
        throw new Error("artifact fetch missing session or id");
      }
      return visualArtifactSrc(client, sid, id);
    },
    cachedSrc === undefined ? {} : { initialValue: cachedSrc },
  );
  // Guard resource errors inside the card.
  useArtifactDeletion(artifactId, () => srcResource.error);
  const srcFailed = () => srcResource.error !== undefined;
  const src = () =>
    srcResource.error === undefined ? srcResource() : undefined;
  createEffect(() => {
    if (!src()) {
      if (!skipIntro) {
        setRevealed(false);
        setLightbox(false);
      }
    }
  });

  const markReady = () => {
    setRevealed(true);
    markVisualArtifactIntroDone(artifactId());
  };

  createEffect(() => {
    if (!lightbox()) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setLightbox(false);
      }
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => window.removeEventListener("keydown", onKey));
  });

  const onAddToChat = () => {
    const ref = chatRef();
    if (ref) void addToChat(ref, { destination: chatDestination() });
  };
  const chatRef = () => {
    const projectId = props.projectId?.trim() ?? "";
    const id = artifactId();
    if (!projectId || !id) return null;
    return {
      kind: "artifact" as const,
      projectId,
      artifactId: id,
      name: caption() || id,
      previewUrl: src() ?? undefined,
    };
  };

  const card = (
    <figure
      class="den-transcript-visual"
      classList={{
        "den-transcript-visual--prominent": !!props.prominent,
        "den-transcript-visual--ready": revealed(),
        "den-transcript-visual--instant": skipIntro,
      }}
      data-testid="transcript-visual-artifact"
      data-artifact-id={artifactId()}
      data-artifact-canonical={props.entryKey?.trim() || artifactId()}
      draggable={chatRef() ? true : undefined}
      onDragStart={(event) => startChatAttachmentDrag(event, chatRef())}
      onContextMenu={(e) => {
        const projectId = props.projectId?.trim() ?? "";
        if (!projectId || !props.sessionId?.trim() || !artifactId()) return;
        e.preventDefault();
        e.stopPropagation();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <button
        type="button"
        class="den-transcript-visual__frame"
        classList={{ "den-transcript-visual__frame--sized": !!reservedAspect() }}
        style={{ "aspect-ratio": reservedAspect() }}
        aria-label={caption() ? `Enlarge ${caption()}` : "Enlarge visual artifact"}
        aria-haspopup="dialog"
        aria-busy={!srcFailed() && !revealed()}
        disabled={!src() || !revealed()}
        onClick={() => setLightbox(true)}
      >
        <Show when={src()}>
          {(url) => (
            <img
              src={url()}
              alt={caption() || "Visual artifact"}
              class="den-transcript-visual__img"
              loading="lazy"
              decoding="async"
              width={props.artifact.width ?? undefined}
              height={props.artifact.height ?? undefined}
              style={{ "aspect-ratio": reservedAspect() }}
              ref={(el) => {
                if (el?.complete && el.naturalWidth > 0) markReady();
              }}
              onLoad={() => markReady()}
            />
          )}
        </Show>
      </button>
      <Show when={srcFailed()}>
        <p
          class="den-transcript-visual__caption"
          role="status"
          data-testid="transcript-visual-error"
        >
          {srcResource.error instanceof LycaonApiError && srcResource.error.code === "artifact_deleted"
            ? "This artifact was deleted." : "This visual could not be loaded."}
        </p>
      </Show>
      <Show when={caption()}>
        {(text) => (
          <figcaption class="den-transcript-visual__caption">{text()}</figcaption>
        )}
      </Show>
      <Show when={menu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={[
              contextAction("addToChat", {
                testId: "menu-add-to-chat",
                onSelect: onAddToChat,
              }),
            ]}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
      <Show when={lightbox() && src()}>
        {(url) => (
          <ResidentPortal mount={document.body}>
            <DenOverlay
              data-testid="transcript-visual-lightbox"
              onClick={() => setLightbox(false)}
            >
              <div
                class="den-overlay__panel"
                role="dialog"
                aria-modal="true"
                aria-label={caption() || "Visual artifact"}
                onClick={(event) => event.stopPropagation()}
              >
                <header class="den-overlay__header" {...chromeProps()}>
                  <span class="den-overlay__title">
                    {caption() || "Visual artifact"}
                  </span>
                  <ChromeCloseButton
                    class="den-overlay__close den-inset-icon-btn"
                    label="Close enlarged visual"
                    onClick={() => setLightbox(false)}
                  />
                </header>
                <Scrollport
                  class="den-overlay__body den-transcript-visual-lightbox__body"
                  contentClass="den-transcript-visual-lightbox__content"
                >
                  <img
                    src={url()}
                    alt={caption() || "Visual artifact"}
                    class="den-transcript-visual-lightbox__img"
                  />
                </Scrollport>
              </div>
            </DenOverlay>
          </ResidentPortal>
        )}
      </Show>
    </figure>
  );
  return (
    <div ref={bindTranscriptEntry} data-artifact-path={artifactFetchPath(sessionId(), artifactId())}>
      {card}
    </div>
  );
}
