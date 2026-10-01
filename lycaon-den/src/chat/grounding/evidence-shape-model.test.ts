import { describe, expect, it } from "vitest";
import {
  evidenceRecordDetailLines,
  evidenceShapeLabel,
  renderEvidenceRecordSummary,
  fidelityLabel,
} from "./evidence-shape-model.ts";

describe("evidence-shape-model", () => {
  it("labels opaque trust tier for reviewers", () => {
    expect(fidelityLabel("opaque")).toContain("no semantics claimed");
  });

  it("summarizes url shape", () => {
    expect(
      renderEvidenceRecordSummary({
        shape: "url",
        urls: ["https://example.com/doc"],
      }),
    ).toBe("https://example.com/doc");
  });

  it("summarizes a multi-result url record with a remainder count", () => {
    expect(
      renderEvidenceRecordSummary({
        shape: "url",
        urls: ["https://a.example/1", "https://b.example/2", "https://c.example/3"],
      }),
    ).toBe("https://a.example/1 +2 more");
  });

  it("lists every grounded url beyond the headline in detail lines", () => {
    expect(
      evidenceRecordDetailLines({
        shape: "url",
        tool: "web_search",
        urls: ["https://a.example/1", "https://b.example/2", "https://c.example/3"],
      }),
    ).toEqual(["Tool: web_search", "https://b.example/2", "https://c.example/3"]);
  });

  it("summarizes artifact shape with flagged path for scan evidence", () => {
    expect(
      renderEvidenceRecordSummary({
        shape: "artifact",
        kind: "scan",
        path: "internal/auth/handler.go",
        excerpt: "SQL injection via string concatenation",
        urls: [],
      }),
    ).toBe("internal/auth/handler.go");
  });

  it("summarizes unknown shape generically", () => {
    expect(
      renderEvidenceRecordSummary({
        shape: "future_shape",
        kind: "novel",
        excerpt: "body",
        urls: [],
      }),
    ).toContain("body");
    expect(evidenceShapeLabel("future_shape")).toBe("future shape");
  });
});
