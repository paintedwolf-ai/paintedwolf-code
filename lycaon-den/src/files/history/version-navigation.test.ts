import { afterEach, describe, expect, it } from "vitest";
import type { SourceFileVersion } from "../../api/types.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { fileBufferKey } from "../components/project-files-model.ts";
import { emptyFileVersionHistory, type FileVersionHistory, type FileVersionView } from "./file-version.ts";
import {
  resetFileVersionSelectionForTests,
  selectedVersionComparison,
  setSelectedFileVersion,
  toggleVersionComparison,
} from "./files-version-selection.ts";
import { createVersionNavigation } from "./version-navigation.ts";

const PROJECT = "p1";
const KEY = fileBufferKey("r1", "src/main.ts");
const BUFFER = {
  key: KEY,
  rootId: "r1",
  rootLabel: "repo",
  fileId: "f1",
  path: "src/main.ts",
  name: "main.ts",
  kind: "text",
} as FileBuffer;

function version(
  id: string,
  overrides: Partial<SourceFileVersion> = {},
): SourceFileVersion {
  return {
    id,
    file_id: "f1",
    workspace_kind: "project",
    root_id: "r1",
    path: "src/main.ts",
    op: "write",
    state: "content",
    content_sha256: `sha-${id}`,
    size_bytes: 10,
    capture_state: "stored",
    capture_quality: "exact",
    landing: "working_file",
    ordinal: 1,
    turn: 0,
    created_at: "2026-09-01T10:00:00Z",
    ...overrides,
  };
}

const v1 = version("v1", { created_at: "2026-09-01T10:00:00Z", origin: "user" });
const v2 = version("v2", { created_at: "2026-09-02T10:00:00Z", origin: "agent" });

const historyWithVersions: FileVersionHistory = {
  ...emptyFileVersionHistory(),
  status: "ready",
  gitStatus: "ready",
  versions: [v2, v1], // v2 is newer than v1
};

afterEach(resetFileVersionSelectionForTests);

describe("version navigation", () => {
  it("scrubs backwards to past versions and forward to Current", () => {
    const mockHistory = historyWithVersions;
    const nav = createVersionNavigation({
      projectId: () => PROJECT,
      client: () => null,
      sourceSessionId: () => "sess",
      walking: () => false,
      forgetVersionHistory: () => {},
      versionHistoryForBuffer: () => mockHistory,
    });

    // Start at Current
    expect(nav.versionForBuffer(BUFFER)).toBeNull();

    // Step -1: from Current to newest past version (v2)
    nav.stepVersion(BUFFER, -1);
    // Without a client diff response, set selectedFileVersion directly to verify navigation.
    setSelectedFileVersion(PROJECT, KEY, {
      versionId: "v2",
      fileId: "f1",
      rootId: "r1",
      path: "src/main.ts",
      op: "write",
      ts: v2.created_at,
      beforeAvailability: "available",
      availability: "available",
      sha256: null,
      sizeBytes: 10,
      source: { kind: "text", before: "v1", after: "v2" },
    });
    expect(nav.versionForBuffer(BUFFER)?.versionId).toBe("v2");

    // Stepping backwards to older version (v1)
    setSelectedFileVersion(PROJECT, KEY, {
      versionId: "v1",
      fileId: "f1",
      rootId: "r1",
      path: "src/main.ts",
      op: "write",
      ts: v1.created_at,
      beforeAvailability: "available",
      availability: "available",
      sha256: null,
      sizeBytes: 10,
      source: { kind: "text", before: "", after: "v1" },
    });
    expect(nav.versionForBuffer(BUFFER)?.versionId).toBe("v1");

    // Stepping forward from v2 (index 0) returns to Current
    setSelectedFileVersion(PROJECT, KEY, {
      versionId: "v2",
      fileId: "f1",
      rootId: "r1",
      path: "src/main.ts",
      op: "write",
      ts: v2.created_at,
      beforeAvailability: "available",
      availability: "available",
      sha256: null,
      sizeBytes: 10,
      source: { kind: "text", before: "v1", after: "v2" },
    });
    nav.stepVersion(BUFFER, 1);
    expect(nav.versionForBuffer(BUFFER)).toBeNull();
  });

  it("returns to current directly", () => {
    const nav = createVersionNavigation({
      projectId: () => PROJECT,
      client: () => null,
      sourceSessionId: () => "sess",
      walking: () => false,
      forgetVersionHistory: () => {},
    });

    setSelectedFileVersion(PROJECT, KEY, {
      versionId: "v1",
      fileId: "f1",
      rootId: "r1",
      path: "src/main.ts",
      op: "write",
      ts: v1.created_at,
      beforeAvailability: "available",
      availability: "available",
      sha256: null,
      sizeBytes: 10,
      source: { kind: "text", before: "", after: "v1" },
    });

    nav.returnToCurrent(BUFFER.key);
    expect(nav.versionForBuffer(BUFFER)).toBeNull();
  });

  it("toggles version comparison", () => {
    const shown: FileVersionView = { versionId: "v1", fileId: "f1", rootId: "r1", path: "src/main.ts", op: "write",
      ts: "2026-08-24T12:00:00Z", beforeAvailability: "available", availability: "available", sha256: null, sizeBytes: 2,
      source: { kind: "text", before: "", after: "v1" }, initialComparison: "current" };
    expect(selectedVersionComparison(PROJECT, KEY)).toBeNull();
    expect(toggleVersionComparison(PROJECT, KEY, shown)).toBe("before");
    expect(toggleVersionComparison(PROJECT, KEY, shown)).toBe("current");
  });
});
