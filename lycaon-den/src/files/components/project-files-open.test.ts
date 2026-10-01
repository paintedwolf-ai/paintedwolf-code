import { required } from "../../test/at.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { applyFilesBufferLoad, applyFilesBufferDraft, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { beginFileVersionSelection } from "../history/files-version-selection.ts";
import { openSourceInFilesStage } from "./project-files-open.ts";
import {
  navigateProjectJumpBack,
  resetProjectJumpHistoryForTests,
} from "./project-files-jump-bridge.ts";

vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "view-main", prepareClientIdentity: async () => "view-main" }));
const leaveWalk = vi.hoisted(() => vi.fn());
vi.mock("../walk/walk-store.ts", () => ({ leaveWalk }));

const projectId = "project-1";

/** Intent only; fetching and its failure paths belong to the buffer loader. */
describe("openSourceInFilesStage", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    resetProjectJumpHistoryForTests();
    leaveWalk.mockClear();
  });

  it("leaves an active walk before aiming, like every in-view opener", () => {
    openSourceInFilesStage({
      request: {
        projectId,
        rootId: "root-1",
        path: "a.txt",
        absolutePath: "/repo/a.txt",
        intent: "permanent",
      },
      rootLabelFor: () => "repo",
      navigate: () => {},
    });
    expect(leaveWalk).toHaveBeenCalledWith(projectId);
  });

  it("names the buffer and navigates without waiting on the wire", () => {
    const navigate = vi.fn();
    openSourceInFilesStage({
      request: {
        projectId,
        rootId: "root-1",
        path: "a.txt",
        absolutePath: "/repo/a.txt",
        intent: "permanent",
        line: 12,
      },
      rootLabelFor: () => "repo",
      navigate,
    });

    // Navigation does not wait on the read.
    expect(navigate).toHaveBeenCalledOnce();
    const state = projectFilesState(projectId);
    expect(state.order).toHaveLength(1);
    const key = state.order[0]!;
    expect(state.byKey[key]?.path).toBe("a.txt");
    expect(state.byKey[key]?.loading).toBe(true);
    expect(state.byKey[key]?.revealLine).toBe(12);
  });

  it.each([false, true])("rechecks a clean path and preserves an unsaved draft (dirty=%s)", (dirty) => {
    const open = () => openSourceInFilesStage({
      request: { projectId, rootId: "root-1", path: "a.txt", absolutePath: "/repo/a.txt", intent: "permanent" },
      rootLabelFor: () => "repo", navigate: () => {},
    });
    open();
    const key = applyFilesBufferLoad(projectId, projectFilesState(projectId).order[0]!, {
      workspace_id: "w", workspace_kind: "project", file_id: "file-a", version_id: "v1", path: "a.txt",
      content: "loaded", sha256: "sha", encoding: "utf-8", size_bytes: 6, writable: true, binary: false, over_limit: false,
    });
    if (dirty) applyFilesBufferDraft(projectId, key, "unsaved");
    const history = beginFileVersionSelection(projectId, key);
    open();
    expect(history.current()).toBe(false);
    const buffer = projectFilesState(projectId).byKey[key]!;
    expect(buffer.loading).toBe(!dirty);
    expect(filesBufferText(required(buffer))).toBe(dirty ? "unsaved" : "loaded");
    expect(projectFilesState(projectId).order).toHaveLength(1);
  });

  it("records the jump so the reveal survives the load", () => {
    const open = (path: string, line: number) =>
      openSourceInFilesStage({
        request: {
          projectId,
          rootId: "root-1",
          path,
          absolutePath: `/repo/${path}`,
          intent: "transient",
          line,
        },
        rootLabelFor: () => "repo",
        navigate: () => {},
      });
    open("nested/b.txt", 4);
    open("nested/c.txt", 9);

    // Stepping back reaches the address and line the first open recorded.
    const jump = navigateProjectJumpBack(projectId);
    expect(jump?.path).toBe("nested/b.txt");
    expect(jump?.line).toBe(4);
  });

  it("reuses the tab when the same address is opened again", () => {
    const open = () =>
      openSourceInFilesStage({
        request: {
          projectId,
          rootId: "root-1",
          path: "a.txt",
          absolutePath: "/repo/a.txt",
          intent: "permanent",
        },
        rootLabelFor: () => "repo",
        navigate: () => {},
      });
    open();
    open();

    expect(projectFilesState(projectId).order).toHaveLength(1);
  });

  it("ignores a request with no project", () => {
    const navigate = vi.fn();
    openSourceInFilesStage({
      request: {
        projectId: "  ",
        rootId: "root-1",
        path: "a.txt",
        absolutePath: "/repo/a.txt",
        intent: "permanent",
      },
      rootLabelFor: () => "repo",
      navigate,
    });
    expect(navigate).not.toHaveBeenCalled();
  });
});
