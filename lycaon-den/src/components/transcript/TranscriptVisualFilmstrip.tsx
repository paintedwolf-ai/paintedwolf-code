import { For, Show, createEffect, createMemo, createResource, createSignal, onCleanup } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import type { VisualArtifact } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import { artifactFetchPath } from "../../chat/visual/visual-artifact-model.ts";
import {
  markVisualArtifactIntroDone,
  visualArtifactIntroDone,
} from "../../chat/visual/visual-artifact-reveal.ts";
import {
  type FilmstripFrame,
} from "../../chat/visual/filmstrip-zip.ts";
import { filmstripCache } from "../../chat/visual/frame-archive-cache.ts";
import { filmstripStepsFromToolOutput } from "../../chat/visual/filmstrip-steps.ts";
import type { LycaonClient } from "../../api/client.ts";
import { DenOverlay } from "../overlay/DenOverlay.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { LycaonApiError } from "../../api/http.ts";
import { useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";

type Props = {
  artifact: VisualArtifact;
  sessionId?: string | null;
  client?: LycaonClient | null;
  entryKey?: string;
  prominent?: boolean;
  /** The containing gallery already provides the enlarged viewer. */
  enlarged?: boolean;
  /** Tool result text used for step labels and details. */
  toolOutput?: string | null;
};

export function TranscriptVisualFilmstrip(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined,
  );
  const dedup = useArtifactDedup();
  const [index, setIndex] = createSignal(0);
  const [lightbox, setLightbox] = createSignal(false);
  let railEl: HTMLDivElement | undefined;
  const sessionId = () => props.sessionId?.trim() ?? "";
  const artifactId = () => props.artifact.id.trim();
  const stripCaption = () => props.artifact.caption?.trim() || "";
  const skipIntro = visualArtifactIntroDone(artifactId());
  createEffect(() => {
    const id = artifactId();
    const key = props.entryKey?.trim() ?? "";
    if (!dedup || !id || !key) return;
    const unregister = dedup.registerCanonical(id, key, () => setLightbox(true));
    onCleanup(unregister);
  });

  const media = createMemo(() => {
    const client = props.client;
    const sid = sessionId();
    const id = artifactId();
    if (!client || !sid || !id) return null;
    const lease = filmstripCache.acquire(client, sid, id);
    onCleanup(() => lease.release());
    return lease;
  });
  const [framesResource] = createResource(media, (lease) => lease.value ?? lease.ready);
  // Resource failures stay inside the artifact card.
  useArtifactDeletion(artifactId, () => framesResource.error);
  const framesFailed = () => framesResource.error !== undefined;
  const frames = () => {
    if (framesResource.error !== undefined) return undefined;
    // Resource completion triggers a read of the current lease's frames.
    framesResource();
    return media()?.value;
  };

  const steps = createMemo(() => {
    const list = frames() ?? [];
    return filmstripStepsFromToolOutput(
      props.toolOutput,
      list.length,
      list.map((fr) => fr.caption),
    );
  });

  const activeStep = createMemo(() => {
    const list = steps();
    if (!list.length) return null;
    return list[Math.min(index(), list.length - 1)] ?? null;
  });

  createEffect(() => {
    const list = frames();
    if (!list?.length) return;
    if (index() >= list.length) setIndex(0);
  });

  createEffect(() => {
    if (!lightbox()) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setLightbox(false);
        return;
      }
      const list = frames();
      if (!list?.length) return;
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        setIndex((i) => (i - 1 + list.length) % list.length);
      }
      if (event.key === "ArrowRight") {
        event.preventDefault();
        setIndex((i) => (i + 1) % list.length);
      }
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => window.removeEventListener("keydown", onKey));
  });

  const active = (): FilmstripFrame | null => {
    const list = frames();
    if (!list?.length) return null;
    return list[Math.min(index(), list.length - 1)] ?? null;
  };

  createEffect(() => {
    if ((frames()?.length ?? 0) > 0) {
      markVisualArtifactIntroDone(artifactId());
    }
  });

  const frameLabel = (fr: FilmstripFrame) => {
    const step = steps()[fr.index];
    return step?.label || fr.caption || `Frame ${fr.index + 1}`;
  };

  const moveRailSelection = (next: number) => {
    const count = frames()?.length ?? 0;
    if (count === 0) return;
    const clamped = Math.min(Math.max(next, 0), count - 1);
    setIndex(clamped);
    queueMicrotask(() => {
      railEl
        ?.querySelector<HTMLElement>(`[data-filmstrip-index="${clamped}"]`)
        ?.focus();
    });
  };

  const onRailKeyDown = (event: KeyboardEvent) => {
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    const count = frames()?.length ?? 0;
    if (count === 0) return;
    let next = -1;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      next = (index() + 1) % count;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      next = (index() - 1 + count) % count;
    } else if (event.key === "Home") {
      next = 0;
    } else if (event.key === "End") {
      next = count - 1;
    }
    if (next < 0) return;
    event.preventDefault();
    moveRailSelection(next);
  };

  const card = (
    <figure
      class="den-transcript-visual den-transcript-visual--filmstrip"
      classList={{
        "den-transcript-visual--prominent": !!props.prominent,
        "den-transcript-visual--ready": !!frames()?.length,
        "den-transcript-visual--instant": skipIntro,
      }}
      data-testid="transcript-visual-filmstrip"
      data-artifact-id={artifactId()}
      data-artifact-canonical={props.entryKey?.trim() || artifactId()}
    >
      <Show when={stripCaption()}>
        {(text) => (
          <header class="den-transcript-visual-filmstrip__header">
            <span class="den-transcript-visual-filmstrip__title">{text()}</span>
            <Show when={(frames()?.length ?? 0) > 0}>
              <span class="den-transcript-visual-filmstrip__count">
                {frames()?.length} steps
              </span>
            </Show>
          </header>
        )}
      </Show>
      <div class="den-transcript-visual-filmstrip__stage">
        <Show
          when={!framesFailed()}
          fallback={
            <p
              class="den-transcript-visual-filmstrip__step-detail"
              role="status"
              data-testid="transcript-visual-filmstrip-error"
            >
              {framesResource.error instanceof LycaonApiError && framesResource.error.code === "artifact_deleted"
                ? "This artifact was deleted." : "These capture steps could not be loaded."}
            </p>
          }
        >
          <Show when={!props.enlarged} fallback={
            <div class="den-transcript-visual__frame den-transcript-visual-filmstrip__hero">
              <Show when={active()}>
                {(fr) => <img decoding="async" src={fr().src} alt={frameLabel(fr())} class="den-transcript-visual__img" />}
              </Show>
            </div>
          }>
          <button
            type="button"
            class="den-transcript-visual__frame den-transcript-visual-filmstrip__hero"
            aria-label={(() => {
              const fr = active();
              return fr ? `Enlarge ${frameLabel(fr)}` : "Enlarge filmstrip";
            })()}
            aria-haspopup="dialog"
            aria-busy={!frames()?.length}
            disabled={!active()}
            onClick={() => setLightbox(true)}
          >
            <Show when={active()}>
              {(fr) => (
                <img
                  decoding="async"
                  src={fr().src}
                  alt={frameLabel(fr())}
                  class="den-transcript-visual__img"
                />
              )}
            </Show>
          </button>
          </Show>
        </Show>
        <Show when={activeStep()}>
          {(step) => (
            <aside
              class="den-transcript-visual-filmstrip__reading"
              data-testid="transcript-visual-filmstrip-reading"
            >
              <div class="den-transcript-visual-filmstrip__step-index">
                Step {step().index + 1}
                <Show when={step().evidenceHandle}>
                  {(h) => (
                    <code class="den-transcript-visual-filmstrip__handle">{h()}</code>
                  )}
                </Show>
              </div>
              <div class="den-transcript-visual-filmstrip__step-label">
                {step().label}
              </div>
              <p class="den-transcript-visual-filmstrip__step-detail">
                {step().detail}
              </p>
            </aside>
          )}
        </Show>
      </div>
      <Show when={(frames()?.length ?? 0) > 1}>
        <Scrollport
          ref={(el) => { railEl = el; }}
          class="den-transcript-visual-filmstrip__rail"
          axis="x"
          contentClass="den-transcript-visual-filmstrip__rail-content"
          content={{
            role: "listbox",
            "aria-orientation": "horizontal",
            "aria-label": "Filmstrip steps",
            onKeyDown: onRailKeyDown,
          }}
          data-testid="transcript-visual-filmstrip-rail"
        >
          <For each={frames() ?? []}>
            {(fr, i) => {
              const step = () => steps()[i()];
              return (
                <button
                  type="button"
                  role="option"
                  tabindex={i() === index() ? 0 : -1}
                  data-filmstrip-index={i()}
                  class="den-transcript-visual-filmstrip__step"
                  classList={{
                    "den-transcript-visual-filmstrip__step--active":
                      i() === index(),
                  }}
                  aria-selected={i() === index()}
                  aria-label={step()?.label || frameLabel(fr)}
                  onClick={() => setIndex(i())}
                >
                  <span class="den-transcript-visual-filmstrip__step-num">
                    {i() + 1}
                  </span>
                  <span class="den-transcript-visual-filmstrip__step-thumb">
                    <img src={fr.src} alt="" loading="lazy" decoding="async" />
                  </span>
                  <span class="den-transcript-visual-filmstrip__step-cap">
                    {step()?.label || frameLabel(fr)}
                  </span>
                </button>
              );
            }}
          </For>
        </Scrollport>
      </Show>
      <Show when={lightbox() && active()}>
        {(fr) => (
          <ResidentPortal mount={document.body}>
            <DenOverlay
              data-testid="transcript-visual-filmstrip-lightbox"
              onClick={() => setLightbox(false)}
            >
              <div
                class="den-overlay__panel"
                role="dialog"
                aria-modal="true"
                aria-label={frameLabel(fr())}
                onClick={(event) => event.stopPropagation()}
              >
                <header class="den-overlay__header" {...chromeProps()}>
                  <span class="den-overlay__title">{frameLabel(fr())}</span>
                  <span class="den-transcript-visual-filmstrip__counter">
                    {index() + 1} / {frames()?.length ?? 0}
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
                  <button
                    type="button"
                    class="den-transcript-visual-lightbox__nav"
                    aria-label="Previous frame"
                    disabled={(frames()?.length ?? 0) <= 1}
                    onClick={() =>
                      setIndex(
                        (i) =>
                          (i - 1 + (frames()?.length ?? 1)) %
                          (frames()?.length ?? 1),
                      )
                    }
                  >
                    ‹
                  </button>
                  <img
                    decoding="async"
                  src={fr().src}
                    alt={frameLabel(fr())}
                    class="den-transcript-visual-lightbox__img"
                  />
                  <button
                    type="button"
                    class="den-transcript-visual-lightbox__nav"
                    aria-label="Next frame"
                    disabled={(frames()?.length ?? 0) <= 1}
                    onClick={() =>
                      setIndex((i) => (i + 1) % (frames()?.length ?? 1))
                    }
                  >
                    ›
                  </button>
                </Scrollport>
                <Show when={activeStep()?.detail}>
                  {(detail) => (
                    <p class="den-transcript-visual-filmstrip__lightbox-detail">
                      {detail()}
                    </p>
                  )}
                </Show>
              </div>
            </DenOverlay>
          </ResidentPortal>
        )}
      </Show>
    </figure>
  );

  return (
    <div
      ref={bindTranscriptEntry}
      data-artifact-path={artifactFetchPath(sessionId(), artifactId())}
    >
      {card}
    </div>
  );
}
