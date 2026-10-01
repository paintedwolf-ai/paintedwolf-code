import { describe, expect, it } from "vitest";
import {
  projectUserMessageParts,
  transcriptChipTarget,
  type TranscriptAttachmentChip,
} from "./user-message-parts.ts";

describe("projectUserMessageParts", () => {
  it("falls back to full content when parts are absent", () => {
    const got = projectUserMessageParts("plain ask", null);
    expect(got.prose).toBe("plain ask");
    expect(got.chips).toEqual([]);
    expect(got.copyText).toBe("plain ask");
  });

  it("projects prose and path-file chips from content_parts", () => {
    const fence =
      '```attachment filename="browser_nested_link_clicks.js" mime="text/plain" truncated="false"\n' +
      "[User attached file: browser/actors/test/browser/browser_nested_link_clicks.js]\n```";
    const got = projectUserMessageParts("Tell me how this works\n\n" + fence, [
      {
        content: "Tell me how this works",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
      },
      {
        content: fence,
        origin: "retrieval",
        authority: "none",
        trust_tier: "untrusted",
        source: "browser/actors/test/browser/browser_nested_link_clicks.js",
        path: "browser/actors/test/browser/browser_nested_link_clicks.js",
        media_type: "text/plain",
        reference_kind: "path_file",
      },
    ]);
    expect(got.prose).toBe("Tell me how this works");
    expect(got.chips).toHaveLength(1);
    expect(got.chips[0]).toMatchObject({
      kind: "path-file",
      label: "browser_nested_link_clicks.js",
      path: "browser/actors/test/browser/browser_nested_link_clicks.js",
    });
    expect(got.copyText).toContain("Tell me how this works");
    expect(got.copyText).toContain("Attached: browser_nested_link_clicks.js");
    expect(got.copyText).not.toContain("```attachment");
  });

  it("carries the host-stamped root through the chip to its open target", () => {
    const fence =
      '```attachment filename="a.ts" mime="text/plain" truncated="false"\n' +
      "[User attached file: @app/src/a.ts]\n```";
    const got = projectUserMessageParts(fence, [
      {
        content: fence,
        origin: "retrieval",
        authority: "none",
        trust_tier: "untrusted",
        source: "@app/src/a.ts",
        path: "@app/src/a.ts",
        root_id: "root-app",
        media_type: "text/plain",
        reference_kind: "path_file",
      },
    ]);
    expect(got.chips[0]).toMatchObject({ kind: "path-file", rootId: "root-app" });
    expect(transcriptChipTarget(got.chips[0]!)).toEqual({
      path: "@app/src/a.ts",
      rootId: "root-app",
      entryKind: "file",
      startLine: undefined,
      endLine: undefined,
    });
  });

  it("renders a ranged path-file chip from the typed scope", () => {
    const fence =
      '```attachment filename="main.go" mime="text/plain" truncated="false"\n' +
      "[User attached file: src/main.go:12-34]\n```";
    const got = projectUserMessageParts(fence, [
      {
        content: fence,
        origin: "retrieval",
        authority: "none",
        trust_tier: "untrusted",
        source: "src/main.go",
        path: "src/main.go",
        media_type: "text/plain",
        reference_kind: "path_file",
        start_line: 12,
        end_line: 34,
      },
    ]);
    expect(got.chips[0]).toMatchObject({
      kind: "path-file",
      label: "main.go:12–34",
      path: "src/main.go",
      startLine: 12,
      endLine: 34,
    });
  });

  it("reads search-hit identity from typed fields, not the hint line", () => {
    // The hint prose disagrees with the typed fields.
    const fence =
      '```attachment filename="msg-9" mime="text/plain" truncated="false"\n' +
      "[User attached search result: wrong-kind wrong-ref]\nsnippet\n```";
    const got = projectUserMessageParts(fence, [
      {
        content: fence,
        origin: "retrieval",
        authority: "none",
        trust_tier: "untrusted",
        source: "msg-9",
        media_type: "text/plain",
        reference_kind: "search_hit",
        hit_kind: "message",
        source_ref: "msg-9",
      },
    ]);
    expect(got.chips[0]).toMatchObject({
      kind: "search-hit",
      hitKind: "message",
      sourceRef: "msg-9",
    });
  });

  it("renders a folder chip from the typed kind", () => {
    const fence =
      '```attachment filename="pkg" mime="text/plain" truncated="false"\n' +
      "[User attached folder: pkg]\n```";
    const got = projectUserMessageParts(fence, [
      {
        content: fence,
        origin: "retrieval",
        authority: "none",
        trust_tier: "untrusted",
        source: "pkg",
        path: "pkg",
        media_type: "text/plain",
        reference_kind: "path_folder",
      },
    ]);
    expect(got.chips[0]).toMatchObject({ kind: "path-folder", path: "pkg" });
  });

  it("skips attachment parts without typed identity", () => {
    const fence =
      '```attachment filename="rows.csv" mime="text/csv" truncated="false" bytes="12"\na,b\n```';
    const got = projectUserMessageParts("x", [
      {
        content: "x",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
      },
      {
        content: fence,
        origin: "attachment",
        authority: "none",
        trust_tier: "untrusted",
        source: "attachment",
      },
    ]);
    expect(got.chips).toEqual([]);
  });

  it("hides host subject-binding lines from the user bubble", () => {
    const fence =
      '```attachment filename="CONTRIBUTING.md" mime="text/markdown" truncated="false"\nbody\n```';
    const bind =
      "This turn includes user attachment(s): CONTRIBUTING.md. Apply the user's request to those named materials first. Switch subject only when the user names another path.";
    const got = projectUserMessageParts("fewer words", [
      {
        content: "fewer words",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
      },
      {
        content: bind,
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        source: "attachment_subject_binding",
      },
      {
        content: fence,
        origin: "attachment",
        authority: "none",
        trust_tier: "untrusted",
        source: "CONTRIBUTING.md",
        media_type: "text/markdown",
        blob_id: "a".repeat(64),
        size_bytes: 4,
      },
    ]);
    expect(got.prose).toBe("fewer words");
    expect(got.prose).not.toContain("This turn includes");
    expect(got.copyText).not.toContain("This turn includes");
    expect(got.chips).toHaveLength(1);
    expect(got.chips[0]).toMatchObject({ kind: "text", label: "CONTRIBUTING.md" });
  });

  it("shows an attached video as a video chip", () => {
    const got = projectUserMessageParts("why does it jump?", [
      { content: "why does it jump?", origin: "user", authority: "user", trust_tier: "trusted" },
      {
        content: '```attachment filename="bug.mp4" mime="video/mp4" truncated="false"\nVideo, 0:03.0 long\n```',
        origin: "attachment",
        authority: "none",
        trust_tier: "untrusted",
        source: "bug.mp4",
        media_type: "video/mp4",
        blob_id: "b".repeat(64),
        size_bytes: 2048,
      },
    ]);
    expect(got.chips).toEqual([expect.objectContaining({ kind: "video", label: "bug.mp4", mime: "video/mp4" })]);
    expect(transcriptChipTarget(got.chips[0]!)).toBeNull();
  });
});

