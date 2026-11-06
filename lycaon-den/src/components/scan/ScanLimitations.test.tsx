import { assertNamedControl } from "../../test/controls.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { CodeScan } from "../../api/types.ts";
import { ScanLimitations } from "./ScanLimitations.tsx";

const limited: CodeScan = {
  id: "scan", categories: ["sast"], status: "complete", long_running: false,
  findings_count: 0, created_at: "2026-09-05T00:00:00Z", coverage_status: "partial",
  warning_summary: [{ kind: "file_partial_semantics", count: 100, files: 100, rules: 0 }],
};

describe("ScanLimitations", () => {
  it("shows one summary and loads diagnostic locations only when requested", async () => {
    const loadFull = vi.fn(async (): Promise<CodeScan> => ({
      ...limited,
      warnings: Array.from({ length: 100 }, (_, i) => ({
        kind: "file_partial_semantics", file: `source-${i}.py`, start_line: 7,
      })),
    }));
    render(() => <ScanLimitations projectId="project" scan={limited} client={{ getCodeScan: loadFull }} />);
    const issuesTrigger = assertNamedControl(screen.getByTestId("scans-issues-trigger"), "button");
    expect(issuesTrigger.textContent).toContain("100");
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(loadFull).not.toHaveBeenCalled();
    fireEvent.click(issuesTrigger);
    await screen.findByText(/source-0\.py:7/);
    expect(loadFull).toHaveBeenCalledWith("project", "scan", "full");
    expect(screen.getAllByRole("listitem")).toHaveLength(21);
    fireEvent.click(assertNamedControl(screen.getByTestId("table-pager-next"), "button"));
    await screen.findByText(/source-20\.py:7/);
  });

  it("stays quiet for complete coverage", () => {
    render(() => <ScanLimitations projectId="project" scan={{ ...limited, coverage_status: "complete", warning_summary: [] }} client={{ getCodeScan: vi.fn() }} />);
    expect(screen.queryByTestId("scans-issues-trigger")).toBeNull();
  });
});
