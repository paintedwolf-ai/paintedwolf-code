import { Show, createResource, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { visualArtifactSrc } from "../../chat/visual/visual-artifact-src.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { LycaonApiError } from "../../api/http.ts";
import { artifactWasDeleted, useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";

type Props = {
  client: LycaonClient;
  sessionId: string;
  artifactId: string;
  label?: string;
};

/** A durable live-tool video artifact played beside its source transcript. */
export function LiveToolRecordingPlayer(props: Props) {
  return <Show when={!artifactWasDeleted(props.artifactId)} fallback={
    <p role="status" data-artifact-id={props.artifactId}>This artifact was deleted.</p>
  }><RecordingMedia {...props} /></Show>;
}

function RecordingMedia(props: Props) {
  const [mediaError, setMediaError] = createSignal(false);
  const [srcResource] = createResource(
    () => {
      const sessionId = props.sessionId.trim();
      const artifactId = props.artifactId.trim();
      return sessionId && artifactId ? `${sessionId}\0${artifactId}` : null;
    },
    async (key) => {
      setMediaError(false);
      const [sessionId = "", artifactId = ""] = key.split("\0");
      return visualArtifactSrc(props.client, sessionId, artifactId);
    },
  );
  // The resource accessor rethrows fetch errors; read its error first.
  useArtifactDeletion(() => props.artifactId, () => srcResource.error);
  const failed = () => srcResource.error !== undefined || mediaError();
  const src = () =>
    srcResource.error === undefined ? srcResource() : undefined;

  return (
    <section
      class="mt-den-4 mr-den-3 mb-den-5 w-[min(100%,32.5rem)] overflow-hidden rounded-den-lg border border-[var(--den-line)]"
      data-testid="live-tool-recording-player"
      aria-busy={!failed() && !src()}
    >
      <header
        class="flex justify-between px-den-5 py-den-3 text-den-compact text-den-text-muted"
        {...chromeProps()}
      >
        <span>{props.label?.trim() || "Recording of live session"}</span>
      </header>
      <Show
        when={failed()}
        fallback={
          <Show
            when={src()}
            fallback={
              <div
                class="aspect-[4/3] bg-den-tint-1"
                data-testid="live-tool-recording-placeholder"
                aria-hidden="true"
              />
            }
          >
            {(source) => (
              <video
                class="block aspect-[4/3] h-auto w-full bg-[var(--den-text)] object-contain"
                controls
                playsinline
                preload="metadata"
                src={source()}
                onError={() => setMediaError(true)}
              />
            )}
          </Show>
        }
      >
        <p
          class="m-0 px-den-5 pt-den-6 pb-den-7 text-den-compact text-den-text-muted"
          role="status"
        >
          {srcResource.error instanceof LycaonApiError && srcResource.error.code === "artifact_deleted"
            ? "This artifact was deleted." : "Recording could not be played."}
        </p>
      </Show>
    </section>
  );
}
