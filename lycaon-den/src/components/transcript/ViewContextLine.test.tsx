import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { ViewContext } from "../../api/types.ts";

const openSourceLocation = vi.hoisted(() => vi.fn(() => Promise.resolve({ status: "opened" })));
vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform/navigation/open-source.ts")>();
  return { ...actual, openSourceLocation };
});

import { ViewContextLine } from "./ViewContextLine.tsx";

const view: ViewContext = {
  editors: [
    { root_id: "root-a", path: "src/login.go", display_path: "src/login.go", unsaved: true, selection: { start_line: 42, end_line: 88, cursor: false } },
    { root_id: "root-b", path: "guide.md", display_path: "@docs/guide.md", unsaved: false, selection: { start_line: 7, end_line: 7, cursor: true } },
  ],
  diffs: [{ root_id: "root-a", path: "src/refresh.go", display_path: "src/refresh.go" }],
  folder: { root_id: "root-a", path: "src/auth", display_path: "src/auth" },
  search: { query: "LegacyToken", hit: { root_id: "root-a", path: "src/token.go", display_path: "src/token.go", line: 12 } },
  findings: [
    { fingerprint: "a", rule_id: "go.sqli", level: "high", location: { root_id: "root-a", path: "src/db.go", display_path: "src/db.go", line: 9 } },
    { fingerprint: "b", rule_id: "deps.vuln", level: "critical" },
  ],
};

describe("ViewContextLine", () => {
  it("names every place the agent was told about, in order", () => {
    render(() => <ViewContextLine view={view} projectId="project" />);
    expect(screen.getAllByTestId("transcript-view-context-item").map((item) => item.textContent)).toEqual([
      "src/login.go:42-88", "@docs/guide.md:7", "src/refresh.go", "src/auth", "src/token.go:12", "src/db.go:9",
    ]);
    expect(screen.getByTestId("transcript-view-context-search").textContent).toContain(view.search!.query);
    expect(screen.getAllByTestId("transcript-view-context-finding").map((item) => item.textContent)).toEqual([view.findings[1]!.rule_id]);
  });

  it("opens each place at its recorded lines", () => {
    render(() => <ViewContextLine view={view} projectId="project" />);
    const items = screen.getAllByTestId("transcript-view-context-item");
    fireEvent.click(items[0]!);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({ rootId: "root-a", path: "src/login.go", line: 42, endLine: 88 }));
    fireEvent.click(items[1]!);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({ rootId: "root-b", path: "guide.md", line: 7, endLine: undefined }));
    fireEvent.click(items[3]!);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({ path: "src/auth", entryKind: "folder" }));
  });

  it("states an empty view without naming places", () => {
    render(() => <ViewContextLine view={{ editors: [], diffs: [], findings: [] }} projectId="project" />);
    expect(screen.queryAllByTestId("transcript-view-context-item")).toHaveLength(0);
    expect(screen.getByTestId("transcript-view-context").textContent).not.toBe("");
  });
});
