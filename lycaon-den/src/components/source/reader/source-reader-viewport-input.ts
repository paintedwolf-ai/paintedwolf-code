import { DEN_SCROLLPORT_INPUT_EVENT, scrollportMotionForHost, type ScrollportMotion } from "../../../platform/scrolling/scrollport-motion.ts";

/** Viewport intent supersedes page restoration; layout and reflected scroll events do not. */
export function bindReaderViewportInput(host: HTMLElement, input: (pendingScroll: boolean) => void, settled: () => void): () => void {
  let motion: ScrollportMotion | undefined;
  let stopCommits: (() => void) | undefined;
  let stopSettled: (() => void) | undefined;
  let thumbGesture = false;
  const native = () => {
    const next = scrollportMotionForHost(host);
    if (next && next !== motion) {
      stopCommits?.(); stopSettled?.(); motion = next;
      stopCommits = next.subscribeCommits(source => {
        if (source === "thumb_drag" || source === "track_click") input(false);
      });
      stopSettled = next.subscribeInputSettled(settled);
    }
    const thumb = next?.isThumbGestureActive() ?? false;
    const synchronous = thumb || thumbGesture;
    thumbGesture = thumb;
    input(!synchronous);
  };
  const key = (event: KeyboardEvent) => {
    if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) input(false);
  };
  const drag = (event: PointerEvent) => { if (event.buttons !== 0) input(false); };
  host.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, native);
  host.addEventListener("keydown", key, true);
  host.addEventListener("pointermove", drag, true);
  return () => {
    stopCommits?.(); stopSettled?.();
    host.removeEventListener(DEN_SCROLLPORT_INPUT_EVENT, native);
    host.removeEventListener("keydown", key, true);
    host.removeEventListener("pointermove", drag, true);
  };
}
