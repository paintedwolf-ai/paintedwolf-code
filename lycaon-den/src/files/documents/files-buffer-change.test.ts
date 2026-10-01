import { describe, expect, it } from "vitest";
import type { FileBuffer } from "./files-buffer-state.ts";
import type { FileVersionView } from "../history/file-version.ts";
import { filesBufferChange, fileVersionChangeKind } from "./files-buffer-change.ts";

const buffer = (values: Partial<FileBuffer>): FileBuffer =>
  ({ key: "r\0a.ts", rootId: "r", rootLabel: "repo", fileId: "f", path: "a.ts", name: "a.ts", kind: "text",
    sourcePresent: true, ...values }) as FileBuffer;

const version = (beforeAvailability: string, availability: string) =>
  ({ beforeAvailability, availability }) as FileVersionView;

describe("fileVersionChangeKind", () => {
  it("names what the shown version did to the file", () => {
    expect(fileVersionChangeKind(version("absent", "available"))).toBe("added");
    expect(fileVersionChangeKind(version("available", "absent"))).toBe("deleted");
    expect(fileVersionChangeKind(version("not_captured", "available"))).toBe("changed");
    expect(fileVersionChangeKind(version("absent", "absent"))).toBeNull();
  });
});

describe("filesBufferChange", () => {
  it("prefers the shown version over the file's state in the current view", () => {
    const deletedNow = buffer({ deleted: { deleted_ts: "", previous: {} , reader: {} } as unknown as FileBuffer["deleted"] });
    expect(filesBufferChange("p", deletedNow, version("absent", "available"))).toEqual({ kind: "added", subject: "version" });
    expect(filesBufferChange("p", deletedNow, null)).toEqual({ kind: "deleted", subject: "view" });
  });

  it("marks a composed preview tab from its version", () => {
    const preview = buffer({ kind: "diff" });
    expect(filesBufferChange("p", preview, version("available", "absent"))).toEqual({ kind: "deleted", subject: "version" });
    expect(filesBufferChange("p", preview, null)).toBeNull();
  });
});
