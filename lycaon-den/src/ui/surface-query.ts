import { createComputed, createEffect, createMemo, onCleanup, untrack } from "solid-js";
import { createProjectionStore, type ProjectionRecord } from "../store/projection-store.ts";
import { errorOf, isResolved, unloaded, valueOf, type LoadState } from "../store/load-state.ts";
import { useResidentLive } from "./resident-activity.ts";
import { usePresentationParticipant } from "./presentation-context.tsx";
import { createPresentation, createPresentationWaiting } from "./presentation.ts";

type QuerySource = { client: object; key: string };
let clients = new WeakMap<object, Map<string, ReturnType<typeof createProjectionStore<unknown>>>>();

export function resetSurfaceQueriesForTests(): void {
  clients = new WeakMap();
}

function queryStore<T>(client: object, name: string) {
  let domains = clients.get(client);
  if (!domains) {
    domains = new Map();
    clients.set(client, domains);
  }
  let store = domains.get(name);
  if (!store) {
    store = createProjectionStore<unknown>();
    domains.set(name, store);
  }
  return store as ReturnType<typeof createProjectionStore<T>>;
}

/** Shares reads by domain and identity. */
export function createSurfaceQuery<S extends QuerySource, T>(options: {
  name: string;
  source: () => S | null;
  load: (source: S, signal: AbortSignal) => Promise<T>;
  required?: boolean;
  /** Immutable resources can reuse a prepared result on activation. */
  revalidateOnActivation?: boolean;
  /** Bounds retained data to an explicit scope. */
  scope?: (source: S) => string;
}) {
  const live = useResidentLive();
  // The key names the read, so a rebuilt source with the same client and key is the same read.
  const source = createMemo(options.source, undefined, {
    equals: (previous, next) => previous === next ||
      (previous != null && next != null && previous.client === next.client && previous.key === next.key),
  });
  const record = createMemo<ProjectionRecord<T> | undefined>(() => {
    const next = source();
    return next ? queryStore<T>(next.client, options.name).get(next.key) : undefined;
  });
  const state = (): LoadState<T> => record()?.state() ?? unloaded();
  const presentation = createPresentation<{ source: S; value: T }>();
  onCleanup(presentation.dispose);
  createComputed(() => {
    const target = source();
    const current = state();
    untrack(() => {
      if (!target) {
        presentation.cancel();
        return;
      }
      const candidate = presentation.begin();
      const value = valueOf(current);
      if (value !== undefined) candidate.publish({ source: target, value });
    });
  });
  const displayed = () => {
    const target = source();
    const snapshot = presentation.displayed();
    if (!target || !snapshot || target.client !== snapshot.source.client) return undefined;
    const scope = options.scope ?? ((s: S) => s.key);
    return scope(target) === scope(snapshot.source) ? snapshot : undefined;
  };
  const value = createMemo(() => displayed()?.value);
  const ready = () => !source() || value() !== undefined || isResolved(state());
  const refresh = async () => {
    const target = untrack(source);
    const current = untrack(record);
    if (!target || !current) return undefined;
    current.invalidate();
    return current.read((signal) => options.load(target, signal));
  };
  if (options.required !== false) usePresentationParticipant(options.name, ready,
    () => {
      const message = errorOf(state());
      return message ? { tone: "error", message } : null;
    }, () => void refresh());
  const loading = () => state().state === "loading" || (source() != null && state().state === "unloaded");
  const coldPending = () => loading() && value() === undefined;
  const showLoading = createPresentationWaiting(coldPending);
  // Retained data from before the surface went idle describes the host as it was then.
  const activation = createMemo<{ live: boolean; from: LoadState<T>; settled: boolean }>((previous) => {
    const isLive = live();
    const current = state();
    if (!previous || previous.live !== isLive) return { live: isLive, from: current, settled: false };
    if (previous.settled || !isLive || current === previous.from || !isResolved(current)) return previous;
    return { ...previous, settled: true };
  });

  createEffect(() => {
    const current = record();
    // Revision changes keep the same record and its pending read retained.
    const target = untrack(source);
    if (!current || !target) return;
    const release = current.retain();
    onCleanup(() => {
      release();
      queryStore<T>(target.client, options.name).trim();
    });
  });
  let lastActive: ProjectionRecord<T> | undefined;
  createEffect(() => {
    const target = source();
    const current = record();
    if (!live() || !target || !current) return;
    untrack(() => {
      if (options.revalidateOnActivation !== false && (lastActive === current || isResolved(current.state()))) current.invalidate();
      lastActive = current;
      void current.read((signal) => options.load(target, signal));
    });
  });

  return {
    value,
    displayed,
    state,
    ready,
    loading,
    coldPending,
    showLoading,
    refreshing: () => loading() && value() !== undefined,
    /** A read has settled since the surface last became live. */
    settledSinceActivation: () => activation().settled,
    error: () => errorOf(state()),
    cause: () => record()?.cause(),
    refresh,
    capture() {
      const target = untrack(source);
      const current = untrack(record);
      return {
        source: target,
        current: () => untrack(record) === current,
        publish: (next: T) => current?.publish(next),
        update: (change: (previous: T | undefined) => T) => {
          if (current) current.publish(change(untrack(current.value)));
        },
      };
    },
    publish(next: T) {
      untrack(record)?.publish(next);
    },
    update(change: (previous: T | undefined) => T) {
      const current = untrack(record);
      if (current) current.publish(change(untrack(current.value)));
    },
  };
}
