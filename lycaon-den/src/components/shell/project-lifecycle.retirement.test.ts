// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { createWorkspaceProjectRemoval } from "./project-lifecycle.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectAppNotices } from "../../notices/notice-select.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import type { ProjectRemovalProgress } from "../../api/types.ts";
import { createShellStore } from "../../store/shell-store.ts";
const seams = vi.hoisted(() => ({
  prepare: vi.fn(async () => undefined), retire: vi.fn(), thumbnail: vi.fn(), invalidate: vi.fn(async () => undefined),
  report: vi.fn(), notify: vi.fn((_notice: import("../../notices/notice-model.ts").NoticeInput) => {}), assess: vi.fn(), remove: vi.fn(), lookup: vi.fn(),
}));
vi.mock("../../files/documents/project-files-buffers.ts", () => ({ prepareFilesRootDetach: seams.prepare, dropFilesBuffersForRoot: vi.fn() }));
vi.mock("../../home/thumbnail-store.ts", () => ({ forgetProjectThumbnail: seams.thumbnail }));
vi.mock("../../contributions/contribution-store.ts", () => ({ invalidateContributionFrame: seams.invalidate }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getEntityRetire: () => ({ project: seams.retire }),
  getLycaonClient: () => ({ assessProjectRemoval: seams.assess, createProjectRemoval: seams.remove, getProjectRemoval: seams.lookup }),
  noticeReporterFor: () => ({ reportError: seams.report, publish: seams.notify }) }));

describe("workspace project retirement", () => {
  it("prepares every root and retires local identity only after durable deletion", async () => {
    const shell = createShellStore("connected"); shell.commitStageScope({ projectId: "project", sessionId: "session" });
    const drop = vi.fn(); const removeRecentsForProject = vi.fn(async () => undefined);
    seams.assess.mockResolvedValue({ assessment_token: "assessment", extensions: [] });
    seams.remove.mockResolvedValue({ project_state: "deleted", cleanup_state: "removed" });
    const controller = createRoot((dispose) => {
      onTestFinished(dispose);
      return createWorkspaceProjectRemoval({ shell, projects: { byId: () => ({ roots: [{ id: "root-one" }, { id: "root-two" }] }),
        summary: () => ({ displayName: "Garden" }), drop } as never, recents: { removeRecentsForProject } as never });
    });
    await controller.deleteProject("project", { confirmed: true });
    expect(seams.prepare).toHaveBeenCalledWith("project", "root-one"); expect(seams.prepare).toHaveBeenCalledWith("project", "root-two");
    expect(seams.remove).toHaveBeenCalledWith("project", expect.objectContaining({ assessment_token: "assessment", force: false, remove_extensions: [] }));
    expect(seams.retire).toHaveBeenCalledWith("project"); expect(drop).toHaveBeenCalledWith("project");
    expect(seams.thumbnail).toHaveBeenCalledWith("project"); expect(removeRecentsForProject).toHaveBeenCalledWith("project");
    expect(shell.state.activeProjectId).toBeNull(); expect(seams.invalidate).toHaveBeenCalledOnce();
  });
});

it("updates one existing app notice with host progress and withdraws it at settlement", async () => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  const notices = createNoticeStore();
  registerNoticePublisher(notices);
  onTestFinished(() => { registerNoticePublisher(null); vi.useRealTimers(); });
  seams.notify.mockImplementation((notice) => notices.publish(notice, APP_SCOPE));
  seams.assess.mockResolvedValue({ assessment_token: "assessment", extensions: [] });
  const phases: ProjectRemovalProgress["phase"][] = ["preparing", "chats", "project_data", "files", "extensions"];
  const messages = ["Preparing deletion", "Deleting chats: 2 of 5 complete", "Deleting project data", "Removing managed project files", "Removing selected extensions"];
  const running = (phase: ProjectRemovalProgress["phase"]) => ({ project_state: "unknown", progress: { phase, completed: 2, total: 5 } });
  seams.remove.mockResolvedValue(running("preparing"));
  for (const phase of phases.slice(1)) seams.lookup.mockResolvedValueOnce(running(phase));
  seams.lookup.mockResolvedValueOnce({ project_state: "deleted", cleanup_state: "removed" });
  const controller = createRoot((dispose) => {
    onTestFinished(dispose);
    return createWorkspaceProjectRemoval({ shell: createShellStore("connected"), projects: {
      byId: () => ({ roots: [] }), summary: () => ({ displayName: "Garden" }), drop: vi.fn(),
    } as never, recents: { removeRecentsForProject: vi.fn() } as never });
  });
  const done = controller.deleteProject("project", { confirmed: true });
  await vi.advanceTimersByTimeAsync(0);
  let noticeID: string | undefined;
  for (let i = 0; i < phases.length; i++) {
    if (i) await vi.advanceTimersByTimeAsync(1000);
    const rows = selectAppNotices(notices.index());
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ code: "project_removal_running", title: "Deleting project", severity: "info", message: `Garden: ${messages[i]}. You can keep using other projects.` });
    if (noticeID) expect(rows[0]?.id).toBe(noticeID);
    noticeID = rows[0]?.id;
    expect(seams.retire).not.toHaveBeenCalled();
  }
  await vi.advanceTimersByTimeAsync(1000);
  await done;
  expect(selectAppNotices(notices.index())).toHaveLength(0);
  expect(seams.retire).toHaveBeenCalledWith("project");
  expect(seams.report).not.toHaveBeenCalled();
});
