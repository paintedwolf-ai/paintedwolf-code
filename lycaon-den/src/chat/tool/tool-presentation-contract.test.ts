import { describe, expect, it } from "vitest";
import {
  normalizeToolWireOutput,
  parseToolJsonObject,
  peelCompactionBanner,
  readablePresentationIssues,
  stripReadLineNumbers,
  violatesStructuredToolPresentation,
} from "./tool-presentation-contract.ts";
import { buildStructuredToolPresentation } from "./tool-part-structured.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import {
  TOOL_COMPACTION_FIXTURES,
  TOOL_SPILL_FIXTURES,
  TOOL_STRUCTURED_OUTPUT_FIXTURES,
} from "./tool-structured-output-fixtures.ts";

function part(tool: string, output: string): ToolPartView {
  return {
    id: `tc-${tool}`,
    toolCallId: `tc-${tool}`,
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool,
    kind: tool === "read" ? "read" : tool === "command" ? "command" : "generic",
    status: "completed",
    output,
    error: null,
  };
}

describe("tool presentation contract wire peel", () => {
  it("strips read line-number prefixes", () => {
    expect(stripReadLineNumbers("1\t# Title\n2\t\n3\tbody")).toBe(
      "# Title\n\nbody",
    );
  });

  it("normalizes compaction + verbatim head/tail + evidence handle", () => {
    const peeled = normalizeToolWireOutput(
      `[compacted tool_result — original ~100 tokens; map + verbatim head/tail below are the working set]\n\nverbatim head/tail:\n[read#1]\n{"path":"a.md","content":"1\\tok"}`,
    );
    expect(peeled.compactionBanner).toContain("compacted tool_result");
    expect(peeled.evidenceHandle).toBe("read#1");
    expect(peeled.body.trim().startsWith("{")).toBe(true);
  });

  it("preserves a verbatim marker inside ordinary output", () => {
    const body = "Source text\nverbatim head/tail:\nMore source text";
    expect(normalizeToolWireOutput(body)).toEqual({ compactionBanner: null, evidenceHandle: null, body });
  });

  it.each(["before", "after"])("preserves compacted JSON with a handle %s the banner", (position) => {
    const banner = "[compacted tool_result — retained pointer]";
    const handle = "root:child:read#2";
    const payload = { content: "Source mentions verbatim head/tail:\ninside a string" };
    const suffix = '\n>>> Feedback\nverbatim head/tail:\n{"unrelated":true}';
    const body = JSON.stringify(payload) + suffix;
    const prefix = position === "before" ? `[${handle}]\n${banner}` : `${banner}\n[${handle}]`;
    const output = `${prefix}\n${body}`;
    const normalized = normalizeToolWireOutput(output);
    expect(normalized).toEqual({ compactionBanner: banner, evidenceHandle: handle, body });
    expect(parseToolJsonObject(normalized.body)?.parsed).toEqual(payload);
    expect(buildStructuredToolPresentation(part("read", output)).sections)
      .toContainEqual(expect.objectContaining({ kind: "content", text: payload.content }));
  });

  it("requires a complete delimiter line after a compaction summary", () => {
    const banner = "[compacted tool_result — summary and excerpt]";
    const summary = "The summary mentions verbatim head/tail: inline.\n";
    const body = '[read#3]\n{"content":"retained excerpt"}';
    expect(normalizeToolWireOutput(`${banner}\n${summary}verbatim head/tail:\n${body}`))
      .toEqual({ compactionBanner: banner, evidenceHandle: "read#3", body: '{"content":"retained excerpt"}' });
    expect(normalizeToolWireOutput(`${banner}\n${summary}`).body).toBe(summary);
  });

  it("peels compaction banners before JSON body", () => {
    const banner = "[compacted tool_result — list page]";
    const peeled = peelCompactionBanner(
      `${banner}\n[find#1]\n{"results":[],"total_results":0}`,
    );
    expect(peeled.banner).toBe(banner);
    expect(peeled.body).toContain('{"results"');
  });

  it.each([
    "package main\nfunc main() {}",
    'const result = {"content":"source text"};',
    'Report: {"ok":true}',
    '{ "content": "ordinary incomplete text',
  ])("preserves braces in ordinary output: %s", (output) => {
    expect(parseToolJsonObject(output)).toBeNull();
    expect(buildStructuredToolPresentation(part("read", output)).sections)
      .toContainEqual(expect.objectContaining({ kind: "content", text: output }));
  });

  it("parses a leading object after its evidence prefix", () => {
    expect(parseToolJsonObject('[read#1]\n{"path":"main.go"}'))
      .toMatchObject({ parsed: { path: "main.go" } });
  });

  it("parses an overlay event after its evidence handle", () => {
    const output = '[promote_overlay#1]\n[host:overlay-promote-event] {"merge_status":"merged","applied":["main.go"]}';
    const view = buildStructuredToolPresentation(part("promote_overlay", output));
    expect(view.evidenceHandle).toBe("promote_overlay#1");
    expect(view.sections).toContainEqual({ kind: "content", text: "main.go", label: "Applied paths" });
  });

  it("keeps nested objects and escaped string braces inside the payload", () => {
    const payload = { rows: [{ content: 'const brace = "}"; \\ {', note: "\n>>> inside a string" }] };
    const jsonBody = JSON.stringify(payload);
    expect(parseToolJsonObject(`[read#1]\n${jsonBody}\nTrailing text`))
      .toEqual({ parsed: payload, jsonBody });
  });
});

