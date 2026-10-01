import { describe, expect, it } from "vitest";
import type { SearchHit } from "../../api/types.ts";
import { chatRefForSearchHit, chatRefForToolRaw } from "./search-add-to-chat.ts";

const roots = [{ id: "root-a", path: "/proj" }];

describe("chatRefForSearchHit", () => {
  it("prefers path-file when openPath jails under rootRefs", () => {
    const hit: SearchHit = {
      hit_id: "code-1",
      hit_kind: "code",
      source: "code",
      project_id: "proj-1",
      session_id: "sess-1",
      source_ref: "src/main.go:1",
      path: "src/main.go",
      line: 1,
      title: "fn",
      snippet: "fn",
      score: 1,
    };
    expect(chatRefForSearchHit(hit, roots)).toEqual({
      kind: "path-file",
      projectId: "proj-1",
      rootId: "root-a",
      path: "src/main.go",
      name: "main.go",
    });
  });

  it("uses artifact ref for artifact hits without an in-jail path", () => {
    const hit: SearchHit = {
      hit_id: "artifact-1",
      hit_kind: "artifact",
      source: "artifact",
      project_id: "proj-1",
      session_id: "sess-1",
      source_ref: "art-99",
      title: "Mockup",
      score: 1,
    };
    expect(chatRefForSearchHit(hit, [])).toEqual({
      kind: "artifact",
      projectId: "proj-1",
      artifactId: "art-99",
      name: "Mockup",
    });
  });

  it("uses search-hit for message and evidence rows", () => {
    const hit: SearchHit = {
      hit_id: "finding-1",
      hit_kind: "finding",
      source: "message",
      project_id: "proj-1",
      session_id: "sess-1",
      source_ref: "finding#1",
      title: "XSS",
      snippet: "script",
      score: 1,
    };
    expect(chatRefForSearchHit(hit, roots)).toEqual({
      kind: "search-hit",
      projectId: "proj-1",
      sessionId: "sess-1",
      sourceRef: "finding#1",
      hitKind: "finding",
      name: "XSS",
    });
  });

  it("returns null when search-hit cannot resolve (no source_ref)", () => {
    const hit: SearchHit = {
      hit_id: "message-1",
      hit_kind: "message",
      source: "message",
      project_id: "proj-1",
      session_id: "sess-1",
      title: "hello",
      snippet: "hello",
      score: 1,
    };
    expect(chatRefForSearchHit(hit, roots)).toBeNull();
  });
});

describe("chatRefForToolRaw", () => {
  it("builds a tool search-hit from machine coords", () => {
    expect(
      chatRefForToolRaw({
        projectId: "proj-1",
        sessionId: "sess-1",
        toolCallId: "tc-9",
        name: "command",
      }),
    ).toEqual({
      kind: "search-hit",
      projectId: "proj-1",
      sessionId: "sess-1",
      sourceRef: "tc-9",
      hitKind: "tool",
      name: "command",
    });
  });

  it("returns null when any coord is missing", () => {
    expect(
      chatRefForToolRaw({
        projectId: "",
        sessionId: "sess-1",
        toolCallId: "tc-9",
      }),
    ).toBeNull();
    expect(
      chatRefForToolRaw({
        projectId: "proj-1",
        sessionId: "  ",
        toolCallId: "tc-9",
      }),
    ).toBeNull();
    expect(
      chatRefForToolRaw({
        projectId: "proj-1",
        sessionId: "sess-1",
        toolCallId: "",
      }),
    ).toBeNull();
  });
});
