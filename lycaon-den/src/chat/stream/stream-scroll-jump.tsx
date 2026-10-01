import { createEffect, createSignal, onCleanup } from "solid-js";
import {
  isStreamNearBottom,
  streamTranscriptEl,
  subscribeStreamScroll,
} from "./stream-scroll.ts";
import { useTranscriptViewport } from "./transcript-viewport.tsx";

export function StreamScrollJump() {
  const viewport = useTranscriptViewport();
  const [visible, setVisible] = createSignal(false);

  createEffect(() => {
    const el = viewport?.stream();
    // Following keeps the latest message in view.
    if (!el || viewport?.following() !== false) {
      setVisible(false);
      return;
    }
    let frame: number | undefined;
    // The end of history before a gap is not the latest message.
    const detached = !viewport.presentsLiveTail();
    const sync = () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      frame = undefined;
      setVisible(detached || !isStreamNearBottom(el));
    };
    const schedule = () => {
      if (frame === undefined) frame = requestAnimationFrame(sync);
    };
    // Scroll subscribers already share the read phase before virtual rows change.
    const unsubscribe = subscribeStreamScroll(el, sync);
    // Content arriving below the reader offers the jump without a scroll.
    const ro = new ResizeObserver(schedule);
    ro.observe(streamTranscriptEl(el));
    schedule();
    onCleanup(() => {
      unsubscribe();
      ro.disconnect();
      if (frame !== undefined) cancelAnimationFrame(frame);
    });
  });

  const onJump = () => {
    viewport?.jumpToTail(true);
  };

  return (
    <button
      type="button"
      class="den-chat-stream-jump"
      classList={{ "den-chat-stream-jump--visible": visible() }}
      data-testid="stream-scroll-jump"
      aria-label="Scroll to latest messages"
      aria-hidden={!visible()}
      tabIndex={visible() ? 0 : -1}
      onClick={onJump}
    >
      <span class="den-chat-stream-jump-icon" aria-hidden="true" />
    </button>
  );
}