describe("tool presentation contract compaction fixtures", () => {
  it.each(Object.entries(TOOL_COMPACTION_FIXTURES))(
    "structures compacted %s output readably",
    (name, output) => {
      const tool = name.startsWith("read_")
        ? "read"
        : name.startsWith("summarize_")
          ? "summarize"
          : "find";
      const view = buildStructuredToolPresentation(part(tool, output));
      expect(
        readablePresentationIssues(view, output),
        `${name} readability`,
      ).toEqual([]);
      expect(
        view.sections.some((section) => section.kind === "compaction"),
      ).toBe(true);
      expect(violatesStructuredToolPresentation(view, output)).toBeNull();
      if (name === "read_verbatim_head_tail") {
        const body = view.sections.find(
          (s) => s.kind === "content",
        );
        expect(body && body.kind === "content" ? body.text : "").toContain(
          "# Demo",
        );
        expect(body && body.kind === "content" ? body.text : "").not.toContain(
          '{"content"',
        );
        expect(body && body.kind === "content" ? body.text : "").not.toContain(
          "\\n",
        );
      }
    },
  );
});

describe("tool presentation contract catalog fixtures", () => {
  it("flags unstructured JSON fallback", () => {
    const output = "{}";
    const view = buildStructuredToolPresentation(part("state_query", output));
    expect(readablePresentationIssues(view, output)).toContain(
      "JSON payload fell through to unstructured fallback",
    );
  });

  it("accepts representative native tool fixtures", () => {
    for (const tool of ["grep", "find", "git_status", "summarize"] as const) {
      const output = TOOL_STRUCTURED_OUTPUT_FIXTURES[tool]!;
      const view = buildStructuredToolPresentation(part(tool, output));
      expect(readablePresentationIssues(view, output), tool).toEqual([]);
    }
  });

  it.each(Object.entries(TOOL_SPILL_FIXTURES))(
    "structures spill-path %s output readably",
    (name, output) => {
      const view = buildStructuredToolPresentation(part("promote_overlay", output));
      expect(readablePresentationIssues(view, output), `${name} readability`).toEqual(
        [],
      );
      const facts = view.sections.find((section) => section.kind === "facts");
      expect(
        facts?.kind === "facts"
          ? facts.facts.some((fact) => fact.hostDataSpill === true)
          : false,
      ).toBe(true);
    },
  );
});
