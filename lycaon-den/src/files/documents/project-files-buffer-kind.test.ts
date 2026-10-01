import { describe, expect, it } from "vitest";
import type { ProjectSourceReadResponse } from "../../api/types.ts";
import {
  bufferKindFromSourceRead,
  encodingChipLabel,
  infoCardExplanation,
  infoCardTitle,
  infoCardVariant,
  isLockedImageMime,
} from "./project-files-buffer-kind.ts";

function res(
  partial: Partial<ProjectSourceReadResponse>,
): ProjectSourceReadResponse {
  return {
    file_id: "file-x",
    version_id: "version-x",
    path: "x",
    content: "",
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: 0,
    ...partial,
    workspace_id: partial.workspace_id ?? "workspace-1",
    workspace_kind: partial.workspace_kind ?? "project",
  };
}

describe("bufferKindFromSourceRead", () => {
  it("classifies editable text under the 4 MiB ceiling", () => {
    expect(
      bufferKindFromSourceRead(
        res({ content: "hello", sha256: "abc", size_bytes: 4 * 1024 * 1024 - 1 }),
      ),
    ).toBe("text");
  });

  it("classifies an empty editable file as text", () => {
    expect(bufferKindFromSourceRead(res({ content: "", sha256: "empty" }))).toBe("text");
  });

  it("classifies over-limit metadata as info", () => {
    expect(
      bufferKindFromSourceRead(
        res({
          over_limit: true,
          size_bytes: 4 * 1024 * 1024 + 1,
          mime: "text/plain",
          modified_at: "2026-01-01T00:00:00Z",
        }),
      ),
    ).toBe("info");
  });

  it("classifies sniffed PNG as image", () => {
    expect(
      bufferKindFromSourceRead(
        res({ binary: true, mime: "image/png", size_bytes: 1200 }),
      ),
    ).toBe("image");
  });

  it("classifies ELF binary as info", () => {
    expect(
      bufferKindFromSourceRead(
        res({ binary: true, mime: "application/x-executable", size_bytes: 900 }),
      ),
    ).toBe("info");
  });

  it.each(["image/x-icon", "image/vnd.microsoft.icon"])("opens %s icons in the image viewer", (mime) => {
    expect(bufferKindFromSourceRead(res({ binary: true, mime }))).toBe("image");
  });

  it("does not treat extension-only hints as image", () => {
    expect(isLockedImageMime("image/png")).toBe(true);
    expect(isLockedImageMime("application/octet-stream")).toBe(false);
  });
});

describe("info card copy", () => {
  it("uses distinct explanations for over-limit vs binary", () => {
    const overLimit = infoCardExplanation(infoCardVariant({ overLimit: true }));
    const binary = infoCardExplanation(infoCardVariant({ overLimit: false }));
    expect(overLimit).toBe("This file is larger than the in-app editor opens.");
    expect(binary).toBe(
      "This is a binary file. If you know it is BOM-less UTF-16 text, choose its byte order below.",
    );
  });

  it("names unsupported encoding and never calls it binary", () => {
    const v = infoCardVariant({
      overLimit: false,
      unsupportedEncodingDetected: "windows-1252",
    });
    expect(v).toBe("unsupported_encoding");
    expect(infoCardTitle(v)).toBe("Can't open this file safely");
    expect(infoCardExplanation(v, "windows-1252")).toContain("windows-1252");
    expect(infoCardExplanation(v, "windows-1252")).not.toMatch(/binary/i);
    expect(infoCardExplanation(v, "unknown")).toContain(
      "unsupported or malformed text encoding",
    );
  });
});

describe("status chips", () => {
  it("shows encoding only when not plain utf-8", () => {
    expect(encodingChipLabel("utf-8")).toBeNull();
    expect(encodingChipLabel("utf-8-bom")).toBe("UTF-8 BOM");
    expect(encodingChipLabel("utf-16le")).toBe("UTF-16 LE");
    expect(encodingChipLabel("utf-16le-bom")).toBe("UTF-16 LE BOM");
    expect(encodingChipLabel("utf-16be")).toBe("UTF-16 BE");
    expect(encodingChipLabel("utf-16be-bom")).toBe("UTF-16 BE BOM");
    expect(encodingChipLabel(null)).toBeNull();
  });
});