describe("transcriptChipTarget", () => {
  const chip = (
    over: Partial<TranscriptAttachmentChip>,
  ): TranscriptAttachmentChip => ({
    id: "part-0",
    kind: "path-file",
    label: "main.go",
    detail: "src/main.go",
    ...over,
  });

  it("opens a path-file at its recorded range", () => {
    expect(
      transcriptChipTarget(
        chip({ path: "src/main.go", startLine: 12, endLine: 20 }),
      ),
    ).toEqual({
      path: "src/main.go",
      entryKind: "file",
      startLine: 12,
      endLine: 20,
    });
  });

  it("opens a path-folder as a folder", () => {
    expect(
      transcriptChipTarget(chip({ kind: "path-folder", path: "internal/db" })),
    ).toEqual({ path: "internal/db", entryKind: "folder" });
  });

  it("returns null for payload attachments, whose body lives in host data", () => {
    expect(
      transcriptChipTarget(
        chip({ kind: "text", path: undefined, blobId: "b".repeat(64) }),
      ),
    ).toBeNull();
    expect(
      transcriptChipTarget(chip({ kind: "document", path: undefined })),
    ).toBeNull();
  });

  it("returns null for a search hit, which is a coordinate with no body", () => {
    expect(
      transcriptChipTarget(
        chip({ kind: "search-hit", path: undefined, sourceRef: "sess/42" }),
      ),
    ).toBeNull();
  });

  it("returns null when the path is blank", () => {
    expect(transcriptChipTarget(chip({ path: "   " }))).toBeNull();
  });
});
