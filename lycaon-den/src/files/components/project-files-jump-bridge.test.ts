import { beforeEach, describe, expect, it } from "vitest";
import { fileBufferKey } from "./project-files-model.ts";
import {
  applyFilesBufferLoad,
  closeFilesBuffer,
  openFilesBuffer,
  resetProjectFilesForTests,
} from "../documents/project-files-buffers.ts";
import {
  navigateProjectJumpBack,
  pushProjectJump,
  resetProjectJumpHistoryForTests,
  retargetProjectJumpHistory,
} from "./project-files-jump-bridge.ts";

describe("project files jump bridge", () => {
  beforeEach(() => {
    resetProjectJumpHistoryForTests();
    resetProjectFilesForTests();
  });

  it("retargets a path-keyed jump after a rename", () => {
    const projectId = "project-1";
    pushProjectJump(projectId, {
      bufferKey: fileBufferKey("root-1", "src/a.ts"),
      rootId: "root-1",
      path: "src/a.ts",
      line: 3,
    }, 1);
    pushProjectJump(projectId, {
      bufferKey: fileBufferKey("root-1", "src/b.ts"),
      rootId: "root-1",
      path: "src/b.ts",
      line: 7,
    }, 1_000);

    retargetProjectJumpHistory(
      projectId,
      "root-1",
      "src/a.ts",
      "lib/a.ts",
    );

    expect(navigateProjectJumpBack(projectId)).toEqual({
      bufferKey: fileBufferKey("root-1", "lib/a.ts"),
      rootId: "root-1",
      path: "lib/a.ts",
      line: 3,
    });
  });

  it("retains and retargets the address for a closed stable-id jump", () => {
    const projectId = "project-1";
    const rootId = "root-1";
    const provisionalKey = openFilesBuffer(projectId, {
      rootId,
      rootLabel: "repo",
      path: "src/a.ts",
      intent: "permanent",
    });
    const stableKey = applyFilesBufferLoad(projectId, provisionalKey, {
      file_id: "file-a",
      version_id: "version-a",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "src/a.ts",
      content: "a",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 1,
      sha256: "sha-a",
    });
    pushProjectJump(projectId, {
      bufferKey: stableKey,
      rootId,
      path: "src/a.ts",
      line: 7,
    }, 1);
    pushProjectJump(projectId, {
      bufferKey: fileBufferKey(rootId, "src/b.ts"),
      rootId,
      path: "src/b.ts",
      line: 1,
    }, 1_000);
    retargetProjectJumpHistory(projectId, rootId, "src/a.ts", "lib/a.ts");
    void closeFilesBuffer(projectId, stableKey);

    expect(navigateProjectJumpBack(projectId)).toEqual({
      bufferKey: stableKey,
      line: 7,
      rootId,
      path: "lib/a.ts",
    });
  });
});
