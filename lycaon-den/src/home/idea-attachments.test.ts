// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  HOME_IDEA_FOLDER_REJECT,
  homeIdeaChipDetail,
  homeIdeaChipGlyph,
  intakeHomeIdeaDrop,
  launchChipsFromIdea,
  type HomeIdeaAttachment,
} from "./idea-attachments.ts";
import { DROP_REJECT_MISSING } from "../platform/files/file-drop.ts";
import { setPreflightReport } from "../platform/persistence/preflight-report.ts";
import { rejectMessageForCode } from "../chat/composer/composer-attachments.ts";
import { resetHomeLaunchesForTest } from "./launch/home-launches.ts";
import { memoryHomeOutbox } from "./launch/home-outbox.ts";

vi.mock("../platform/files/read-path-bytes.ts", () => ({
  importPathKind: vi.fn(),
  readPathBytes: vi.fn(),
}));

import { importPathKind } from "../platform/files/read-path-bytes.ts";

const TEST_CAPS = {
  auto_attach_paste_bytes: 16 * 1024,
  max_inline_text_bytes: 64 * 1024,
  max_attachments: 3,
  max_references: 64,
  max_images: 1,
  max_upload_bytes: 1024,
  max_image_bytes: 1024,
  max_body_bytes: 1024,
  max_turn_bytes: 2048,
  max_body_preview_bytes: 16 * 1024,
  max_large_text_preview_bytes: 8 * 1024,
  max_turn_preview_bytes: 32 * 1024,
  max_document_bytes: 1024,
  image_mime_types: ["image/png", "image/jpeg", "image/gif", "image/webp"],
  max_video_bytes: 128 * 1024 * 1024,
  video_mime_types: ["video/mp4", "video/quicktime", "video/webm"],
  text_mime_types: ["text/plain"],
  text_extensions: [".txt"],
  text_basenames: [],
};

function webFile(name: string, bytes: number, mime = "text/plain"): File {
  return new File([new Uint8Array(bytes)], name, { type: mime });
}

function fileItem(file: File) {
  return { source: "file" as const, file };
}

function pathItem(absolutePath: string) {
  return { source: "path" as const, absolutePath };
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHomeLaunchesForTest(memoryHomeOutbox());
  setPreflightReport({
    overall: "ok",
    probes: [],
    attachment_capabilities: TEST_CAPS,
  });
});

afterEach(() => {
  setPreflightReport(undefined);
});

/** The single chip a one-item drop must produce; fails loud if intake drops it. */
async function intakeOne(
  ...args: Parameters<typeof intakeHomeIdeaDrop>
): Promise<HomeIdeaAttachment> {
  const items = await intakeHomeIdeaDrop(...args);
  expect(items).toHaveLength(1);
  return items[0]!;
}

