import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  on,
  onCleanup,
  onMount,
  type Accessor,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import {
  acquirePreviewWatch,
  frameSrc,
  subscribeLivePreviewStore,
  type LivePreviewSnapshot,
} from "../../chat/visual/preview-store.ts";

type Props = {
  sessionId: string;
  client: LycaonClient;
  snapshot: Accessor<LivePreviewSnapshot | undefined>;
  /** Compact embed for ask_user review cards. */
  compact?: boolean;
};

/** Read-only preview for one invocation-scoped page. */
export function LivePreviewPane(props: Props) {
  const [tick, setTick] = createSignal(0);
  const [paneVisible, setPaneVisible] = createSignal(true);
  let rootEl: HTMLElement | undefined;

  onMount(() => {
    const unsub = subscribeLivePreviewStore(() => setTick((n) => n + 1));
    onCleanup(unsub);
    if (typeof IntersectionObserver === "undefined" || !rootEl) return;
    const io = new IntersectionObserver(
      (entries) => {
        const hit = entries.some((e) => e.isIntersecting);
        setPaneVisible(hit);
      },
      { threshold: 0.05 },
    );
    io.observe(rootEl);
    onCleanup(() => io.disconnect());
  });

  const snap = createMemo((): LivePreviewSnapshot | undefined => {
    tick();
    return props.snapshot();
  });

  const live = () => !!snap()?.live;
  const src = () => frameSrc(snap());
  const action = () => snap()?.action ?? null;

  // Preserve the watch across frame object replacements.
  const watchKey = createMemo(() => {
    const sid = props.sessionId.trim();
    const pageId = snap()?.pageId?.trim() ?? "";
    if (!sid || !pageId || !paneVisible() || !live()) return "";
    return `${sid}\u0000${pageId}`;
  });
  createEffect(
    on(watchKey, (key) => {
      if (!key) return;
      const sep = key.indexOf("\u0000");
      const sid = key.slice(0, sep);
      const pageId = key.slice(sep + 1);
      onCleanup(acquirePreviewWatch(props.client, sid, pageId));
    }),
  );

  const paintSrc = src;
  const status = () => {
    if (live() && src()) {
      if (snap()?.idle === false) return "busy";
      return "live";
    }
    return paintSrc() ? "ended" : "idle";
  };

  return (
    <section
      ref={(el) => {
        rootEl = el;
      }}
      class="den-live-preview"
      classList={{
        "den-live-preview--compact": !!props.compact,
        "den-live-preview--live": live(),
        "den-live-preview--ended": status() === "ended",
      }}
      data-testid="live-preview-pane"
      data-status={status()}
      data-page-id={snap()?.pageId || undefined}
      aria-label={live() ? "Live tool session" : "Final frame from live tool session"}
    >
      <header class="den-live-preview__chrome">
        <span
          class="den-live-preview__badge"
          data-testid="live-preview-status"
          data-status={status()}
        >
          {status() === "live"
            ? "Live"
            : status() === "busy"
              ? "Driving"
              : status() === "ended"
                ? "Final frame"
                : "No page"}
        </span>
        <Show when={snap()?.title || snap()?.url}>
          <span class="den-live-preview__meta" data-testid="live-preview-meta">
            <Show when={snap()?.title}>
              {(t) => <strong class="den-live-preview__title">{t()}</strong>}
            </Show>
            <Show when={snap()?.url}>
              {(u) => <code class="den-live-preview__url">{u()}</code>}
            </Show>
          </span>
        </Show>
      </header>
      <div
        class="den-live-preview__stage"
        data-testid="live-preview-stage"
        onPointerDown={(e) => e.preventDefault()}
        onClick={(e) => e.preventDefault()}
        onKeyDown={(e) => e.preventDefault()}
      >
        <Show
          when={paintSrc()}
          fallback={
            <p class="den-live-preview__empty" data-testid="live-preview-empty">
              Waiting for a driven page…
            </p>
          }
        >
          {(url) => (
            <div class="den-live-preview__frame-wrap">
              <img
                src={url()}
                alt={snap()?.title || "Page preview"}
                class="den-live-preview__img"
                draggable={false}
              />
              <Show when={live() && action()}>
                {(act) => (
                  <>
                    <div
                      class="den-live-preview__action-label"
                      data-testid="live-preview-action"
                    >
                      {act().label}
                    </div>
                    <Show
                      when={
                        act().x != null &&
                        act().y != null &&
                        act().w != null &&
                        act().h != null
                      }
                    >
                      <div
                        class="den-live-preview__target"
                        data-testid="live-preview-target"
                        style={{
                          left: `${act().x}px`,
                          top: `${act().y}px`,
                          width: `${act().w}px`,
                          height: `${act().h}px`,
                        }}
                      />
                    </Show>
                  </>
                )}
              </Show>
            </div>
          )}
        </Show>
      </div>
    </section>
  );
}
