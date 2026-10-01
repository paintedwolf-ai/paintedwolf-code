import { describe, expect, it } from "vitest";
import {
  normalizeMarkdownFences,
  prepareMarkdownSource,
  workerTranscriptMarkdownSource,
} from "./markdown-output.ts";

const sampleWorkerEnvelope = `<task job_id="job-1" child_session_id="child-1" state="complete">
  <summary>Short</summary>
  <task_result>## Done

- item one
- item two</task_result>
</task>`;

describe("normalizeMarkdownFences", () => {
  it("inserts newline before inline fence", () => {
    const raw = "File: game.py ```python\nprint(1)\n```";
    const out = normalizeMarkdownFences(raw);
    expect(out).toContain("File: game.py\n\n```python");
    expect(out).toContain("print(1)");
  });

  it("leaves triple backticks inside an inline code span untouched", () => {
    const raw = "outputs with backticks (e.g., ` ```json ... ``` `), stripping";
    expect(normalizeMarkdownFences(raw)).toBe(raw);
  });
});

describe("prepareMarkdownSource", () => {
  it("inserts space after hashes when model omits it", () => {
    expect(prepareMarkdownSource("##NoSpace")).toBe("## NoSpace");
  });

  it("inserts blank line before glued heading", () => {
    expect(prepareMarkdownSource("Intro\n## Title")).toBe("Intro\n\n## Title");
  });
});

describe("workerTranscriptMarkdownSource", () => {
  it("returns task_result markdown from completion XML", () => {
    expect(workerTranscriptMarkdownSource(sampleWorkerEnvelope)).toContain(
      "## Done",
    );
    expect(workerTranscriptMarkdownSource(sampleWorkerEnvelope)).toContain(
      "item one",
    );
  });

  it("passes through plain markdown when not XML", () => {
    const md = "# Title\n\nParagraph.";
    expect(workerTranscriptMarkdownSource(md)).toBe(md);
  });

  it("renders worker completion JSON as markdown", () => {
    const raw =
      '{"leg_status":"complete","files_modified":["src/a.go"],"objectives_met":["added handler"],"remaining_risk":[],"suggested_next_task":"verify tests","brief":"Done."}';
    const out = workerTranscriptMarkdownSource(raw);
    expect(out).toContain("## Complete");
    expect(out).toContain("Done.");
    expect(out).toContain("### Objectives met");
    expect(out).toContain("- added handler");
    expect(out).toContain("### Files modified");
    expect(out).toContain("- src/a.go");
    expect(out).toContain("### Suggested next");
    expect(out).toContain("verify tests");
    expect(out).not.toContain("leg_status");
  });

  it("renders fenced worker completion JSON as markdown", () => {
    const raw =
      '```json\n{"leg_status":"partial","brief":"Need review.","remaining_risk":["tests not run"]}\n```';
    const out = workerTranscriptMarkdownSource(raw);
    expect(out).toContain("## Partial");
    expect(out).toContain("Need review.");
    expect(out).toContain("- tests not run");
  });

  it("renders Harmony-prefixed envelope-only JSON as a Complete card", () => {
    const envelope =
      '{"leg_status":"complete","brief":"Done.","objectives_met":["landed"]}';
    for (const raw of [
      `final${envelope}`,
      `assistantfinal${envelope}`,
      `assistantassistant${envelope}`,
    ]) {
      const out = workerTranscriptMarkdownSource(raw);
      expect(out).toContain("## Complete");
      expect(out).toContain("Done.");
      expect(out).not.toContain("leg_status");
    }
  });

  it("leaves hybrid Harmony dumps as raw text", () => {
    const raw =
      "analysisWe attempted tea CLI but error." +
      'assistantcommentary to=functions.command json{"command":"curl http://localhost"}' +
      'assistantfinal{"leg_status":"complete","brief":"done"}';
    const out = workerTranscriptMarkdownSource(raw);
    expect(out).toBe(raw);
    expect(out).not.toContain("## Complete");
  });
});
