import { stubClient } from "../../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../../api/http.ts";
import type { FormatSettings, SourceFormatSettings } from "../../../api/types.ts";
import { isFormatConfigPath, resetFormatConfigNamesForTests } from "./format-settings.ts";
import {
  loadFormatSettingsForBuffer,
  refreshOpenFormatSettings,
} from "./format-settings-load.ts";
import { applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests, setFilesBufferFormatSettings } from "../../../files/documents/project-files-buffers.ts";
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

function resolved(path: string, rootId: string, settings: FormatSettings): SourceFormatSettings {
  return {
    path, root_id: rootId, config_names: [".editorconfig", "pyproject.toml"],
    sources: [{ tool: "editorconfig", configs: [".editorconfig"], settings }],
  };
}

const editorConfigOnly = (settings: FormatSettings) => [{ tool: "editorconfig" as const, configs: [".editorconfig"], settings }];

describe("format settings loading", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    resetFormatConfigNamesForTests();
  });

  it("stores the host resolution for the buffer's root and session", async () => {
    const key = openLoaded("pkg/foo.ts");
    const answer = resolved("pkg/foo.ts", "root-1", { indent_style: "space", indent_size: 2, trim_trailing_whitespace: true });
    const getProjectSourceFormatSettings = vi.fn(async () => answer);
    const buffer = projectFilesState("p1").byKey[key]!;
    expect(isFormatConfigPath("p1", "pkg/pyproject.toml")).toBe(false);
    await loadFormatSettingsForBuffer({
      client: stubClient({ getProjectSourceFormatSettings }), projectId: "p1", key, buffer, sessionId: "s1",
    });
    expect(getProjectSourceFormatSettings).toHaveBeenCalledWith("p1", "pkg/foo.ts", "root-1", "s1");
    expect(projectFilesState("p1").byKey[key]?.formatSources).toEqual(answer.sources);
    expect(projectFilesState("p1").byKey[key]?.editorConfig).toEqual({
      indentStyle: "spaces", indentSize: 2, indentSizeIsTab: false, trimTrailingWhitespace: true,
    });
    expect(isFormatConfigPath("p1", "pkg/pyproject.toml")).toBe(true);
  });

  it("keeps the current settings when the host cannot resolve", async () => {
    const key = openLoaded("a.ts");
    setFilesBufferFormatSettings("p1", key, editorConfigOnly({ indent_size: 8 }));
    const getProjectSourceFormatSettings = vi.fn(async () => {
      throw new LycaonApiError("unavailable", 500, "internal_error");
    });
    await refreshOpenFormatSettings({ client: stubClient({ getProjectSourceFormatSettings }), projectId: "p1", rootId: "root-1" });
    expect(projectFilesState("p1").byKey[key]?.editorConfig).toEqual({ indentSize: 8, indentSizeIsTab: false });
  });

  it("refreshes the settings of every open buffer in the root", async () => {
    const keys = ["a.ts", "pkg/b.ts"].map((path) => {
      const key = openLoaded(path);
      setFilesBufferFormatSettings("p1", key, editorConfigOnly({ indent_size: 8 }));
      return key;
    });
    const getProjectSourceFormatSettings = vi.fn(async (_pid: string, path: string, rootId: string) =>
      resolved(path, rootId, { indent_size: 2 }));
    await refreshOpenFormatSettings({ client: stubClient({ getProjectSourceFormatSettings }), projectId: "p1", rootId: "root-1" });
    expect(keys.map((key) => projectFilesState("p1").byKey[key]?.editorConfig?.indentSize)).toEqual([2, 2]);
  });
});
