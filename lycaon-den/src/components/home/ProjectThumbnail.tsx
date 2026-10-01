import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import { afterPaint } from "../../ui/surface-reveal.ts";

type Props = {
  seed: string;
  src?: string | null;
  /** True while a live cover may still arrive. */
  pending?: boolean;
};

/** Number of CSS-defined placeholder tint buckets. */
export const PROJECT_THUMB_TINT_COUNT = 5;

/** Deterministic tint bucket for a project seed. */
export function tintIndexFor(seed: string): number {
  let h = 0;
  for (let i = 0; i < seed.length; i += 1) {
    h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  }
  return h % PROJECT_THUMB_TINT_COUNT;
}

/** Reveals the placeholder or live cover after loading settles. */
export function ProjectThumbnail(props: Props) {
  const tint = () => tintIndexFor(props.seed);
  const widths = () => {
    let h = 0;
    for (let i = 0; i < props.seed.length; i += 1) h = (h * 17 + props.seed.charCodeAt(i)) >>> 0;
    return [62 + (h % 28), 84 - (h % 18), 70 + (h % 22)];
  };
  const [liveReady, setLiveReady] = createSignal(false);
  const [placeholderReady, setPlaceholderReady] = createSignal(false);
  let revealGen = 0;
  let cancelLiveReveal: (() => void) | undefined;

  const bumpReveal = () => {
    revealGen += 1;
    cancelLiveReveal?.();
    cancelLiveReveal = undefined;
    setLiveReady(false);
    return revealGen;
  };

  const scheduleLiveReveal = () => {
    const gen = revealGen;
    cancelLiveReveal?.();
    cancelLiveReveal = afterPaint(() => {
      if (gen === revealGen) setLiveReady(true);
    });
  };

  createEffect(() => {
    const src = props.src;
    const pending = props.pending === true;
    const gen = bumpReveal();

    if (src) {
      setPlaceholderReady(false);
      return;
    }
    if (pending) {
      setPlaceholderReady(false);
      return;
    }

    onCleanup(
      afterPaint(() => {
        if (gen === revealGen) setPlaceholderReady(true);
      }),
    );
  });

  onCleanup(() => {
    cancelLiveReveal?.();
  });

  const showLive = () => !!props.src && liveReady();

  return (
    <div
      class="project-thumb-stack"
      data-testid="project-thumbnail"
      data-pending={props.pending === true ? "true" : undefined}
      data-placeholder-ready={placeholderReady() ? "true" : undefined}
      data-live-ready={showLive() ? "true" : undefined}
    >
      <div
        class="project-thumb project-thumb--placeholder"
        classList={{
          "project-thumb--ready": placeholderReady() && !showLive(),
        }}
        data-tint={tint()}
        aria-hidden={showLive() ? "true" : undefined}
      >
        <div class="project-thumb__dots">
          <For each={[0, 1, 2]}>{() => <span />}</For>
        </div>
        <div class="project-thumb__bar" />
        <For each={widths()}>
          {(w) => <div class="project-thumb__line" style={{ width: `${w}%` }} />}
        </For>
      </div>
      <Show when={props.src}>
        {(src) => (
          <img
            class="project-thumb project-thumb--live"
            classList={{ "project-thumb--live-ready": liveReady() }}
            data-testid="project-thumbnail-live"
            src={src()}
            alt=""
            loading="lazy"
            ref={(el) => {
              if (el && el.complete && el.naturalWidth > 0) scheduleLiveReveal();
            }}
            onLoad={() => scheduleLiveReveal()}
          />
        )}
      </Show>
    </div>
  );
}
