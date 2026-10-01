// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { createFilesPresentationTracking } from "./files-presentation-tracking.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { completeFilePresentation, type PresentationTarget } from "./presentation-tracking.ts";

vi.mock("../../ui/resident-presence-context.tsx", () => ({ useResidentInteractive: () => () => true }));
vi.mock("../tree/scope-resolution.ts", () => ({ scopeFileFor: vi.fn() }));
vi.mock("./presentation-tracking.ts", () => ({
  completeFilePresentation: vi.fn(), filePresentationTarget: vi.fn(), trackFilePresentation: vi.fn(),
}));

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const target = (ordinal: number): PresentationTarget => ({ fileId: "file", effectId: `effect-${ordinal}`, ordinal });

function subject() {
  return createRoot(dispose => {
    const [projectId, setProjectId] = createSignal("project-a");
    const refreshScopeMarks = vi.fn(async () => undefined);
    const tracker = createFilesPresentationTracking({
      projectId, client: () => ({}) as LycaonClient,
      presentationToken: "editor", activeBuffer: () => null, stageEl: () => undefined,
      stageReady: () => true, editorPresentationPending: () => false,
      scopeContentVersion: () => 0, versionForBuffer: () => null, refreshScopeMarks,
    });
    return { ...tracker, setProjectId, refreshScopeMarks, dispose };
  });
}

let store: ReturnType<typeof createNoticeStore>;
beforeEach(() => {
  vi.clearAllMocks();
  store = createNoticeStore();
  registerNoticePublisher(store);
});
afterEach(() => registerNoticePublisher(null));

const notices = (projectId: string) =>
  selectProjectNoticeGroups(store.index()).find(group => group.projectId === projectId)?.notices ?? [];

describe("File presentation acknowledgement ordering", () => {
  it("does not report an older failure after a newer acknowledgement succeeded", async () => {
    const older = deferred();
    vi.mocked(completeFilePresentation).mockReturnValueOnce(older.promise).mockResolvedValueOnce(undefined);
    const tracking = subject();
    try {
      tracking.completePresentation(target(1));
      tracking.completePresentation(target(2));
      await vi.waitFor(() => expect(tracking.refreshScopeMarks).toHaveBeenCalledOnce());
      older.reject(new Error("late failure"));
      await older.promise.catch(() => undefined);
      await Promise.resolve();
      expect(notices("project-a")).toEqual([]);
    } finally { tracking.dispose(); }
  });

  it("reports a failed acknowledgement as a project notice", async () => {
    vi.mocked(completeFilePresentation).mockRejectedValue(new Error("unavailable"));
    const tracking = subject();
    try {
      tracking.completePresentation(target(4));
      await vi.waitFor(() => expect(notices("project-a")).toMatchObject([{ code: "files_review_save_failed", message: "unavailable" }]));
      expect(tracking.refreshScopeMarks).not.toHaveBeenCalled();
    } finally { tracking.dispose(); }
  });

  it("keeps an old project's delayed failure out of the new project", async () => {
    const pending = deferred();
    vi.mocked(completeFilePresentation).mockReturnValueOnce(pending.promise);
    const tracking = subject();
    try {
      tracking.completePresentation(target(1));
      tracking.setProjectId("project-b");
      pending.reject(new Error("old project failure"));
      await pending.promise.catch(() => undefined);
      await Promise.resolve();
      expect(notices("project-b")).toEqual([]);
      expect(tracking.refreshScopeMarks).not.toHaveBeenCalled();
    } finally { tracking.dispose(); }
  });
});
