import { Show, createEffect, createResource, createSignal, onCleanup } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import type { LycaonClient } from "../../api/client.ts";
import { isVisualVideoMime } from "../../chat/visual/visual-artifact-model.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { DenOverlay } from "../overlay/DenOverlay.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { DenRadioControl } from "../primitives/DenRadio.tsx";
import { artifactWasDeleted, useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";
import { LycaonApiError } from "../../api/http.ts";

/** Renders an ask artifact preview. */
export function FeedbackArtifactThumb(props: {
  client: LycaonClient;
  sessionId: string;
  artifactId: string;
  /** Visible A/B badge; also names the radio. */
  label?: string;
  /** Accessible name for the radio on thumbs that show no badge. */
  choiceLabel?: string;
  selectable?: boolean;
  selected?: boolean;
  disabled?: boolean;
  radioName?: string;
  onSelect?: () => void;
}) {
  const [lightbox, setLightbox] = createSignal(false);
  const [blobResource] = createResource(
    () => artifactWasDeleted(props.artifactId) ? null : `${props.sessionId}\0${props.artifactId}`,
    async (key) => {
      const [sid, id] = key.split("\0");
      if (!sid || !id) throw new Error("artifact fetch missing session or id");
      return props.client.getSessionArtifact(sid, id);
    },
  );
  // Reading the accessor rethrows a failed fetch; the guard keeps it in the
  // thumb instead of escaping to the stage boundary.
  useArtifactDeletion(() => props.artifactId, () => blobResource.error);
  const blobFailed = () => blobResource.error !== undefined;
  const blob = () =>
    !artifactWasDeleted(props.artifactId) && blobResource.error === undefined ? blobResource() : undefined;
  const [src, setSrc] = createSignal<string | null>(null);
  createEffect(() => {
    const b = blob();
    if (!b) {
      setSrc(null);
      return;
    }
    const url = URL.createObjectURL(b);
    setSrc(url);
    onCleanup(() => URL.revokeObjectURL(url));
  });
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

  const title = () => (props.label ? `Variant ${props.label}` : "Attached visual");
  const isVideo = () => isVisualVideoMime(blob()?.type);
  const fallback = () => {
    if (artifactWasDeleted(props.artifactId) ||
      (blobResource.error instanceof LycaonApiError && blobResource.error.code === "artifact_deleted")) {
      return <p class="den-workflow-feedback-card-artifact-loading" role="status">This artifact was deleted.</p>;
    }
    if (blobFailed()) {
      return <p class="den-workflow-feedback-card-artifact-loading" role="status">Preview unavailable.</p>;
    }
    return <p class="den-workflow-feedback-card-artifact-loading" aria-busy="true">Loading preview…</p>;
  };

  return (
    <div
      class="den-workflow-feedback-card-artifact"
      classList={{
        "den-workflow-feedback-card-artifact--selectable": props.selectable,
        "den-workflow-feedback-card-artifact--selected": props.selectable && props.selected,
      }}
      data-testid="workflow-feedback-artifact"
      data-artifact-id={props.artifactId}
      data-compare-label={props.label || undefined}
      data-selected={props.selectable && props.selected ? "" : undefined}
    >
      <Show
        when={props.selectable}
        fallback={
          <>
            <Show when={props.label}>
              {(label) => (
                <p class="den-workflow-feedback-card-compare-label" data-testid="workflow-feedback-compare-label">
                  {label()}
                </p>
              )}
            </Show>
            <Show when={src()} fallback={fallback()}>
              {(url) => (
                <button
                  type="button"
                  class="den-workflow-feedback-card-artifact-frame"
                  aria-label={`Enlarge ${title()}`}
                  aria-haspopup="dialog"
                  onClick={() => setLightbox(true)}
                >
                  <Show
                    when={isVideo()}
                    fallback={
                      <img src={url()} alt={title()} class="den-workflow-feedback-card-artifact-img" />
                    }
                  >
                    <video
                      src={url()}
                      class="den-workflow-feedback-card-artifact-img"
                      muted
                      preload="metadata"
                    />
                  </Show>
                </button>
              )}
            </Show>
          </>
        }
      >
        <label class="den-workflow-feedback-card-artifact-choice">
          <DenRadioControl
            class="den-workflow-feedback-card-artifact-radio"
            name={props.radioName}
            aria-label={props.label ?? props.choiceLabel}
            checked={props.selected}
            disabled={props.disabled}
            onChange={() => props.onSelect?.()}
          />
          <span class="den-workflow-feedback-card-artifact-frame">
            <Show when={src()} fallback={fallback()}>
              {(url) => (
                <Show
                  when={isVideo()}
                  fallback={<img src={url()} alt="" class="den-workflow-feedback-card-artifact-img" />}
                >
                  <video
                    src={url()}
                    class="den-workflow-feedback-card-artifact-img"
                    muted
                    preload="metadata"
                  />
                </Show>
              )}
            </Show>
            <Show when={props.label}>
              {(label) => (
                <span
                  class="den-workflow-feedback-card-artifact-badge"
                  data-testid="workflow-feedback-compare-label"
                  aria-hidden="true"
                >
                  {label()}
                </span>
              )}
            </Show>
          </span>
        </label>
        <Show when={src()}>
          <button
            type="button"
            class="den-workflow-feedback-card-artifact-zoom"
            aria-label={`Enlarge ${title()}`}
            aria-haspopup="dialog"
            onClick={() => setLightbox(true)}
          >
            ⤢
          </button>
        </Show>
      </Show>
      <Show when={lightbox() && src()}>
        {(url) => (
          <ResidentPortal mount={document.body}>
            <DenOverlay
              data-testid="workflow-feedback-artifact-lightbox"
              onClick={() => setLightbox(false)}
            >
              <div
                class="den-overlay__panel"
                role="dialog"
                aria-modal="true"
                aria-label={title()}
                onClick={(event) => event.stopPropagation()}
              >
                <header class="den-overlay__header" {...chromeProps()}>
                  <span class="den-overlay__title">{title()}</span>
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
                  <Show
                    when={isVideo()}
                    fallback={
                      <img
                        src={url()}
                        alt={title()}
                        class="den-transcript-visual-lightbox__img"
                      />
                    }
                  >
                    <video
                      src={url()}
                      class="den-transcript-visual-lightbox__img"
                      controls
                      preload="metadata"
                    />
                  </Show>
                </Scrollport>
              </div>
            </DenOverlay>
          </ResidentPortal>
        )}
      </Show>
    </div>
  );
}
