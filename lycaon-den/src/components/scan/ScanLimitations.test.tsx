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
    expect(screen.getByRole("button", { name: "Analysis issues · 100" })).toBeTruthy();
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(loadFull).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Analysis issues · 100" }));
    expect(await screen.findByText(/source-0.py:7/)).toBeTruthy();
    expect(loadFull).toHaveBeenCalledWith("project", "scan", "full");
    expect(screen.getAllByRole("listitem")).toHaveLength(21);
    expect(screen.queryByText(/source-20.py:7/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText(/source-20.py:7/)).toBeTruthy();
    expect(screen.queryByText(/source-0.py:7/)).toBeNull();
  });

  it("explains bounded coverage as a source budget, not an engine gap", () => {
    render(() => <ScanLimitations projectId="project" scan={{ ...limited, coverage_status: "bounded", warning_summary: [] }} client={{ getCodeScan: vi.fn() }} />);
    fireEvent.click(screen.getByTestId("scans-issues-trigger"));
    expect(screen.getByTestId("scans-limitations").textContent).toContain("too large for the source budget");
  });

  it("stays quiet for complete coverage", () => {
    render(() => <ScanLimitations projectId="project" scan={{ ...limited, coverage_status: "complete", warning_summary: [] }} client={{ getCodeScan: vi.fn() }} />);
    expect(screen.queryByTestId("scans-issues-trigger")).toBeNull();
  });
});
