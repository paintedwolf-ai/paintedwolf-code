import { afterEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceHistoryState } from "../../api/types.ts";
import { createSourceHistoryCommands } from "./project-files-history.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function history(label: string): SourceHistoryState {
  return { undo: { id: label, label, kind: "create", root_id: "root", path: "file.ts", is_dir: false, removes_path: false }, redo: null };
}

function commands(getProjectSourceHistory: LycaonClient["getProjectSourceHistory"]) {
  return createSourceHistoryCommands({
    projectId: () => "project", client: () => ({ getProjectSourceHistory }) as LycaonClient,
    sourceSessionId: () => undefined,
    lifecycleHooks: () => ({ guardDirtyUnderPath: async () => "proceed", confirmChange: vi.fn() }),
  });
}

const notices = createNoticeStore();
const noticeRows = () => selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "project")?.notices ?? [];
afterEach(() => registerNoticePublisher(null));

describe("Source history refresh ordering", () => {
  it("keeps the most recent response when an earlier request completes last", async () => {
    const earlier = deferred<SourceHistoryState>();
    const later = deferred<SourceHistoryState>();
    const get = vi.fn().mockReturnValueOnce(earlier.promise).mockReturnValueOnce(later.promise);
    registerNoticePublisher(notices);
    const subject = commands(get);
    const first = subject.refreshSourceHistory();
    const second = subject.refreshSourceHistory();
    later.resolve(history("latest"));
    await second;
    earlier.resolve(history("stale"));
    await first;
    expect(subject.sourceHistory()).toEqual(history("latest"));
    expect(noticeRows()).toEqual([]);
  });

  it("does not replace successful recent history with an earlier failure", async () => {
    const earlier = deferred<SourceHistoryState>();
    registerNoticePublisher(notices);
    const get = vi.fn().mockReturnValueOnce(earlier.promise).mockResolvedValueOnce(history("latest"));
    const subject = commands(get);
    const first = subject.refreshSourceHistory();
    await subject.refreshSourceHistory();
    earlier.reject(new Error("stale failure"));
    await first;
    expect(subject.sourceHistory()).toEqual(history("latest"));
    expect(noticeRows()).toEqual([]);
  });

  it("clears history and reports a current failure to the project's notices", async () => {
    registerNoticePublisher(notices);
    const get = vi.fn().mockResolvedValueOnce(history("previous")).mockRejectedValueOnce(new Error("unavailable"));
    const subject = commands(get);
    await subject.refreshSourceHistory();
    await subject.refreshSourceHistory();
    expect(subject.sourceHistory()).toEqual({ undo: null, redo: null });
    expect(noticeRows()).toMatchObject([{ code: "files_history_action_failed", message: "unavailable" }]);
  });
});
