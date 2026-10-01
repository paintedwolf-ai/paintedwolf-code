/** Transcript entry motion does not control viewport position. */
export const TRANSCRIPT_ENTER_FADE_MS = 560;
export const TRANSCRIPT_ENTER_FADE_ANIMATION = "den-enter-reveal";

/** Settle on animation completion or timeout. */
export function onTranscriptEnterFadeDone(
  node: HTMLElement,
  onDone: () => void,
): () => void {
  const view = node.ownerDocument.defaultView ?? window;
  let settled = false;

  const settle = (): boolean => {
    if (settled) return false;
    settled = true;
    node.removeEventListener("animationend", onEnd);
    view.clearTimeout(safety);
    return true;
  };
  const finish = () => {
    if (settle()) onDone();
  };
  const onEnd = (event: AnimationEvent) => {
    if (event.target !== node) return;
    if (event.animationName !== TRANSCRIPT_ENTER_FADE_ANIMATION) return;
    finish();
  };
  const safety = view.setTimeout(finish, TRANSCRIPT_ENTER_FADE_MS + 80);
  node.addEventListener("animationend", onEnd);
  return () => {
    settle();
  };
}
