import {
  DEFAULT_PRETTY_RESOURCE_LIMITS,
  estimateProjectionCacheBytes,
} from "./pretty-limits.ts";
import { computePrettyNativeLayout } from "./pretty-native-layout.ts";
import type {
  PrettyNativeLayoutOptions,
  PrettyNativeLayoutResult,
} from "./pretty-types.ts";

interface CacheEntry {
  readonly key: string;
  readonly sourceIdentity: string;
  readonly sourceRevision: number;
  readonly bytes: number;
  readonly result: PrettyNativeLayoutResult;
}

function makeCacheKey(
  sourceIdentity: string,
  sourceRevision: number,
  optionsKey = "",
): string {
  return `${sourceIdentity}\0${sourceRevision}\0${optionsKey}`;
}

/**
 * Bounded LRU cache for computed native Pretty layout boundaries.
 * Enforces the 8 MiB residency ceiling over accounted boundary and line-state arrays.
 */
export class PrettyProjectionCache {
  readonly maxBytes: number;
  private readonly entries = new Map<string, CacheEntry>();
  private _totalBytes = 0;
  private _evictionCount = 0;

  constructor(maxBytes = DEFAULT_PRETTY_RESOURCE_LIMITS.maxCacheBytes) {
    this.maxBytes = Math.max(0, maxBytes);
  }

  get totalBytes(): number {
    return this._totalBytes;
  }

  get size(): number {
    return this.entries.size;
  }

  get evictionCount(): number {
    return this._evictionCount;
  }

  get(
    sourceIdentity: string,
    sourceRevision: number,
    optionsKey = "",
  ): PrettyNativeLayoutResult | undefined {
    const key = makeCacheKey(sourceIdentity, sourceRevision, optionsKey);
    const existing = this.entries.get(key);
    if (!existing) return undefined;
    this.entries.delete(key);
    this.entries.set(key, existing);
    return existing.result;
  }

  set(result: PrettyNativeLayoutResult, optionsKey = ""): boolean {
    const bytes = estimateProjectionCacheBytes(result);
    if (bytes > this.maxBytes) return false;
    const key = makeCacheKey(
      result.sourceIdentity,
      result.sourceRevision,
      optionsKey,
    );
    const prev = this.entries.get(key);
    if (prev) {
      this._totalBytes -= prev.bytes;
      this.entries.delete(key);
    }

    while (this.entries.size > 0 && this._totalBytes + bytes > this.maxBytes) {
      const oldestKey = this.entries.keys().next().value;
      if (oldestKey === undefined) break;
      const oldest = this.entries.get(oldestKey);
      if (oldest) {
        this._totalBytes -= oldest.bytes;
        this.entries.delete(oldestKey);
        this._evictionCount += 1;
      }
    }

    this.entries.set(key, {
      key,
      sourceIdentity: result.sourceIdentity,
      sourceRevision: result.sourceRevision,
      bytes,
      result,
    });
    this._totalBytes += bytes;
    return true;
  }

  deleteSurface(sourceIdentity: string): number {
    let removed = 0;
    for (const [key, entry] of this.entries) {
      if (entry.sourceIdentity === sourceIdentity) {
        this._totalBytes -= entry.bytes;
        this.entries.delete(key);
        removed += 1;
      }
    }
    return removed;
  }

  clear(): void {
    this.entries.clear();
    this._totalBytes = 0;
  }
}

export interface PrettyScheduledJobOptions extends PrettyNativeLayoutOptions {
  readonly sourceIdentity: string;
  readonly sourceRevision: number;
  readonly generation?: number;
  readonly enabled?: boolean;
  readonly optionsKey?: string;
  readonly onPublish?: (result: PrettyNativeLayoutResult) => void;
}

interface SurfaceJobState {
  revision: number;
  generation: number;
  enabled: boolean;
  jobId: number;
  abort?: AbortController;
}

/**
 * Coordinates at most one active layout job per surface.
 * Cancels superseded work and refuses to publish stale revisions or disabled states.
 */
export class PrettyJobScheduler {
  readonly cache: PrettyProjectionCache;
  private readonly surfaces = new Map<string, SurfaceJobState>();
  private nextJobId = 1;
  private _canceledJobCount = 0;
  private _staleDiscardedCount = 0;
  private _publishedCount = 0;

