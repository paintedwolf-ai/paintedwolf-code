import type { LycaonClient } from "../../api/client.ts";
import type { SourceSymbol } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";

const MAX_ENTRIES = 256;
const MAX_BYTES = 4 * 1024 * 1024;
type Symbols = readonly SourceSymbol[];
type Entry = { symbols: Symbols; bytes: number };
type Flight = { controller: AbortController; result: Promise<Symbols>; users: number };
type Cache = { entries: Map<string, Entry>; flights: Map<string, Flight>; failures: Map<string, { error: LycaonApiError; until: number }>; bytes: number };
let caches = new WeakMap<LycaonClient, Cache>();

function cacheFor(client: LycaonClient): Cache {
  let cache = caches.get(client);
  if (!cache) {
    cache = { entries: new Map(), flights: new Map(), failures: new Map(), bytes: 0 };
    caches.set(client, cache);
  }
  return cache;
}

function retain(cache: Cache, key: string, symbols: Symbols): void {
  const bytes = key.length * 2 + symbols.reduce((sum, s) => sum + 32 + 2 * (s.name.length + s.kind.length), 0);
  if (bytes > MAX_BYTES / 4) return;
  cache.entries.set(key, { symbols, bytes });
  cache.bytes += bytes;
  while (cache.entries.size > MAX_ENTRIES || cache.bytes > MAX_BYTES) {
    const oldest = cache.entries.entries().next().value;
    if (!oldest) break;
    cache.entries.delete(oldest[0]);
    cache.bytes -= oldest[1].bytes;
  }
}

/** A consumer cancels its subscription; only the last departure cancels shared work. */
function subscribe(flight: Flight, signal?: AbortSignal): Promise<Symbols> {
  flight.users++;
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = (complete: () => void) => {
      if (settled) return;
      settled = true;
      signal?.removeEventListener("abort", abort);
      flight.users--;
      complete();
    };
    const abort = () => {
      finish(() => reject(new DOMException("Source symbol request aborted", "AbortError")));
      // Reactive replacement can subscribe to the same revision in this turn.
      queueMicrotask(() => {
        if (flight.users === 0) flight.controller.abort();
      });
    };
    signal?.addEventListener("abort", abort, { once: true });
    flight.result.then(
      (symbols) => finish(() => resolve(symbols)),
      (error: unknown) => finish(() => reject(error instanceof Error
        ? error : new Error(typeof error === "string" ? error : "Source symbol request failed"))),
    );
    if (signal?.aborted) abort();
  });
}

/** Bounded content-addressed outlines, isolated by backend connection and scope. */
export async function fetchCachedSourceSymbols(args: {
  client: LycaonClient;
  projectId: string;
  rootId: string;
  path: string;
  sha256: string | null;
  sessionId?: string;
  signal?: AbortSignal;
}): Promise<Symbols> {
  args.signal?.throwIfAborted();
  const sha = args.sha256?.trim() ?? "";
  if (!sha) return [];
  const key = [args.projectId, args.sessionId?.trim() ?? "", args.rootId, args.path, sha].join("\0");
  const cache = cacheFor(args.client);
  const failure = cache.failures.get(key);
  if (failure && failure.until > Date.now()) throw failure.error;
  cache.failures.delete(key);
  const hit = cache.entries.get(key);
  if (hit) {
    cache.entries.delete(key);
    cache.entries.set(key, hit);
    return hit.symbols;
  }
  let flight = cache.flights.get(key);
  if (!flight || flight.controller.signal.aborted) {
    const controller = new AbortController();
    const next: Flight = { controller, users: 0, result: Promise.resolve([]) };
    next.result = args.client.listProjectSourceSymbols(args.projectId, args.path, {
      rootId: args.rootId, sessionId: args.sessionId, signal: controller.signal,
    }).then((response): Symbols => {
      controller.signal.throwIfAborted();
      // A file may change between the buffer read and the outline request.
      if (response.sha256 !== sha) return [];
      const symbols = Object.freeze(response.symbols.map((symbol) => Object.freeze({ ...symbol })));
      retain(cache, key, symbols);
      return symbols;
    }).catch((error: unknown) => {
      if (error instanceof LycaonApiError &&
        (error.code === "source_analysis_incomplete" || error.code === "source_analysis_unavailable")) {
        cache.failures.set(key, { error, until: Date.now() + 30_000 });
        while (cache.failures.size > 64) {
          const oldest = cache.failures.keys().next().value;
          if (oldest === undefined) break;
          cache.failures.delete(oldest);
        }
      }
      throw error;
    }).finally(() => {
      if (cache.flights.get(key) === next) cache.flights.delete(key);
    });
    cache.flights.set(key, next);
    flight = next;
  }
  return subscribe(flight, args.signal);
}

export function resetSourceSymbolsCacheForTests(): void {
  caches = new WeakMap();
}
