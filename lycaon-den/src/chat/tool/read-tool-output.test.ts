import { describe, expect, it } from "vitest";
import {
  formatLogFormatLabel,
  formatTimeSpan,
  readLogDigestFromOutput,
} from "./read-tool-output.ts";

const SAMPLE = JSON.stringify({
  path: "var/log/app.log",
  mode: "outline",
  outline_kind: "log_digest",
  outline_source: "log",
  total_lines: 1200,
  truncation_banner: "Survey complete — use ranges at cluster anchors.",
  log_digest: {
    format: "json_lines",
    record_count: 500,
    parsed_count: 498,
    truncated: true,
    time_span: {
      start: "2026-06-01T10:00:00Z",
      end: "2026-06-01T11:00:00Z",
    },
    facets: [
      {
        key: "severity",
        values: [
          { value: "error", count: 12 },
          { value: "info", count: 400 },
        ],
      },
    ],
    clusters: [
      {
        template: "connection refused host=<HOST>",
        count: 8,
        severity: "error",
        first_line: 42,
        last_line: 99,
      },
    ],
  },
});

describe("readLogDigestFromOutput", () => {
  it("parses log_digest read outline JSON", () => {
    const view = readLogDigestFromOutput(SAMPLE);
    expect(view).not.toBeNull();
    expect(view?.path).toBe("var/log/app.log");
    expect(view?.digest.format).toBe("json_lines");
    expect(view?.digest.facets?.[0]?.key).toBe("severity");
    expect(view?.digest.clusters?.[0]?.first_line).toBe(42);
    expect(view?.truncationBanner).toContain("Survey complete");
  });

  it("returns null for symbol outlines", () => {
    expect(
      readLogDigestFromOutput(
        JSON.stringify({ outline_kind: "symbols", symbols: [] }),
      ),
    ).toBeNull();
  });
});

describe("formatTimeSpan", () => {
  it("formats RFC3339 timestamps for display", () => {
    const text = formatTimeSpan("2026-06-01T10:00:00Z", "2026-06-01T11:00:00Z");
    expect(text).toContain("2026-06-01");
    expect(text).toContain("→");
  });
});

describe("formatLogFormatLabel", () => {
  it("uses sentence case", () => {
    expect(formatLogFormatLabel("json_lines")).toBe("JSON lines");
  });
});
