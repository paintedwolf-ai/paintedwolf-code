import { describe, expect, it } from "vitest";
import {
  searchHitDisplay,
  softTitle,
} from "./search-hit-display.ts";

describe("searchHitDisplay", () => {
  it("labels a symbol hit by its declaration kind and keeps the host's match positions", () => {
    const d = searchHitDisplay({
      hit_id: "sym-1",
      hit_kind: "symbol",
      source: "code",
      project_id: "p",
      root_id: "r",
      path: "internal/config/parse.go",
      line: 12,
      title: "ParseConfig",
      context: "internal/config/parse.go:12",
      snippet: "func ParseConfig(path string) (*Config, error) {",
      symbol_kind: "function",
      title_highlights: [{ start: 0, end: 1 }, { start: 5, end: 6 }],
    });
    expect(d.kindLabel).toBe("Function");
    expect(d.openLabel).toBe("Open");
    expect(d.titleMatches).toEqual([0, 5]);
    expect(d.openPath).toEqual({ path: "internal/config/parse.go", line: 12 });
    expect(d.preview).toEqual({ format: "code", text: "func ParseConfig(path string) (*Config, error) {" });
  });

  it("names a symbol hit without a declaration kind plainly", () => {
    const d = searchHitDisplay({ hit_id: "sym-2", hit_kind: "symbol", source: "code", project_id: "p", title: "Cfg" });
    expect(d.kindLabel).toBe("Symbol");
    expect(d.titleMatches).toBeUndefined();
  });

  it("uses host-stamped title/context and keeps an open path for file hits", () => {
    const d = searchHitDisplay({
      hit_id: "file-1",
      hit_kind: "file",
      source: "code",
      project_id: "p",
      path: "lycaon-den/src/components/search/Crossbar.tsx",
      source_ref: "lycaon-den/src/components/search/Crossbar.tsx",
      title: "Crossbar.tsx",
      context: "lycaon-den/src/components/search",
      score: 0.95,
    });
    expect(d.title).toBe("Crossbar.tsx");
    expect(d.context).toBe("lycaon-den/src/components/search");
    expect(d.kindLabel).toBe("File");
    expect(d.openLabel).toBe("Open");
    expect(d.openPath).toEqual({
      path: "lycaon-den/src/components/search/Crossbar.tsx",
    });
    expect(d.preview).toBeUndefined();
  });

  it("file hit at the repo root has no folder line", () => {
    const d = searchHitDisplay({
      hit_id: "file-root",
      hit_kind: "file",
      source: "code",
      project_id: "p",
      path: "go.mod",
      title: "go.mod",
      score: 0.95,
    });
    expect(d.title).toBe("go.mod");
    expect(d.context).toBeUndefined();
    expect(d.openPath).toEqual({ path: "go.mod" });
  });

  it("network hit with a chat home opens in chat", () => {
    const d = searchHitDisplay({
      hit_id: "network-1",
      hit_kind: "network",
      source: "tool",
      project_id: "p",
      session_id: "s1",
      source_ref: "msg_1#call_1",
      title: "connected to api.example.com",
      snippet: "connected to api.example.com",
      score: 1,
    });
    expect(d.kindLabel).toBe("Network");
    expect(d.openLabel).toBe("Open in chat");
  });

  it("prefers host tool title over snippet JSON walls", () => {
    const d = searchHitDisplay({
      hit_id: "tool-1",
      hit_kind: "tool",
      source: "message",
      project_id: "p",
      source_ref: "call_1",
      title: "read",
      context: "src/main.go",
      snippet: 'read {"path":"src/main.go","offset":0}',
      score: 1,
    });
    expect(d.title).toBe("read");
    expect(d.context).toBe("src/main.go");
    expect(d.preview?.format).toBe("prose");
    expect(d.preview?.text).toBe("Recorded input to read.\nsrc/main.go");
    expect(d.title).not.toContain("{");
  });

  it("summarizes tool results using host metadata instead of partial JSON", () => {
    const d = searchHitDisplay({
      hit_id: "tool-result-1",
      hit_kind: "tool",
      source: "tool",
      project_id: "p",
      source_ref: "call_1d883afc",
      title: "Terminal interface in use",
      handle: "read#25",
      snippet:
        '{"caption":"Terminal interface in use","markup":"<svg>"}',
    });
    expect(d.title).toBe("Terminal interface in use");
    expect(d.preview).toEqual({ format: "prose", text: "Recorded output from Terminal interface in use.\nEvidence: read#25" });
  });

  it("uses host code title and path context with open path", () => {
    const d = searchHitDisplay({
      hit_id: "message-1",
      hit_kind: "code",
      source: "code",
      project_id: "p",
      source_ref: "src/main.rs:42",
      path: "src/main.rs",
      line: 42,
      title: "fn main() {}",
      context: "src/main.rs:42",
      snippet: "fn main() {}",
    });
    expect(d.title).toBe("fn main() {}");
    expect(d.context).toBe("src/main.rs:42");
    expect(d.pivotPath).toBe("src/main.rs");
  });

  it("does not parade message UUIDs when host stamped the title", () => {
    const d = searchHitDisplay({
      hit_id: "code-1",
      hit_kind: "message",
      source: "message",
      project_id: "p",
      source_ref: "ae4f7348-1234-5678-9abc-def012345678",
      title: "Session layout with scrollbox",
      snippet: "Session layout with scrollbox\nMore detail here",
    });
    expect(d.title).toBe("Session layout with scrollbox");
    expect(d.context).toBeUndefined();
    expect(d.kindLabel).toBe("Message");
  });

  it("omits prose previews that only repeat the title", () => {
    const d = searchHitDisplay({
      hit_id: "finding-1",
      hit_kind: "finding",
      source: "finding",
      project_id: "p",
      source_ref: "scan-1",
      title: "Remote script piped to shell — supply-chain risk.",
      snippet: "Remote script piped to shell — supply-chain risk.",
    });
    expect(d.title).toContain("Remote script");
    expect(d.preview).toBeUndefined();
  });

  it.each([
    ['{"count":2}', "json", '{\n  "count": 2\n}'],
    ['{\\"count\\":2}', "json", '{\n  "count": 2\n}'],
    ['{"count":', "prose", '{"count":'],
  ])("formats the message preview %s", (snippet, format, text) => {
    const display = searchHitDisplay({
      hit_id: "message-json", hit_kind: "message", source: "message",
      project_id: "p", title: "Message", snippet,
    });
    expect(display.preview).toEqual({ format, text });
  });

  it("preserves JSON that is too deeply nested to format", () => {
    const snippet = "[".repeat(20_000) + "0" + "]".repeat(20_000);
    const display = searchHitDisplay({
      hit_id: "message-deep-json", hit_kind: "message", source: "message",
      project_id: "p", title: "Message", snippet,
    });
    expect(display.preview).toEqual({ format: "prose", text: snippet });
  });

  it("softTitle only ellipsizes extreme lengths", () => {
    expect(softTitle("short")).toBe("short");
    expect(softTitle("x".repeat(300)).endsWith("…")).toBe(true);
  });
});
