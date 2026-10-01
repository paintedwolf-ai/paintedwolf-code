import { stubClient } from "../../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../../api/http.ts";
import type { SourceEditorConfig } from "../../../api/types.ts";
import {
  loadEditorConfigForBuffer,
  refreshOpenEditorConfigs,
} from "./editorconfig-load.ts";
import { applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests, setFilesBufferEditorConfig } from "../../../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../../../files/documents/files-buffer-state.ts";

function openLoaded(path: string) {
  const key = openFilesBuffer("p1", { rootId: "root-1", rootLabel: "repo", path, intent: "permanent" });
  applyFilesBufferLoad("p1", key, {
    file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project", path,
    content: "const value = 1;\n", sha256: path, encoding: "utf-8", writable: true,
    over_limit: false, binary: false, size_bytes: 17,
  });
  return key;
}

describe("EditorConfig loading", () => {
  beforeEach(() => resetProjectFilesForTests());

  it("stores the host resolution for the buffer's root and session", async () => {
    const key = openLoaded("pkg/foo.ts");
    const getProjectSourceEditorConfig = vi.fn(async (): Promise<SourceEditorConfig> => ({
      path: "pkg/foo.ts", root_id: "root-1", indent_style: "space", indent_size: 2, trim_trailing_whitespace: true,
    }));
    const buffer = projectFilesState("p1").byKey[key]!;
    await loadEditorConfigForBuffer({
      client: stubClient({ getProjectSourceEditorConfig }), projectId: "p1", key, buffer, sessionId: "s1",
    });
    expect(getProjectSourceEditorConfig).toHaveBeenCalledWith("p1", "pkg/foo.ts", "root-1", "s1");
    expect(projectFilesState("p1").byKey[key]?.editorConfig).toEqual({
      indentStyle: "spaces", indentSize: 2, indentSizeIsTab: false, trimTrailingWhitespace: true,
    });
  });

  it("keeps the current settings when the host cannot resolve", async () => {
    const key = openLoaded("a.ts");
    setFilesBufferEditorConfig("p1", key, { indentSize: 8 });
    const getProjectSourceEditorConfig = vi.fn(async () => {
      throw new LycaonApiError("unavailable", 500, "internal_error");
    });
    await refreshOpenEditorConfigs({ client: stubClient({ getProjectSourceEditorConfig }), projectId: "p1", rootId: "root-1" });
    expect(projectFilesState("p1").byKey[key]?.editorConfig).toEqual({ indentSize: 8 });
  });

  it("refreshes EditorConfig settings on every open buffer in the root", async () => {
    const keys = ["a.ts", "pkg/b.ts"].map((path) => {
      const key = openLoaded(path);
      setFilesBufferEditorConfig("p1", key, { indentSize: 8 });
      return key;
    });
    const getProjectSourceEditorConfig = vi.fn(async (_pid: string, path: string, rootId: string) => ({
      path, root_id: rootId, indent_size: 2,
    }));
    await refreshOpenEditorConfigs({ client: stubClient({ getProjectSourceEditorConfig }), projectId: "p1", rootId: "root-1" });
    expect(keys.map((key) => projectFilesState("p1").byKey[key]?.editorConfig?.indentSize)).toEqual([2, 2]);
  });
});
