import "../../test/document-outbox-fixture.ts";
import { beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import { scheduleSessionFidelityPersist } from "../editor/editor-session-fidelity.ts";
import { fileVersionFromSnapshot } from "../history/file-version.ts";
import { openFilesBuffer, applyFilesBufferLoad, focusedFilesBufferKey, resetProjectFilesForTests, setFilesActiveBuffer } from "./project-files-buffers.ts";
import { projectFilesState } from "./files-buffer-state.ts";
import {
  flushFilesHotExitToDisk,
  parseFilesHotExitState,
  restoreFilesHotExitForProject,
  scheduleFilesHotExitPersist,
  syncFilesHotExitFromSnapshot,
} from "./files-hot-exit.ts";

const projectId = "project-hot-exit";
const roots = [{
  id: "root-1",
  path: "/repo",
  label: "repo",
  is_primary: true,
  added_at: "2026-01-01T00:00:00Z",
  kind: "attached" as const,
}];

describe("Files hot exit", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    syncFilesHotExitFromSnapshot();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  });

  it("keeps the active file selected when invalid persisted rows are discarded", () => {
    const state = parseFilesHotExitState({ byProject: { [projectId]: {
      activeIndex: 2,
      buffers: [
        { rootId: "root-1", path: "a.txt", kind: "text" },
        { rootId: "root-1", path: "unsupported", kind: "unknown" },
        { rootId: "root-1", path: "b.txt", kind: "image" },
      ],
    } } });
    expect(state?.byProject[projectId]?.activeIndex).toBe(1);
    expect(state?.byProject[projectId]?.buffers.map((row) => row.path)).toEqual(["a.txt", "b.txt"]);
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1, filesHotExit: state });
    restoreFilesHotExitForProject(projectId, roots);
    const restored = projectFilesState(projectId);
    expect(restored.byKey[restored.activeKey!]?.path).toBe("b.txt");
  });

  it("restores as a presentation, so a reader's later open displaces it", () => {
    const state = parseFilesHotExitState({ byProject: { [projectId]: {
      activeIndex: 1,
      buffers: [
        { rootId: "root-1", path: "a.txt", kind: "text" },
        { rootId: "root-1", path: "b.txt", kind: "text" },
      ],
    } } });
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1, filesHotExit: state });
    restoreFilesHotExitForProject(projectId, roots);
    expect(projectFilesState(projectId).aimOrigin).toBe("presentation");
    const asked = openFilesBuffer(projectId, {
      rootId: "root-1", rootLabel: "repo", path: "c.txt", intent: "permanent",
    });
    expect(focusedFilesBufferKey(projectId)).toBe(asked);
    expect(projectFilesState(projectId).aimOrigin).toBe("reader");
  });

  it.each([0, -1, 0.5, 3, NaN, Infinity, "1"])(
    "drops an active index that does not identify a retained row: %s", (activeIndex) => {
      const state = parseFilesHotExitState({ byProject: { [projectId]: {
        activeIndex,
        buffers: [null, { rootId: "root-1", path: "a.txt", kind: "text" }],
      } } });
      expect(state?.byProject[projectId]?.buffers).toHaveLength(1);
      expect(state?.byProject[projectId]?.activeIndex).toBeUndefined();
    },
  );

  it("persists presentation identity without draft bytes", async () => {
    openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "a.txt",
      intent: "permanent",
    });
    openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "worker.txt",
      intent: "permanent",
      jobId: "job-1",
    });
    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();

    const persisted = getAppStateSnapshot().filesHotExit?.byProject[projectId];
    expect(persisted?.buffers).toHaveLength(1);
    expect(JSON.stringify(persisted)).not.toContain("draft");
  });

  it("persists the active index for stable-id buffers", async () => {
    const first = openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "a.txt",
      fileId: "file-a",
      intent: "permanent",
    });
    openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "b.txt",
      fileId: "file-b",
      intent: "permanent",
    });
    setFilesActiveBuffer(projectId, first);

    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();

    expect(
      getAppStateSnapshot().filesHotExit?.byProject[projectId]?.activeIndex,
    ).toBe(0);
  });

  it("persists the tab the reader aimed at while the previous one stays painted", async () => {
    const painted = openFilesBuffer(projectId, {
      rootId: "root-1", rootLabel: "repo", path: "a.txt", intent: "permanent",
    });
    applyFilesBufferLoad(projectId, painted, {
      file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project",
      path: "a.txt", content: "a", over_limit: false, writable: true, binary: false,
      size_bytes: 1, sha256: "sha-a",
    });
    const aimed = openFilesBuffer(projectId, {
      rootId: "root-1", rootLabel: "repo", path: "b.txt", intent: "permanent",
    });
    expect(projectFilesState(projectId)).toMatchObject({ activeKey: painted, pendingKey: aimed });

    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();

    expect(
      getAppStateSnapshot().filesHotExit?.byProject[projectId]?.activeIndex,
    ).toBe(1);
  });

  it("persists file panes without serializing comparison previews as files", async () => {
    openFilesBuffer(projectId, {
      rootId: "root-1", rootLabel: "repo", path: "a.txt", intent: "permanent",
    });
    openFilesBuffer(projectId, {
      rootId: "root-1", rootLabel: "repo", path: "b.txt", intent: "permanent",
      kind: "diff",
      diffPreview: {
        version: fileVersionFromSnapshot({
          rootId: "root-1", path: "b.txt", before: "before", after: "after",
        }),
      },
    });
    openFilesBuffer(projectId, {
      rootId: "", rootLabel: "", path: "", intent: "permanent", kind: "walk", name: "Outside the app",
      walkStep: { kind: "outside", key: "outside:o1", ordinal: 2, effects: [], toolCallId: null, label: "outside" },
    });
    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();
    const entry = getAppStateSnapshot().filesHotExit?.byProject[projectId];
    expect(entry?.buffers.map(({ path, kind }) => ({ path, kind }))).toEqual([
      { path: "a.txt", kind: "text" },
    ]);
    expect(entry?.activeIndex).toBeUndefined();
    resetProjectFilesForTests();
    restoreFilesHotExitForProject(projectId, roots);
    const state = projectFilesState(projectId);
    expect(state.order).toHaveLength(1);
    expect(state.byKey[state.order[0]!]?.path).toBe("a.txt");
  });

  it.each(["utf-16le", "utf-16be"] as const)("restores the human-selected %s byte order", async (encoding) => {
    const key = openFilesBuffer(projectId, { rootId: "root-1", rootLabel: "repo", path: "wide.txt", intent: "permanent" });
    applyFilesBufferLoad(projectId, key, {
      file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project",
      path: "wide.txt", content: "hello", binary: false, over_limit: false, writable: true,
      size_bytes: 10, encoding, sha256: "sha",
    });
    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();
    expect(getAppStateSnapshot().filesHotExit?.byProject[projectId]?.buffers[0]?.decodeAs).toBe(encoding);
    resetProjectFilesForTests();
    restoreFilesHotExitForProject(projectId, roots);
    const state = projectFilesState(projectId);
    expect(state.byKey[state.order[0]!]).toMatchObject({ loading: true, encoding });
  });

  it("persists hot-exit and session fidelity in one write", async () => {
    openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "a.txt",
      intent: "permanent",
    });
    scheduleFilesHotExitPersist(projectId);
    scheduleSessionFidelityPersist();
    await flushFilesHotExitToDisk();
    const snap = getAppStateSnapshot();
    expect(snap.filesHotExit?.byProject[projectId]?.buffers).toHaveLength(1);
    expect(snap.editorViewState).toBeDefined();
  });

  it("restores tabs after the project's root set changes", async () => {
    openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "a.txt",
      intent: "permanent",
    });
    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();

    resetProjectFilesForTests();
    restoreFilesHotExitForProject(projectId, roots);
    expect(projectFilesState(projectId).order).toHaveLength(1);
  });
});
