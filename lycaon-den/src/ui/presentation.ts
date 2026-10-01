import { createEffect, createMemo, createSignal, onCleanup, type Accessor } from "solid-js";

export const PRESENTATION_LOADING_GRACE_MS = 300;

/** Delays loading feedback for brief waits. */
export function createPresentationWaiting(pending: Accessor<boolean>): Accessor<boolean> {
  const isPending = createMemo(pending);
  const [waiting, setWaiting] = createSignal(false);
  createEffect(() => {
    if (!isPending()) {
      setWaiting(false);
      return;
    }
    const timer = setTimeout(() => setWaiting(true), PRESENTATION_LOADING_GRACE_MS);
    onCleanup(() => clearTimeout(timer));
  });
  return waiting;
}

export type PreparationNotice = {
  message: string;
  tone: "waiting" | "error";
};

type Participant = {
  name: string;
  ready: Accessor<boolean>;
  notice?: Accessor<PreparationNotice | null>;
  retry?: () => void;
};

/** Tracks render dependencies outside the DOM. */
export function createPreparation() {
  const [participants, setParticipants] = createSignal<ReadonlyMap<symbol, Participant>>(new Map());
  const pending = () => [...participants().values()].filter((item) => !item.ready());
  const ready = () => pending().length === 0;
  return {
    ready,
    empty: () => participants().size === 0,
    retryable: () => [...participants().values()].some((item) => item.retry && item.notice?.()?.tone === "error"),
    retry() {
      for (const item of participants().values()) {
        if (item.notice?.()?.tone === "error") item.retry?.();
      }
    },
    pending: () => pending().map((item) => item.name),
    notice: () => pending().map((item) => item.notice?.()).find((notice) => notice != null) ?? null,
    register(name: string, settled: Accessor<boolean>, notice?: Accessor<PreparationNotice | null>, retry?: () => void) {
      const token = Symbol(name);
      setParticipants((current) => new Map(current).set(token, { name, ready: settled, notice, retry }));
      return () => setParticipants((current) => {
        if (!current.has(token)) return current;
        const next = new Map(current);
        next.delete(token);
        return next;
      });
    },
  };
}

export type Preparation = ReturnType<typeof createPreparation>;

/** Allows only the latest attempt to commit. */
export function createPresentationIntent() {
  let generation = 0;
  let disposed = false;

  return {
    begin() {
      const attempt = ++generation;
      const current = () => !disposed && attempt === generation;
      return {
        current,
        commit(apply: () => void) {
          if (!current()) return false;
          apply();
          return true;
        },
      };
    },
    cancel() {
      generation++;
    },
    dispose() {
      disposed = true;
      generation++;
    },
  };
}

/** Retains the display until the current candidate publishes. */
export function createPresentation<T>() {
  const [displayed, setDisplayed] = createSignal<T>();
  const intent = createPresentationIntent();
  return {
    displayed,
    begin() {
      const candidate = intent.begin();
      return {
        publish: (value: T) => candidate.commit(() => setDisplayed(() => value)),
        /** Settles the candidate without replacing the displayed snapshot it matches. */
        retain: () => candidate.commit(() => {}),
      };
    },
    cancel: intent.cancel,
    dispose: intent.dispose,
  };
}
