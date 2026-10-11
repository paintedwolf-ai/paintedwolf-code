// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { createWorkspaceProjectRemoval } from "./project-lifecycle.ts";
import { createShellStore } from "../../store/shell-store.ts";
const seams = vi.hoisted(() => ({
  prepare: vi.fn(async () => undefined), retire: vi.fn(), thumbnail: vi.fn(), invalidate: vi.fn(async () => undefined),
  report: vi.fn(), notify: vi.fn(), assess: vi.fn(), remove: vi.fn(), lookup: vi.fn(),
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
