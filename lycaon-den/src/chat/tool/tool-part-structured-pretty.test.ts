import { afterEach, describe, expect, it } from "vitest";
import type { ToolPartView } from "./tool-part-model.ts";
import {
  buildStructuredToolPresentation,
  resetStructuredToolPresentationCacheForTests,
} from "./tool-part-structured.ts";

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "tc-pretty",
    toolCallId: "tc-pretty",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "read",
    kind: "read",
    status: "completed",
    ...overrides,
  };
}

describe("tool-part-structured Pretty language and path hints", () => {
  afterEach(() => {
    resetStructuredToolPresentationCacheForTests();
  });

  it("scopes args.path grammar hints to read tools and sets rawOutputLanguage on JSON wire outputs", () => {
    const readRawView = buildStructuredToolPresentation(
      part({
        id: "read-raw-path",
        tool: "read",
        kind: "read",
        args: { path: "src/main.go" },
        output: "package main\nfunc main() {}\n",
      }),
    );
    const readContentSec = readRawView.sections.find((s) => s.kind === "content");
    expect(readContentSec && readContentSec.kind === "content" ? readContentSec.path : undefined).toBe("src/main.go");
    expect(readRawView.rawOutputPath).toBe("src/main.go");
    expect(readRawView.rawOutputLanguage).toBeUndefined();

    const readJsonView = buildStructuredToolPresentation(
      part({
        id: "read-json-path",
        tool: "read",
        kind: "read",
        args: { path: "src/main.go" },
        output: JSON.stringify({ path: "src/main.go", content: "package main\n" }),
      }),
    );
    const readJsonContentSec = readJsonView.sections.find((s) => s.kind === "content");
    expect(readJsonContentSec && readJsonContentSec.kind === "content" ? readJsonContentSec.path : undefined).toBe(
      "src/main.go",
    );
    expect(readJsonView.rawOutputPath).toBeUndefined();
    expect(readJsonView.rawOutputLanguage).toBe("json");

    const writeView = buildStructuredToolPresentation(
      part({
        id: "write-path",
        tool: "write_file",
        kind: "write",
        args: { path: "src/main.go" },
        output: "wrote 42 bytes\n",
      }),
    );
    const writeSec = writeView.sections.find((s) => s.kind === "content");
    expect(writeSec && writeSec.kind === "content" ? writeSec.path : undefined).toBeUndefined();
    expect(writeView.rawOutputPath).toBeUndefined();

    const genericJsonView = buildStructuredToolPresentation(
      part({
        id: "generic-json",
        tool: "custom_file_tool",
        kind: "generic",
        args: { path: "lib/app.py" },
        output: JSON.stringify({ ok: true, updated: 1 }),
      }),
    );
    expect(genericJsonView.rawOutputPath).toBeUndefined();
    expect(genericJsonView.rawOutputLanguage).toBe("json");

    const fallbackView = buildStructuredToolPresentation(
      part({
        id: "fallback-path",
        tool: "read",
        kind: "read",
        args: { file_path: "lib/app.py" },
        output: '{"content": "def f(): return 1"',
      }),
    );
    const recoveredSec = fallbackView.sections.find((s) => s.kind === "content");
    expect(recoveredSec && recoveredSec.kind === "content" ? recoveredSec.path : undefined).toBe("lib/app.py");
  });
});
