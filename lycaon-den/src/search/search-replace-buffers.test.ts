import { filesBufferText } from "../files/documents/project-files-buffers.ts";
import { stubClient } from "../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { applyFilesBufferLoad, applyFilesBufferDraft, closeFilesBuffer, openFilesBuffer, resetProjectFilesForTests } from "../files/documents/project-files-buffers.ts";
import { projectFilesState, suspendFilesBufferContent } from "../files/documents/files-buffer-state.ts";
import { refreshBuffersAfterReplace } from "./search-replace-buffers.ts";

describe("refreshBuffersAfterReplace", () => {
  beforeEach(resetProjectFilesForTests);

  it("refreshes a clean buffer addressed by its stable file id", async () => {
    const projectId = "project-1";
    const key = openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "repo",
      path: "a.txt",
      fileId: "file-a",
      intent: "permanent",
    });
    applyFilesBufferLoad(projectId, key, {
      file_id: "file-a",
      version_id: "version-old",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      root_id: "root-1",
      path: "a.txt",
      content: "old",
      sha256: "sha-old",
      encoding: "utf-8",
      writable: true,
      over_limit: false,
      binary: false,
      size_bytes: 3,
    });
    const client = stubClient({
      getProjectSource: vi.fn(async () => ({
        file_id: "file-a",
        version_id: "version-new",
        root_id: "root-1",
        path: "a.txt",
        content: "new",
        sha256: "sha-new",
        encoding: "utf-8" as const,
        writable: true,
        over_limit: false,
        binary: false,
        size_bytes: 3,
      })),
    });

    await refreshBuffersAfterReplace(client, projectId, [{
      root_id: "root-1",
      path: "a.txt",
    }]);

    expect(filesBufferText(projectFilesState(projectId).byKey[key]!)).toBe("new");
    expect(client.getProjectSource).toHaveBeenCalledOnce();
  });

  it.each(["edit", "reopen", "suspend"])("does not overwrite a buffer after %s during refresh", async (change) => {
    const args = { rootId: "r", rootLabel: "repo", path: "a.txt", fileId: "f", intent: "permanent" as const };
    const loaded = {
      file_id: "f", version_id: "v", workspace_id: "w", workspace_kind: "project" as const,
      root_id: "r", path: "a.txt", content: "old", sha256: "old-sha", encoding: "utf-8" as const,
      writable: true, over_limit: false, binary: false, size_bytes: 3,
    };
    const key = openFilesBuffer("p", args);
    applyFilesBufferLoad("p", key, loaded);
    let resolve!: (value: typeof loaded) => void;
    const client = stubClient({ getProjectSource: vi.fn(() => new Promise<typeof loaded>((done) => { resolve = done; })) });
    const refreshing = refreshBuffersAfterReplace(client, "p", [{ root_id: "r", path: "a.txt" }]);
    if (change === "reopen") {
      void closeFilesBuffer("p", key);
      openFilesBuffer("p", args);
      applyFilesBufferLoad("p", key, { ...loaded, content: "newly opened" });
    } else if (change === "suspend") {
      suspendFilesBufferContent("p", key);
    } else {
      applyFilesBufferDraft("p", key, "unsaved typing");
    }
    resolve({ ...loaded, content: "replacement", sha256: "replace-sha" });
    await refreshing;
    expect(filesBufferText(projectFilesState("p").byKey[key]!)).toBe(change === "reopen" ? "newly opened" : change === "suspend" ? "" : "unsaved typing");
    if (change === "suspend") expect(projectFilesState("p").byKey[key]?.content.state).toBe("suspended");
    expect(projectFilesState("p").byKey[key]?.dirty).toBe(change === "edit");
  });

  it("does not fetch source bodies for cold tabs after replacement", async () => {
    const key = openFilesBuffer("p", { rootId: "r", rootLabel: "repo", path: "a.txt", intent: "permanent" });
    const read = vi.fn();
    const client = stubClient({ getProjectSource: read });
    await refreshBuffersAfterReplace(client, "p", [{ root_id: "r", path: "a.txt" }]);
    suspendFilesBufferContent("p", key);
    await refreshBuffersAfterReplace(client, "p", [{ root_id: "r", path: "a.txt" }]);
    expect(read).not.toHaveBeenCalled();
    expect(projectFilesState("p").byKey[key]?.content.state).toBe("suspended");
  });
});
