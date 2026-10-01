import { afterEach, describe, expect, it } from "vitest";
import type { FileVersionView } from "./file-version.ts";
import { fileBufferKey } from "../components/project-files-model.ts";
import {
  resetFileVersionSelectionForTests,
  selectedFileVersion,
  setSelectedFileVersion,
  selectedVersionComparison,
  setSelectedVersionComparison,
  shownVersionComparison,
  toggleVersionComparison,
} from "./files-version-selection.ts";

const KEY = fileBufferKey("r1", "src/main.ts");

const version = (versionId: string): FileVersionView => ({ versionId,
fileId: "file-main",
rootId: "r1",
path: "src/main.ts",
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "absent",
sha256: "sha",
sizeBytes: 4,
availability: "available", source: { kind: "text", before: "", after: "one\n" } });

afterEach(resetFileVersionSelectionForTests);

describe("files version selection", () => {
  it("holds one selection per project and buffer", () => {
    setSelectedFileVersion("p1", KEY, version("v1"));
    setSelectedFileVersion("p2", KEY, version("v2"));
    expect(selectedFileVersion("p1", KEY)?.versionId).toBe("v1");
    expect(selectedFileVersion("p2", KEY)?.versionId).toBe("v2");
  });

  it("returns to the working file when cleared", () => {
    setSelectedFileVersion("p1", KEY, version("v1"));
    setSelectedFileVersion("p1", KEY, null);
    expect(selectedFileVersion("p1", KEY)).toBeNull();
  });

  it("starts on the working file", () => {
    expect(selectedFileVersion("p1", KEY)).toBeNull();
  });

  it("tracks and toggles version comparison mode", () => {
    expect(selectedVersionComparison("p1", KEY)).toBeNull();
    setSelectedVersionComparison("p1", KEY, "current");
    expect(selectedVersionComparison("p1", KEY)).toBe("current");
    expect(toggleVersionComparison("p1", KEY, version("v1"))).toBe("before");
    expect(selectedVersionComparison("p1", KEY)).toBe("before");
    expect(toggleVersionComparison("p1", KEY, version("v1"))).toBe("current");
    expect(selectedVersionComparison("p1", KEY)).toBe("current");
  });

  it("toggles from the comparison a walk-opened version actually shows", () => {
    const walked = { ...version("v1"), initialComparison: "before" as const };
    setSelectedFileVersion("p1", KEY, walked);
    expect(shownVersionComparison("p1", KEY, walked)).toBe("before");
    expect(toggleVersionComparison("p1", KEY, walked)).toBe("current");
    expect(shownVersionComparison("p1", KEY, walked)).toBe("current");
  });

  it("keeps a version's own comparison while stepping to one that declares none", () => {
    setSelectedFileVersion("p1", KEY, { ...version("v1"), initialComparison: "before" });
    const stored = version("v2");
    setSelectedFileVersion("p1", KEY, stored);
    expect(shownVersionComparison("p1", KEY, stored)).toBe("before");
  });

  it("stays on Current for a version with no earlier side", () => {
    const first = { ...version("v1"), beforeAvailability: "unavailable" as const, initialComparison: "current" as const };
    setSelectedFileVersion("p1", KEY, first);
    expect(toggleVersionComparison("p1", KEY, first)).toBe("current");
    expect(shownVersionComparison("p1", KEY, first)).toBe("current");
  });

  it("clears version comparison when version selection is cleared", () => {
    setSelectedFileVersion("p1", KEY, version("v1"));
    setSelectedVersionComparison("p1", KEY, "before");
    expect(selectedVersionComparison("p1", KEY)).toBe("before");
    setSelectedFileVersion("p1", KEY, null);
    expect(selectedVersionComparison("p1", KEY)).toBeNull();
  });
});

