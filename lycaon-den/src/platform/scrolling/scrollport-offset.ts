type OffsetSubscriber = { next(offset: number): void };
type OffsetObservation = { subscribers: Set<OffsetSubscriber>; stop(): void };
const observations = new WeakMap<HTMLElement, OffsetObservation>();

/** Sample once in capture, before target listeners can mutate the viewport. */
export function observeScrollportOffset(
  viewport: HTMLElement,
  next: (offset: number) => void,
): () => void {
  let observation = observations.get(viewport);
  if (!observation) {
    const subscribers = new Set<OffsetSubscriber>();
    const capture = (event: Event) => {
      if (event.target !== viewport) return;
      const offset = viewport.scrollTop;
      for (const subscriber of [...subscribers]) {
        if (subscribers.has(subscriber)) subscriber.next(offset);
      }
    };
    viewport.addEventListener("scroll", capture, { passive: true, capture: true });
    observation = {
      subscribers,
      stop: () => viewport.removeEventListener("scroll", capture, true),
    };
    observations.set(viewport, observation);
  }
  const current = observation;
  const subscriber = { next };
  current.subscribers.add(subscriber);
  return () => {
    if (!current.subscribers.delete(subscriber) || current.subscribers.size > 0) return;
    current.stop();
    observations.delete(viewport);
  };
}