  constructor(cache = new PrettyProjectionCache()) {
    this.cache = cache;
  }

  get canceledJobCount(): number {
    return this._canceledJobCount;
  }

  get staleDiscardedCount(): number {
    return this._staleDiscardedCount;
  }

  get publishedCount(): number {
    return this._publishedCount;
  }

  setSurfaceState(
    sourceIdentity: string,
    update: { revision?: number; generation?: number; enabled?: boolean },
  ): void {
    const current = this.surfaces.get(sourceIdentity) ?? {
      revision: update.revision ?? 0,
      generation: update.generation ?? 0,
      enabled: update.enabled ?? true,
      jobId: 0,
    };
    const nextRevision = update.revision ?? current.revision;
    const nextGeneration = update.generation ?? current.generation;
    const nextEnabled = update.enabled ?? current.enabled;
    if (
      current.abort &&
      (nextRevision !== current.revision ||
        nextGeneration !== current.generation ||
        !nextEnabled)
    ) {
      current.abort.abort();
      current.abort = undefined;
      this._canceledJobCount += 1;
    }
    current.revision = nextRevision;
    current.generation = nextGeneration;
    current.enabled = nextEnabled;
    this.surfaces.set(sourceIdentity, current);
  }

  cancelSurface(sourceIdentity: string): void {
    const state = this.surfaces.get(sourceIdentity);
    if (state?.abort) {
      state.abort.abort();
      state.abort = undefined;
      this._canceledJobCount += 1;
    }
  }

  disposeSurface(sourceIdentity: string): void {
    this.cancelSurface(sourceIdentity);
    this.surfaces.delete(sourceIdentity);
    this.cache.deleteSurface(sourceIdentity);
  }

  async schedule(
    rawText: string,
    options: PrettyScheduledJobOptions,
  ): Promise<PrettyNativeLayoutResult | null> {
    const {
      sourceIdentity,
      sourceRevision,
      generation = 0,
      enabled = true,
      optionsKey = "",
      onPublish,
    } = options;

    const prev = this.surfaces.get(sourceIdentity);
    if (prev?.abort) {
      prev.abort.abort();
      prev.abort = undefined;
      this._canceledJobCount += 1;
    }

    const jobId = this.nextJobId++;
    const abort = new AbortController();
    if (options.signal) {
      if (options.signal.aborted) abort.abort();
      else {
        options.signal.addEventListener("abort", () => abort.abort(), {
          once: true,
        });
      }
    }

    const state: SurfaceJobState = {
      revision: sourceRevision,
      generation,
      enabled,
      jobId,
      abort: enabled ? abort : undefined,
    };
    this.surfaces.set(sourceIdentity, state);

    if (!enabled) {
      this._staleDiscardedCount += 1;
      return null;
    }

    const cached = this.cache.get(sourceIdentity, sourceRevision, optionsKey);
    if (cached) {
      state.abort = undefined;
      this._publishedCount += 1;
      onPublish?.(cached);
      return cached;
    }

    await Promise.resolve();

    const liveBefore = this.surfaces.get(sourceIdentity);
    if (
      abort.signal.aborted ||
      !liveBefore ||
      liveBefore.jobId !== jobId ||
      !liveBefore.enabled ||
      liveBefore.revision !== sourceRevision ||
      liveBefore.generation !== generation
    ) {
      if (!abort.signal.aborted) this._staleDiscardedCount += 1;
      return null;
    }

    const result = computePrettyNativeLayout(rawText, {
      ...options,
      signal: abort.signal,
    });

    if (liveBefore.abort === abort) {
      liveBefore.abort = undefined;
    }

    const liveAfter = this.surfaces.get(sourceIdentity);
    if (
      result.outcome === "canceled" ||
      abort.signal.aborted ||
      !liveAfter ||
      liveAfter.jobId !== jobId ||
      !liveAfter.enabled ||
      liveAfter.revision !== sourceRevision ||
      liveAfter.generation !== generation
    ) {
      if (result.outcome !== "canceled") {
        this._staleDiscardedCount += 1;
      }
      return null;
    }

    this.cache.set(result, optionsKey);
    this._publishedCount += 1;
    onPublish?.(result);
    return result;
  }
}
