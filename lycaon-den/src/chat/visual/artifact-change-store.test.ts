import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ARTIFACT_REFRESH_DEBOUNCE_MS,
  ARTIFACT_REFRESH_MAX_WAIT_MS,
  type ArtifactChangeDetail,
  artifactWasDeleted,
  clearArtifactDeletionMemory,
  createArtifactRefreshSubscription,
  notifyArtifactChanged,
  requestArtifactResync,
  subscribeArtifactChanges,
} from "./artifact-change-store.ts";
import { clearFrameArchives, filmstripCache } from "./frame-archive-cache.ts";
import { buildTestFilmstripZip, zipAsBlob } from "./filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "./filmstrip-zip.ts";
import { clearVisualArtifactSrcs, visualArtifactSrc, visualArtifactSrcSync } from "./visual-artifact-src.ts";
import { stubClient } from "../../test/client-fixture.ts";

afterEach(() => {
  clearArtifactDeletionMemory();
  clearVisualArtifactSrcs();
  clearFrameArchives();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("artifact-change-store", () => {
  it("preserves in-flight and settled byte caches across identical-hash written events and invalidates when content_hash changes", async () => {
    let seq = 0;
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:test/${++seq}`);
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});

    let resolveInflight!: (blob: Blob) => void;
    const imgClient = stubClient({
      getSessionArtifact: vi
        .fn<(_sid: string, _id: string) => Promise<Blob>>()
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              resolveInflight = resolve;
            }),
        )
        .mockImplementation(async () => new Blob(["v2"], { type: "image/png" })),
    });
    const archiveClient = stubClient({
      getSessionArtifact: vi.fn(async () => zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME)),
    });

    notifyArtifactChanged({
      artifact_id: "art-1",
      project_id: "proj-1",
      session_id: "sess-1",
      op: "written",
      content_hash: "hash-v1",
    });

    const inflight = visualArtifactSrc(imgClient, "sess-1", "art-1");
    // Handle binding repeats written with the same hash during fetch.
    notifyArtifactChanged({
      artifact_id: "art-1",
      project_id: "proj-1",
      session_id: "sess-1",
      op: "written",
      content_hash: "hash-v1",
    });

    resolveInflight(new Blob(["v1"], { type: "image/png" }));
    const initialSrc = await inflight;
    expect(revokeSpy).not.toHaveBeenCalled();
    expect(visualArtifactSrcSync("sess-1", "art-1")).toBe(initialSrc);

    const lease = filmstripCache.acquire(archiveClient, "sess-1", "art-1");
    const frames = await lease.ready;
    lease.release();
    expect(frames).toBeDefined();

    // Another written event with the same content_hash keeps settled caches intact.
    notifyArtifactChanged({
      artifact_id: "art-1",
      project_id: "proj-1",
      session_id: "sess-1",
      op: "written",
      content_hash: "hash-v1",
    });
    expect(revokeSpy).not.toHaveBeenCalled();
    expect(visualArtifactSrcSync("sess-1", "art-1")).toBe(initialSrc);

    const received: Array<{ projectId: string; detail: ArtifactChangeDetail }> = [];
    const unsubscribe = subscribeArtifactChanges((projectId, detail) => {
      received.push({ projectId, detail });
    });

    // Overwriting the slot with new bytes changes content_hash and retires stale caches.
    notifyArtifactChanged({
      artifact_id: "art-1",
      project_id: "proj-1",
      session_id: "sess-1",
      op: "written",
      content_hash: "hash-v2",
    });

    expect(artifactWasDeleted("art-1")).toBe(false);
    expect(visualArtifactSrcSync("sess-1", "art-1")).toBeUndefined();
    expect(revokeSpy).toHaveBeenCalledWith(initialSrc);

    const remountLease = filmstripCache.acquire(archiveClient, "sess-1", "art-1");
    await remountLease.ready;
    remountLease.release();
    expect(archiveClient.getSessionArtifact).toHaveBeenCalledTimes(2);

    expect(received).toEqual([
      {
        projectId: "proj-1",
        detail: {
          kind: "event",
          event: {
            artifact_id: "art-1",
            project_id: "proj-1",
            session_id: "sess-1",
            op: "written",
            content_hash: "hash-v2",
          },
        },
      },
    ]);
    unsubscribe();
  });

  it("invalidates caches and records tombstone state on deleted events", async () => {
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test/deleted");
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const client = stubClient({
      getSessionArtifact: vi.fn(async () => new Blob(["v1"], { type: "image/png" })),
    });

    await visualArtifactSrc(client, "sess-1", "art-del");
    notifyArtifactChanged({
      artifact_id: "art-del",
      project_id: "proj-1",
      session_id: "sess-1",
      op: "deleted",
    });

    expect(artifactWasDeleted("art-del")).toBe(true);
    expect(visualArtifactSrcSync("sess-1", "art-del")).toBeUndefined();
    expect(revokeSpy).toHaveBeenCalledWith("blob:test/deleted");
  });

  it("emits resync details only for non-empty project IDs", () => {
    const received: Array<{ projectId: string; detail: ArtifactChangeDetail }> = [];
    const unsubscribe = subscribeArtifactChanges((projectId, detail) => {
      received.push({ projectId, detail });
    });

    requestArtifactResync("   ");
    requestArtifactResync(" proj-1 ");

    expect(received).toEqual([{ projectId: "proj-1", detail: { kind: "resync" } }]);
    unsubscribe();
  });

  it("coalesces burst notifications per project, bounds steady streams by maxWaitMs, and cancels on cleanup", () => {
    vi.useFakeTimers();
    const refresh = vi.fn();
    let activeProject = "proj-1";
    const stop = createArtifactRefreshSubscription(() => activeProject, refresh);

    for (let i = 0; i < 5; i++) {
      notifyArtifactChanged({
        artifact_id: `art-${i}`,
        project_id: "proj-1",
        op: "written",
      });
    }
    notifyArtifactChanged({
      artifact_id: "art-foreign",
      project_id: "proj-2",
      op: "written",
    });
    requestArtifactResync("proj-1");

    vi.advanceTimersByTime(ARTIFACT_REFRESH_DEBOUNCE_MS - 1);
    expect(refresh).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(1);

    // Steady notifications arriving faster than delayMs still flush at maxWaitMs.
    const stepMs = Math.floor(ARTIFACT_REFRESH_DEBOUNCE_MS / 2);
    const stepsToMax = Math.floor(ARTIFACT_REFRESH_MAX_WAIT_MS / stepMs);
    for (let i = 0; i < stepsToMax; i++) {
      notifyArtifactChanged({
        artifact_id: `steady-${i}`,
        project_id: "proj-1",
        op: "written",
      });
      vi.advanceTimersByTime(stepMs);
    }
    expect(refresh).toHaveBeenCalledTimes(2);

    notifyArtifactChanged({
      artifact_id: "art-late",
      project_id: "proj-1",
      op: "written",
    });
    stop();
    vi.advanceTimersByTime(ARTIFACT_REFRESH_MAX_WAIT_MS * 2);
    expect(refresh).toHaveBeenCalledTimes(2);

    const stopSwitched = createArtifactRefreshSubscription(() => activeProject, refresh);
    notifyArtifactChanged({
      artifact_id: "art-switch",
      project_id: "proj-1",
      op: "written",
    });
    activeProject = "proj-other";
    vi.advanceTimersByTime(ARTIFACT_REFRESH_DEBOUNCE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
    stopSwitched();
  });
});
