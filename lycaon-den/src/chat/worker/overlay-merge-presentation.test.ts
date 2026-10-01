import { describe, expect, it } from "vitest";
import { buildStructuredToolPresentation } from "../tool/tool-part-structured.ts";

function overlaySections(output: string) {
  return buildStructuredToolPresentation({
    id: output, toolCallId: "promote", assistantMessageId: "assistant", messageId: "message",
    tool: "promote_overlay", kind: "generic", status: "completed", output, error: null,
  }).sections;
}

describe("overlay merge cards", () => {
  it("renders ready resolutions from merge-plan output", () => {
    const output = JSON.stringify({
      job_id: "job-a",
      merge_status: "pending",
      clean_paths: ["pkg/a.go"],
      ready_resolutions: [
        {
          path: "pkg/b.go",
          status: "conflict",
          conflict_tier: "line_shift",
          action: "keep_both",
          needs_review: false,
          advisory: "Land reconciled file",
        },
        {
          path: "pkg/c.go",
          status: "conflict",
          needs_review: true,
        },
      ],
    });
    const sections = overlaySections(output);
    const text = sections
      .map((s) => ("text" in s ? s.text : ""))
      .filter(Boolean)
      .join("\n");
    expect(text).toContain("Ready resolutions: 1 auto, 1 need review");
    expect(text).toContain("pkg/b.go");
  });

  it("falls back to path_status when no ready rows", () => {
    const output = JSON.stringify({
      job_id: "job-a",
      path_status: [
        { path: "main.go", status: "clean" },
        { path: "util.go", status: "conflict", conflict_tier: "overlapping_edit" },
      ],
    });
    const sections = overlaySections(output);
    const content = sections.find((s) => s.kind === "content" && "text" in s);
    expect(content && "text" in content ? content.text : "").toContain("main.go — clean");
    expect(content && "text" in content ? content.text : "").toContain("util.go — conflict");
  });

  it("parses host-prefixed promote event bodies", () => {
    const output = `[host:overlay-promote-event] ${JSON.stringify({
      job_id: "job-a",
      merge_status: "merged",
      applied: ["cli.js"],
    })}`;
    const sections = overlaySections(output);
    expect(sections.some((s) => s.kind === "facts")).toBe(true);
  });

  it("ignores braces in strings and trailing feedback", () => {
    const output = `[host:overlay-promote-event]\n${JSON.stringify({
      job_id: "job-a",
      merge_status: "pending",
      ready_resolutions: [
        { path: "cli.js", status: "conflict", advisory: "literal } brace" },
      ],
    })}\n>>> feedback {not-json}`;
    const sections = overlaySections(output);
    const text = sections
      .map((section) => ("text" in section ? section.text : ""))
      .join("\n");
    expect(text).toContain("literal } brace");
  });

  it("returns no sections for non-merge JSON", () => {
    expect(overlaySections(JSON.stringify({ ok: true }))).toEqual([]);
  });
});
