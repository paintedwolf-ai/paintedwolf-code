import { For, Show, createEffect, createMemo, createResource, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { VisualArtifact } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import { useArtifactDeletion } from "../../chat/visual/artifact-change-store.ts";
import { timelineCache } from "../../chat/visual/frame-archive-cache.ts";
import { frameIndexAt, timelineClock } from "../../chat/visual/timeline-archive.ts";
import { type TimelineMarker, timelineFacts, timelineMarkers, timelineMomentText } from "../../chat/visual/timeline-lanes.ts";
import { artifactFetchPath } from "../../chat/visual/visual-artifact-model.ts";
import { visualArtifactIntroDone } from "../../chat/visual/visual-artifact-reveal.ts";

type Props = {
  artifact: VisualArtifact;
  sessionId?: string | null;
  client?: LycaonClient | null;
  entryKey?: string;
  prominent?: boolean;
};

/** A recorded drive: the page at any moment, with its actions and events on lanes below it. */
export function TranscriptVisualTimeline(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey } : undefined,
  );
  const dedup = useArtifactDedup();
  const sessionId = () => props.sessionId?.trim() ?? "";
  const artifactId = () => props.artifact.id.trim();
  const skipIntro = visualArtifactIntroDone(artifactId());
  let scrubberEl: HTMLInputElement | undefined;
  // A reference elsewhere scrolls here, then lands keyboard focus on the scrubber.
  createEffect(() => {
    const id = artifactId();
    const key = props.entryKey?.trim() ?? "";
    if (!dedup || !id || !key) return;
    onCleanup(dedup.registerCanonical(id, key, () => scrubberEl?.focus({ preventScroll: true })));
  });

  const lease = createMemo(() => {
    const client = props.client;
    const sid = sessionId();
    const id = artifactId();
    if (!client || !sid || !id) return null;
    const acquired = timelineCache.acquire(client, sid, id);
    onCleanup(() => acquired.release());
    return acquired;
  });
  const [loaded] = createResource(lease, (current) => current.value ?? current.ready);
  useArtifactDeletion(artifactId, () => loaded.error);
  const recording = () => {
    if (loaded.error !== undefined) return undefined;
    loaded();
    return lease()?.value;
  };

  const duration = () => recording()?.manifest.duration_ms ?? 0;
  const [time, setTime] = createSignal(0);
  const [playing, setPlaying] = createSignal(false);
  const frame = createMemo(() => {
    const frames = recording()?.frames;
    return frames?.length ? frames[frameIndexAt(frames, time())] : undefined;
  });
  const markers = createMemo(() => {
    const manifest = recording()?.manifest;
    return manifest ? timelineMarkers(manifest) : [];
  });
  const facts = createMemo(() => {
    const manifest = recording()?.manifest;
    return manifest ? timelineFacts(manifest.summary) : [];
  });
  // The stored viewport reserves the frame's box before the archive decodes.
  const aspectRatio = () => {
    const viewport = recording()?.manifest.viewport;
    const width = viewport?.width || props.artifact.width || 0;
    const height = viewport?.height || props.artifact.height || 0;
    return width > 0 && height > 0 ? `${width} / ${height}` : undefined;
  };
  const fraction = (atMs: number) => (duration() > 0 ? Math.min(1, Math.max(0, atMs / duration())) : 0);
  // Lanes share the scrubber's travel, which stops half a thumb from each edge.
  const position = (atMs: number) => `calc(var(--timeline-thumb-half) + (100% - 2 * var(--timeline-thumb-half)) * ${fraction(atMs)})`;
  const span = (from: number, to: number) =>
    `max(4px, calc((100% - 2 * var(--timeline-thumb-half)) * ${fraction(to) - fraction(from)}))`;

  const seek = (atMs: number) => {
    setPlaying(false);
    setTime(Math.min(Math.max(0, atMs), duration()));
  };

  // Playback runs at recorded speed; a recording is short, so it stops at the end.
  createEffect(() => {
    if (!playing()) return;
    let last = performance.now();
    let handle = requestAnimationFrame(function step(now) {
      const next = time() + (now - last);
      last = now;
      if (next >= duration()) {
        setTime(duration());
        setPlaying(false);
        return;
      }
      setTime(next);
      handle = requestAnimationFrame(step);
    });
    onCleanup(() => cancelAnimationFrame(handle));
  });

  const togglePlay = () => {
    if (playing()) {
      setPlaying(false);
      return;
    }
    if (time() >= duration()) setTime(0);
    setPlaying(true);
  };

  // Arrow keys step between painted frames rather than milliseconds.
  const onScrubberKeyDown = (event: KeyboardEvent) => {
    const frames = recording()?.frames;
    if (!frames?.length || event.metaKey || event.ctrlKey || event.altKey) return;
    const current = frameIndexAt(frames, time());
    let next: number | undefined;
    if (event.key === "ArrowRight" || event.key === "ArrowUp") next = frames[Math.min(frames.length - 1, current + 1)]?.atMs;
    else if (event.key === "ArrowLeft" || event.key === "ArrowDown") next = frames[Math.max(0, current - 1)]?.atMs;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = duration();
    else if (event.key === " ") {
      event.preventDefault();
      togglePlay();
      return;
    }
    if (next === undefined) return;
    event.preventDefault();
    seek(next);
  };

  const markerLabel = (marker: TimelineMarker) => `${marker.label} at ${timelineClock(marker.atMs)}`;

  return (
    <div ref={bindTranscriptEntry} data-artifact-path={artifactFetchPath(sessionId(), artifactId())}>
      <figure
        class="den-transcript-visual den-transcript-visual--timeline"
        classList={{
          "den-transcript-visual--prominent": !!props.prominent,
          "den-transcript-visual--ready": !!recording(),
          "den-transcript-visual--instant": skipIntro,
        }}
        data-testid="transcript-visual-timeline"
        data-artifact-id={artifactId()}
        data-artifact-canonical={props.entryKey?.trim() || artifactId()}
      >
        <header class="den-transcript-visual-timeline__header">
          <span class="den-transcript-visual-timeline__title">{props.artifact.caption?.trim() || "Recording"}</span>
          <Show when={recording()}>
            {(rec) => (
              <span class="den-transcript-visual-timeline__count">
                {timelineClock(rec().manifest.duration_ms)} · {rec().frames.length} frames
              </span>
            )}
          </Show>
        </header>
        <Show
          when={loaded.error === undefined}
          fallback={
            <p class="den-transcript-visual-timeline__status" role="status" data-testid="transcript-visual-timeline-error">
              {loaded.error instanceof LycaonApiError && loaded.error.code === "artifact_deleted"
                ? "This artifact was deleted." : "This recording could not be loaded."}
            </p>
          }
        >
          <div
            class="den-transcript-visual__frame den-transcript-visual-timeline__frame"
            aria-busy={!recording()}
            style={{ "aspect-ratio": aspectRatio() }}
          >
            <Show when={frame()}>
              {(current) => (
                <img
                  decoding="async"
                  src={current().src}
                  alt={`Page at ${timelineClock(current().atMs)}`}
                  class="den-transcript-visual__img"
                  data-testid="transcript-visual-timeline-frame"
                />
              )}
            </Show>
          </div>
          <Show when={recording()}>
            {(rec) => (
              <>
                <div class="den-transcript-visual-timeline__controls">
                  <button
                    type="button"
                    class="den-transcript-visual-timeline__play den-inset-icon-btn"
                    aria-label={playing() ? "Pause recording" : "Play recording"}
                    aria-pressed={playing()}
                    onClick={togglePlay}
                    data-testid="transcript-visual-timeline-play"
                  >
                    <span aria-hidden="true">{playing() ? "❚❚" : "▶"}</span>
                  </button>
                  <span class="den-transcript-visual-timeline__clock">
                    {timelineClock(time())} / {timelineClock(duration())}
                  </span>
                </div>
                <div class="den-transcript-visual-timeline__track">
                  <input
                    ref={(el) => { scrubberEl = el; }}
                    type="range"
                    class="den-transcript-visual-timeline__scrubber"
                    min={0}
                    max={Math.max(1, Math.round(duration()))}
                    step={1}
                    value={Math.round(time())}
                    aria-label="Recording time"
                    aria-valuetext={timelineMomentText(rec().manifest, time())}
                    onInput={(event) => seek(Number(event.currentTarget.value))}
                    onKeyDown={onScrubberKeyDown}
                    data-testid="transcript-visual-timeline-scrubber"
                  />
                  <div class="den-transcript-visual-timeline__playhead" style={{ left: position(time()) }} aria-hidden="true" />
                  <For each={(["action", "page"] as const)}>
                    {(lane) => (
                      <div
                        class="den-transcript-visual-timeline__lane"
                        role="group"
                        aria-label={lane === "action" ? "Actions" : "Page events"}
                        data-lane={lane}
                      >
                        <For each={markers().filter((marker) => marker.lane === lane)}>
                          {(marker) => (
                            <button
                              type="button"
                              class="den-transcript-visual-timeline__marker"
                              classList={{ "den-transcript-visual-timeline__marker--span": marker.endMs !== undefined }}
                              data-tone={marker.tone}
                              style={{
                                left: position(marker.atMs),
                                width: marker.endMs !== undefined ? span(marker.atMs, marker.endMs) : undefined,
                              }}
                              aria-label={markerLabel(marker)}
                              data-tip={markerLabel(marker)}
                              onClick={() => seek(marker.atMs)}
                            />
                          )}
                        </For>
                      </div>
                    )}
                  </For>
                </div>
                <ul class="den-transcript-visual-timeline__facts" data-testid="transcript-visual-timeline-facts">
                  <For each={facts()}>
                    {(fact) => (
                      <li class="den-transcript-visual-timeline__fact" data-tone={fact.tone}>
                        <Show when={fact.atMs !== undefined} fallback={fact.text}>
                          <button
                            type="button"
                            class="den-transcript-visual-timeline__fact-seek"
                            aria-label={`${fact.text}, show ${timelineClock(fact.atMs ?? 0)}`}
                            onClick={() => seek(fact.atMs ?? 0)}
                          >
                            {fact.text}
                          </button>
                        </Show>
                      </li>
                    )}
                  </For>
                </ul>
              </>
            )}
          </Show>
        </Show>
      </figure>
    </div>
  );
}