describe("intakeHomeIdeaDrop — web files", () => {
  it("buffers a file within caps with its local metadata", async () => {
    const item = await intakeOne([fileItem(webFile("notes.txt", 12))], []);
    expect(item.kind).toBe("web-file");
    if (item.kind !== "web-file") return;
    expect(item.name).toBe("notes.txt");
    expect(item.byteLength).toBe(12);
    expect(item.mime).toBe("text/plain");
  });

  it("rejects an empty file", async () => {
    const item = await intakeOne([fileItem(webFile("empty.txt", 0))], []);
    expect(item.kind).toBe("reject");
    if (item.kind !== "reject") return;
    expect(item.reason).toBe(rejectMessageForCode("unsupported_attachment"));
  });

  it("rejects a file above the upload cap", async () => {
    const item = await intakeOne([fileItem(webFile("big.txt", 4096))], []);
    expect(item.kind).toBe("reject");
    if (item.kind !== "reject") return;
    expect(item.reason).toBe(rejectMessageForCode("attachment_too_large"));
  });

  it("caps the buffered count within one drop", async () => {
    const items = await intakeHomeIdeaDrop(
      [
        fileItem(webFile("a.txt", 8)),
        fileItem(webFile("b.txt", 8)),
        fileItem(webFile("c.txt", 8)),
        fileItem(webFile("d.txt", 8)),
      ],
      [],
    );
    expect(items.map((i) => i.kind)).toEqual([
      "web-file",
      "web-file",
      "web-file",
      "reject",
    ]);
  });

  it("caps images at the image plane, not the byte plane", async () => {
    const items = await intakeHomeIdeaDrop(
      [
        fileItem(webFile("one.png", 8, "image/png")),
        fileItem(webFile("two.png", 8, "image/png")),
      ],
      [],
    );
    expect(items[0]?.kind).toBe("web-file");
    expect(items[1]?.kind).toBe("reject");
  });

  it("creates an image preview URL when the runtime provides one", async () => {
    const createObjectURL = vi.fn(() => "blob:preview");
    vi.stubGlobal("URL", Object.assign(Object.create(URL), { createObjectURL }));
    try {
      const item = await intakeOne(
        [fileItem(webFile("shot.png", 8, "image/png"))],
        [],
      );
      expect(item.kind).toBe("web-file");
      if (item.kind !== "web-file") return;
      expect(item.previewUrl).toBe("blob:preview");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("refuses every drop until host readiness loads", async () => {
    setPreflightReport(undefined);
    const item = await intakeOne([fileItem(webFile("a.txt", 8))], []);
    expect(item.kind).toBe("reject");
    if (item.kind !== "reject") return;
  });
});

describe("intakeHomeIdeaDrop — native paths", () => {
  it("buffers a regular file and guesses its payload family from the extension", async () => {
    vi.mocked(importPathKind).mockResolvedValue("file");
    const items = await intakeHomeIdeaDrop(
      [pathItem("/tmp/shot.png"), pathItem("/tmp/report.pdf")],
      [],
    );
    expect(items[0]).toMatchObject({
      kind: "path",
      name: "shot.png",
      absolutePath: "/tmp/shot.png",
      expected: "image",
    });
    expect(items[1]).toMatchObject({ kind: "path", expected: "document" });
    // A recording spends an image slot for its frame sheet, so it is guessed on its own.
    expect(await intakeOne([pathItem("/tmp/bug.mov")], [])).toMatchObject({ kind: "path", expected: "video" });
  });

  it("rejects a folder toward the Open a folder door", async () => {
    vi.mocked(importPathKind).mockResolvedValue("folder");
    const item = await intakeOne([pathItem("/tmp/repo")], []);
    expect(item.kind).toBe("reject");
    if (item.kind !== "reject") return;
    expect(item.reason).toBe(HOME_IDEA_FOLDER_REJECT);
  });

  it("rejects a missing path, treating a probe failure the same", async () => {
    vi.mocked(importPathKind).mockRejectedValue(new Error("gone"));
    const item = await intakeOne([pathItem("/tmp/gone.txt")], []);
    expect(item.kind).toBe("reject");
    if (item.kind !== "reject") return;
    expect(item.reason).toBe(DROP_REJECT_MISSING);
  });

  it("counts buffered paths against the reference plane cap", async () => {
    vi.mocked(importPathKind).mockResolvedValue("file");
    const current = await intakeHomeIdeaDrop(
      Array.from({ length: TEST_CAPS.max_references }, (_, i) => pathItem(`/file-${i}`)),
      [],
    );
    const overflow = await intakeOne([pathItem("/d")], current);
    expect(overflow.kind).toBe("reject");
  });
});

describe("chip projection", () => {
  it("labels rejects, sizes web files, and shows full native paths", async () => {
    vi.mocked(importPathKind).mockResolvedValue("file");
    const web = await intakeOne([fileItem(webFile("a.txt", 12))], []);
    const path = await intakeOne([pathItem("/tmp/shot.png")], []);
    const rejected = await intakeOne([fileItem(webFile("e.txt", 0))], []);
    expect(homeIdeaChipDetail(web)).toBe("12 B");
    expect(homeIdeaChipDetail(path)).toBe("/tmp/shot.png");
    expect(homeIdeaChipDetail(rejected)).toBe(
      rejectMessageForCode("unsupported_attachment"),
    );
    expect(homeIdeaChipGlyph(path)).toBe("▣");
    expect(homeIdeaChipGlyph(rejected)).toBe("!");
  });
});

describe("launchChipsFromIdea", () => {
  it("keeps item identity and marks bytes pending until the outbox holds them", async () => {
    vi.mocked(importPathKind).mockResolvedValue("file");
    const web = await intakeOne([fileItem(webFile("a.txt", 12))], []);
    const path = await intakeOne([pathItem("/tmp/shot.png")], []);
    const rejected = await intakeOne([fileItem(webFile("e.txt", 0))], []);
    expect(launchChipsFromIdea([web, path, rejected])).toEqual([
      { kind: "bytes", itemId: web.id, name: "a.txt", mime: "text/plain", byteLength: 12, snapshot: "pending" },
      { kind: "path", itemId: path.id, name: "shot.png", absolutePath: "/tmp/shot.png", expected: "image", snapshot: "ready" },
      { kind: "reject", itemId: rejected.id, name: "e.txt", reason: rejectMessageForCode("unsupported_attachment") },
    ]);
  });
});
