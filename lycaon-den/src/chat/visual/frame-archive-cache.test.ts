import { afterEach, describe, expect, it, vi } from "vitest";
import { FrameArchiveCache, filmstripCodec, timelineCodec } from "./frame-archive-cache.ts";
import { buildTestFilmstripZip, zipAsBlob } from "./filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "./filmstrip-zip.ts";
import { buildTestTimelineZip } from "./timeline-archive.fixture.ts";
import { TIMELINE_MIME } from "./timeline-archive.ts";
import { stubClient } from "../../test/client-fixture.ts";

const caches: Array<FrameArchiveCache<unknown>> = [];
function cache(bytes?: number, entries?: number, frames?: number) {
  const value = new FrameArchiveCache(filmstripCodec, bytes, entries, frames);
  caches.push(value as FrameArchiveCache<unknown>);
  return value;
}
function client() {
  return stubClient({ getSessionArtifact: vi.fn(async () =>
    zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME)) });
}
afterEach(() => {
  for (const value of caches.splice(0)) value.clear();
  vi.restoreAllMocks();
});

describe("filmstrip retention", () => {
  it("shares loading and decoded frames across concurrent readers and remounts", async () => {
    const store = cache();
    const api = client();
    const first = store.acquire(api, "session", "artifact");
    const second = store.acquire(api, "session", "artifact");
    expect(first.ready).toBe(second.ready);
    const frames = await first.ready;
    first.release();
    second.release();
    const remount = store.acquire(api, "session", "artifact");
    expect(remount.value).toBe(frames);
    expect(api.getSessionArtifact).toHaveBeenCalledOnce();
    remount.release();
  });

  it("separates hosts and sessions even when artifact ids agree", async () => {
    const store = cache();
    const a = client();
    const b = client();
    const leases = [store.acquire(a, "one", "id"), store.acquire(a, "two", "id"), store.acquire(b, "one", "id")];
    await Promise.all(leases.map((lease) => lease.ready));
    expect(a.getSessionArtifact).toHaveBeenCalledTimes(2);
    expect(b.getSessionArtifact).toHaveBeenCalledOnce();
    leases.forEach((lease) => lease.release());
  });

  it("evicts idle frames by bytes without invalidating a mounted reader", async () => {
    const store = cache(0);
    const api = client();
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    const active = store.acquire(api, "session", "active");
    const idle = store.acquire(api, "session", "idle");
    const [activeFrames, idleFrames] = await Promise.all([active.ready, idle.ready]);
    idle.release();
    idle.release();
    expect(revoke.mock.calls.map(([url]) => url)).toEqual([...new Set((idleFrames ?? []).map((frame) => frame.src))]);
    expect(active.value).toBe(activeFrames);
    active.release();
    expect(revoke).toHaveBeenCalledTimes(2);
  });

  it("bounds idle frame count independently of PNG byte size", async () => {
    const store = cache(undefined, undefined, 1);
    const lease = store.acquire(client(), "s", "a");
    const frames = await lease.ready;
    expect(lease.value).toBe(frames);
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    lease.release();
    expect(revoke).toHaveBeenCalledTimes(new Set((frames ?? []).map((frame) => frame.src)).size);
    expect(lease.value).toBeUndefined();
  });

  it("releases frames if teardown occurs while the ZIP bytes are being read", async () => {
    let finish!: (bytes: ArrayBuffer) => void;
    const bytes = buildTestFilmstripZip();
    const blob = zipAsBlob(bytes, FILMSTRIP_MIME);
    const read = vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finish = resolve; }));
    Object.defineProperty(blob, "arrayBuffer", { value: read });
    const store = cache();
    const api = stubClient({ getSessionArtifact: async () => blob });
    const lease = store.acquire(api, "s", "a");
    await Promise.resolve();
    expect(read).toHaveBeenCalledOnce();
    store.clear();
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    finish(new Uint8Array(bytes).buffer);
    expect(await lease.ready).toBeUndefined();
    expect(revoke).toHaveBeenCalledOnce();
    lease.release();
  });

  it("evicts the least recently released idle entry", async () => {
    const store = cache(undefined, 1);
    const api = client();
    const a = store.acquire(api, "s", "a");
    const b = store.acquire(api, "s", "b");
    await Promise.all([a.ready, b.ready]);
    a.release();
    b.release();
    const retained = store.acquire(api, "s", "b");
    expect(retained.value).toBeDefined();
    retained.release();
    const next = store.acquire(api, "s", "a");
    await next.ready;
    expect(api.getSessionArtifact).toHaveBeenCalledTimes(3);
    next.release();
  });

  it("discards a load retired by session teardown before it can create URLs", async () => {
    let resolve!: (blob: Blob) => void;
    const api = stubClient({ getSessionArtifact: () => new Promise<Blob>((done) => { resolve = done; }) });
    const store = cache();
    const create = vi.spyOn(URL, "createObjectURL");
    const lease = store.acquire(api, "s", "a");
    lease.release();
    store.clear();
    resolve(zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME));
    expect(await lease.ready).toBeUndefined();
    expect(create).not.toHaveBeenCalled();
  });

  it("invalidates deletion across sessions and permits retry after fetch failure", async () => {
    const store = cache();
    const api = client();
    vi.mocked(api.getSessionArtifact).mockRejectedValueOnce(new Error("unavailable"));
    const failed = store.acquire(api, "s", "a");
    await expect(failed.ready).rejects.toThrow("unavailable");
    failed.release();
    const leases = [store.acquire(api, "s", "a"), store.acquire(api, "t", "a")];
    await Promise.all(leases.map((lease) => lease.ready));
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    store.invalidate("a");
    leases.forEach((lease) => lease.release());
    expect(revoke).toHaveBeenCalledTimes(2);
    const replacement = store.acquire(api, "s", "a");
    expect(replacement.value).toBeUndefined();
    await replacement.ready;
    replacement.release();
  });
});

describe("timeline retention", () => {
  it("decodes a recording once and releases every frame URL when idle", async () => {
    const store = new FrameArchiveCache(timelineCodec, 0);
    caches.push(store as FrameArchiveCache<unknown>);
    const api = stubClient({ getSessionArtifact: vi.fn(async () =>
      zipAsBlob(buildTestTimelineZip(), TIMELINE_MIME)) });
    const lease = store.acquire(api, "s", "timeline");
    const recording = await lease.ready;
    expect(recording?.frames).toHaveLength(4);
    expect(recording?.manifest.summary.layout_shift.total).toBe(0.18);
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    lease.release();
    expect(revoke).toHaveBeenCalledTimes(4);
  });
});
