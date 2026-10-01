// @vitest-environment jsdom
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceFileVersionsResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createFilesVersionHistory } from "./files-version-history.ts";

const buffer = { key: "file-1", fileId: "file-1", kind: "text" as const };
const page = (overrides: Partial<SourceFileVersionsResponse> = {}): SourceFileVersionsResponse => ({
  file_id: "file-1", current: { state: "absent" }, versions: [], commits: [], arrivals: [],
  git_history_state: "not_requested", ...overrides,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}
const disposals: (() => void)[] = [];
afterEach(() => { for (const dispose of disposals.splice(0)) dispose(); });

function setup(list: LycaonClient["listProjectSourceVersions"], initialWorkspace = "workspace-a") {
  return createRoot((dispose) => {
    disposals.push(dispose);
    const [sessionId, setSessionId] = createSignal<string | undefined>("session-a");
    const [workspaceId, setWorkspaceId] = createSignal(initialWorkspace);
    const client = stubClient({ listProjectSourceVersions: vi.fn(list) });
    const history = createFilesVersionHistory({
      projectId: () => "p", client: () => client, sourceSessionId: sessionId, filesWorkspaceId: workspaceId,
    });
    /** Selecting another chat; the workspace changes only when the chat's checkout does. */
    const selectChat = (session: string, workspace = workspaceId()) => { setSessionId(session); setWorkspaceId(workspace); };
    return { ...history, selectChat, client };
  });
}

describe("file version history request lifecycle", () => {
  it("loads retained rows before Git and coalesces repeated requests", async () => {
    const retained = deferred<SourceFileVersionsResponse>();
    const history = setup(() => retained.promise);
    history.loadVersionHistory(buffer);
    history.loadVersionHistory(buffer);
    expect(history.client.listProjectSourceVersions).toHaveBeenCalledTimes(1);
    retained.resolve(page());
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("ready"));
    expect(history.client.listProjectSourceVersions).toHaveBeenCalledTimes(2);
  });

  it("keeps a running request and its rows when another chat shares the checkout", async () => {
    const running = deferred<SourceFileVersionsResponse>();
    const history = setup(() => running.promise);
    history.loadVersionHistory(buffer);
    const signal = vi.mocked(history.client.listProjectSourceVersions).mock.calls.at(-1)?.[2]?.signal;
    history.selectChat("session-b");
    expect(signal?.aborted).toBe(false);
    running.resolve(page({ tracked_at: "shared" }));
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).trackedSince).toBe("shared"));
  });

  it("keeps a running request when the workspace is first answered", async () => {
    const running = deferred<SourceFileVersionsResponse>();
    const history = setup(() => running.promise, "");
    history.loadVersionHistory(buffer);
    const signal = vi.mocked(history.client.listProjectSourceVersions).mock.calls.at(-1)?.[2]?.signal;
    history.selectChat("session-a", "workspace-a");
    expect(signal?.aborted).toBe(false);
    running.resolve(page({ tracked_at: "first" }));
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).trackedSince).toBe("first"));
  });

  it("discards an old workspace's response and does not start its Git request", async () => {
    const old = deferred<SourceFileVersionsResponse>();
    const current = deferred<SourceFileVersionsResponse>();
    const history = setup((_project, _target, options) => options?.sessionId === "session-a" ? old.promise : current.promise);
    history.loadVersionHistory(buffer);
    history.selectChat("session-b", "workspace-b");
    history.loadVersionHistory(buffer);
    old.resolve(page({ tracked_at: "old" }));
    await Promise.resolve();
    expect(history.client.listProjectSourceVersions).toHaveBeenCalledTimes(2);
    expect(history.versionHistoryForBuffer(buffer).trackedSince).toBeNull();
    current.resolve(page({ tracked_at: "current" }));
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("ready"));
    expect(history.versionHistoryForBuffer(buffer).trackedSince).toBe("current");
  });

  it("does not let a closed tab's response overwrite a reopened tab", async () => {
    const old = deferred<SourceFileVersionsResponse>();
    const list = vi.fn<LycaonClient["listProjectSourceVersions"]>().mockReturnValueOnce(old.promise).mockResolvedValue(page({ tracked_at: "reopened" }));
    const history = setup(list);
    history.loadVersionHistory(buffer);
    history.forgetVersionHistory(buffer.key);
    history.loadVersionHistory(buffer);
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("ready"));
    old.resolve(page({ tracked_at: "closed" }));
    await Promise.resolve();
    expect(history.versionHistoryForBuffer(buffer).trackedSince).toBe("reopened");
  });

  it("retains the successful lane when Git fails", async () => {
    const list = vi.fn<LycaonClient["listProjectSourceVersions"]>().mockResolvedValueOnce(page({ tracked_at: "retained" })).mockRejectedValueOnce(new Error("disconnected"));
    const history = setup(list);
    history.loadVersionHistory(buffer);
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("error"));
    expect(history.versionHistoryForBuffer(buffer).status).toBe("ready");
    expect(history.versionHistoryForBuffer(buffer).trackedSince).toBe("retained");
    expect(list.mock.calls.map((call) => call[2]?.lane)).toEqual(["retained", "git"]);
  });

  it("aborts transport when a tab closes, the workspace changes, or the reactive owner is disposed", async () => {
    const pending = deferred<SourceFileVersionsResponse>();
    const history = setup(() => pending.promise);
    history.loadVersionHistory(buffer);
    const signal = () => vi.mocked(history.client.listProjectSourceVersions).mock.calls.at(-1)?.[2]?.signal;
    const closed = signal();
    expect(closed?.aborted).toBe(false);
    history.forgetVersionHistory(buffer.key);
    expect(closed?.aborted).toBe(true);
    history.loadVersionHistory(buffer);
    const oldSession = signal();
    history.selectChat("session-b", "workspace-b");
    expect(oldSession?.aborted).toBe(true);
    history.loadVersionHistory(buffer);
    const disposed = signal();
    disposals.at(-1)?.();
    expect(disposed?.aborted).toBe(true);
    pending.resolve(page());
    await Promise.resolve();
    expect(history.client.listProjectSourceVersions).toHaveBeenCalledTimes(3);
  });

  it("aborts a running Git lane when its tab closes", async () => {
    const pending = deferred<SourceFileVersionsResponse>();
    const list = vi.fn<LycaonClient["listProjectSourceVersions"]>().mockResolvedValueOnce(page()).mockReturnValue(pending.promise);
    const history = setup(list);
    history.loadVersionHistory(buffer);
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    const signal = list.mock.calls[1]?.[2]?.signal;
    history.forgetVersionHistory(buffer.key);
    expect(signal?.aborted).toBe(true);
    pending.resolve(page({ git_history_state: "available" }));
    await Promise.resolve();
    expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("idle");
  });

  it("keeps a Git read alive when an earlier-page action overlaps it", async () => {
    const pending = deferred<SourceFileVersionsResponse>();
    const list = vi.fn<LycaonClient["listProjectSourceVersions"]>().mockResolvedValueOnce(page({ next_cursor: "10" })).mockReturnValue(pending.promise);
    const history = setup(list);
    history.loadVersionHistory(buffer);
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    history.loadEarlierVersions(buffer);
    expect(list).toHaveBeenCalledTimes(2);
    expect(list.mock.calls[1]?.[2]?.signal?.aborted).toBe(false);
    pending.resolve(page({ git_history_state: "available" }));
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("ready"));
  });

  it("continues each lane with the opaque cursor its own listing issued", async () => {
    const list = vi.fn<LycaonClient["listProjectSourceVersions"]>(async (_project, _target, opts) => {
      if (!opts?.cursor) return opts?.lane === "git"
        ? page({ commits: [{ commit: "c1", subject: "one", authored_at: "", committed_at: "", source_path: "a" }], git_history_state: "available", next_cursor: "git-2" })
        : page({ next_cursor: "retained-2" });
      return opts.lane === "git"
        ? page({ commits: [{ commit: "c2", subject: "two", authored_at: "", committed_at: "", source_path: "a" }], git_history_state: "available" })
        : page();
    });
    const history = setup(list);
    history.loadVersionHistory(buffer);
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).nextGitCursor).toBe("git-2"));
    expect(history.versionHistoryForBuffer(buffer).nextVersionsCursor).toBe("retained-2");
    history.loadEarlierVersions(buffer);
    await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).loadingMore).toBe(false));
    const continued = list.mock.calls.slice(2).map(([, , opts]) => [opts?.lane, opts?.cursor]);
    expect(continued).toEqual(expect.arrayContaining([["retained", "retained-2"], ["git", "git-2"]]));
    const held = history.versionHistoryForBuffer(buffer);
    expect(held.commits.map(commit => commit.commit)).toEqual(["c1", "c2"]);
    expect(held.nextVersionsCursor).toBeNull();
    expect(held.nextGitCursor).toBeNull();
  });

  it.each([["timed_out", "took too long"], ["failed", "Couldn’t read Git history"]] as const)("reports a %s Git lane to the project's notices", async (state, phrase) => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    try {
      const history = setup(vi.fn<LycaonClient["listProjectSourceVersions"]>().mockResolvedValueOnce(page()).mockResolvedValueOnce(page({ git_history_state: state })));
      history.loadVersionHistory(buffer);
      await vi.waitFor(() => expect(history.versionHistoryForBuffer(buffer).gitStatus).toBe("ready"));
      const rows = selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "p")?.notices ?? [];
      expect(rows).toMatchObject([{ code: "files_git_history_unavailable" }]);
      expect(rows[0]?.message).toContain(phrase);
    } finally { registerNoticePublisher(null); }
  });
});
