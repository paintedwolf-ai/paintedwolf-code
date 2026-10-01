import { batch, createMemo, createSignal, type Accessor } from "solid-js";
import { loaded, loading, loadFailed, unloaded, valueOf, type LoadState } from "./load-state.ts";

export const PROJECTION_READ_TIMEOUT_MS = 30_000;

const EQUALITY_DEPTH_LIMIT = 16;

/** A memo whose value keeps its identity while structurally unchanged. */
export function createRetainedMemo<T>(read: () => T): Accessor<T> {
  return createMemo<T>((previous) => {
    const next = read();
    return previous !== undefined && structurallyEqual(previous, next) ? previous : next;
  });
}

/** Plain-data equality; class instances compare by identity only. */
export function structurallyEqual(a: unknown, b: unknown, depth = 0): boolean {
  if (Object.is(a, b)) return true;
  if (depth > EQUALITY_DEPTH_LIMIT) return false;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((item, index) => structurallyEqual(item, b[index], depth + 1));
  }
  const protoA = Object.getPrototypeOf(a);
  if (protoA !== Object.getPrototypeOf(b) || (protoA !== Object.prototype && protoA !== null)) return false;
  const left = a as Record<string, unknown>;
  const right = b as Record<string, unknown>;
  const keys = Object.keys(left);
  if (keys.length !== Object.keys(right).length) return false;
  return keys.every((key) =>
    Object.prototype.hasOwnProperty.call(right, key) && structurallyEqual(left[key], right[key], depth + 1),
  );
}

export type ProjectionRecord<T> = {
  state: Accessor<LoadState<T>>;
  value: Accessor<T | undefined>;
  cause: Accessor<unknown>;
  read: (acquire: (signal: AbortSignal) => Promise<T>) => Promise<T | undefined>;
  invalidate: () => void;
  publish: (value: T) => void;
  retain: () => () => void;
  retained: () => boolean;
  dispose: () => void;
};

function createProjectionRecord<T>(): ProjectionRecord<T> {
  const [state, setState] = createSignal<LoadState<T>>(unloaded());
  const [cause, setCause] = createSignal<unknown>();
  let revision = 0;
  let fresh = false;
  let disposed = false;
  let references = 0;
  let controller: AbortController | undefined;
  let inflight: Promise<T | undefined> | undefined;

  const record: ProjectionRecord<T> = {
    state,
    cause,
    value: () => valueOf(state()),
    read(acquire) {
      if (disposed) return Promise.resolve(undefined);
      if (inflight) return inflight;
      if (fresh) return Promise.resolve(record.value());
      controller = new AbortController();
      const signal = controller.signal;
      const requestController = controller;
      const attempt = revision;
      const before = state();
      let timedOut = false;
      const timeout = setTimeout(() => {
        timedOut = true;
        requestController.abort(new Error("Loading took too long. Try again."));
      }, PROJECTION_READ_TIMEOUT_MS);
      let detachAbort = () => {};
      const aborted = new Promise<never>((_resolve, reject) => {
        const onAbort = () => reject(signal.reason instanceof Error ? signal.reason : new Error("Loading stopped."));
        signal.addEventListener("abort", onAbort, { once: true });
        detachAbort = () => signal.removeEventListener("abort", onAbort);
      });
      setCause(undefined);
      setState(loading(state()));
      const request = Promise.resolve().then(() => {
        if (signal.aborted) throw signal.reason;
        return acquire(signal);
      });
      const run = Promise.race([request, aborted]).then(
        (value) => {
          if (disposed || signal.aborted || revision !== attempt) return undefined;
          fresh = true;
          // A re-read that returns the same data keeps the displayed identity.
          const retained = valueOf(state());
          const next = retained !== undefined && structurallyEqual(retained, value) ? retained : value;
          setState(loaded(next));
          return next;
        },
        (error: unknown) => {
          if (disposed || revision !== attempt) return undefined;
          if (!signal.aborted || timedOut) batch(() => {
            setCause(() => error);
            setState(loadFailed(error, state()));
          });
          // Abandoned by its last holder: nothing failed, so show what was there.
          else setState(before);
          return undefined;
        },
      ).then(async (value) => {
        clearTimeout(timeout);
        detachAbort();
        if (inflight !== run) return value;
        inflight = undefined;
        controller = undefined;
        // Repeat when invalidated during the read.
        if (!disposed && revision !== attempt && !fresh) return record.read(acquire);
        return value;
      });
      inflight = run;
      return run;
    },
    invalidate() {
      revision++;
      fresh = false;
    },
    publish(value) {
      if (disposed) return;
      revision++;
      fresh = true;
      controller?.abort();
      controller = undefined;
      inflight = undefined;
      setCause(undefined);
      setState(loaded(value));
    },
    retain() {
      references++;
      let released = false;
      return () => {
        if (released) return;
        released = true;
        references--;
        // A read nobody holds any longer only competes with the one that replaced it.
        if (references === 0) controller?.abort();
      };
    },
    retained: () => references > 0,
    dispose() {
      disposed = true;
      revision++;
      controller?.abort();
      inflight = undefined;
    },
  };
  return record;
}

/** Caches exact resource identities without evicting active records. */
export function createProjectionStore<T>(capacity = 64) {
  const records = new Map<string, ProjectionRecord<T>>();
  const trim = () => {
    for (const [key, record] of records) {
      if (records.size <= capacity) break;
      if (record.retained()) continue;
      records.delete(key);
      record.dispose();
    }
  };
  return {
    get(key: string): ProjectionRecord<T> {
      let record = records.get(key);
      if (record) records.delete(key);
      else record = createProjectionRecord<T>();
      records.set(key, record);
      // Keep the requested record through trimming.
      if (records.size > capacity) {
        const release = record.retain();
        trim();
        release();
      }
      return record;
    },
    invalidate(key?: string) {
      batch(() => {
        if (key !== undefined) records.get(key)?.invalidate();
        else for (const record of records.values()) record.invalidate();
      });
    },
    clear() {
      for (const record of records.values()) record.dispose();
      records.clear();
    },
    trim,
    size: () => records.size,
  };
}
