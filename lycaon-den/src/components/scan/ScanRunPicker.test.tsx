import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CodeScan } from "../../api/types.ts";
import { ScanRunPicker } from "./ScanRunPicker.tsx";

const SCANS: CodeScan[] = [
  {
    id: "scan-1",
    categories: ["sast"],
    scanner_id: "scanner-a",
    status: "complete",
    findings_count: 0,
    long_running: false,
    findings_stored: 0,
    findings_by_level: {},
    scan_scope: "repo",
    created_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "scan-2",
    categories: ["sca"],
    scanner_id: "scanner-b",
    status: "complete",
    findings_count: 1,
    long_running: false,
    findings_stored: 1,
    findings_by_level: { high: 1 },
    scan_scope: "repo",
    created_at: "2026-01-02T00:00:00Z",
  },
];

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

describe("ScanRunPicker", () => {
  it("opens from the keyboard and selects runs through listbox navigation", async () => {
    const onSelect = vi.fn();
    render(() => (
      <ScanRunPicker
        scans={SCANS}
        selectedId="scan-1"
        selectedScan={SCANS[0]!}
        onSelect={onSelect}
        runSortKey="date"
        runSortDir="desc"
        onRunSortKeyChange={vi.fn()}
        onRunSortDirToggle={vi.fn()}
        historyPage={0}
        onHistoryPageChange={vi.fn()}
        scannerLabels={{ "scanner-a": "Scanner A", "scanner-b": "Scanner B" }}
      />
    ));

    const trigger = screen.getByTestId("scans-run-picker");
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    const listbox = await screen.findByRole("listbox", { name: "Scan runs" });
    await waitFor(() => expect(document.activeElement).toBe(listbox));
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-0");

    fireEvent.keyDown(listbox, { key: "ArrowDown" });
    expect(listbox.getAttribute("aria-activedescendant")).toContain("option-1");
    fireEvent.keyDown(listbox, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("scan-2");
    expect(screen.queryByRole("listbox", { name: "Scan runs" })).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("restores trigger focus when Escape dismisses the menu", async () => {
    render(() => (
      <ScanRunPicker
        scans={SCANS}
        selectedId="scan-1"
        selectedScan={SCANS[0]!}
        onSelect={vi.fn()}
        runSortKey="date"
        runSortDir="desc"
        onRunSortKeyChange={vi.fn()}
        onRunSortDirToggle={vi.fn()}
        historyPage={0}
        onHistoryPageChange={vi.fn()}
        scannerLabels={{}}
      />
    ));
    const trigger = screen.getByTestId("scans-run-picker");
    fireEvent.click(trigger);
    const listbox = await screen.findByRole("listbox", { name: "Scan runs" });
    fireEvent.keyDown(listbox, { key: "Escape" });
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("stays open while a container that does not hold the trigger scrolls", async () => {
    let transcript: HTMLDivElement | undefined;
    let stage: HTMLDivElement | undefined;
    render(() => (
      <>
        <div ref={transcript} />
        <div ref={stage}>
          <ScanRunPicker
            scans={SCANS}
            selectedId="scan-1"
            selectedScan={SCANS[0]!}
            onSelect={vi.fn()}
            runSortKey="date"
            runSortDir="desc"
            onRunSortKeyChange={vi.fn()}
            onRunSortDirToggle={vi.fn()}
            historyPage={0}
            onHistoryPageChange={vi.fn()}
            scannerLabels={{}}
          />
        </div>
      </>
    ));

    fireEvent.click(screen.getByTestId("scans-run-picker"));
    const runs = await screen.findByRole("listbox", { name: "Scan runs" });

    fireEvent.scroll(transcript!);
    expect(runs.isConnected).toBe(true);

    fireEvent.scroll(stage!);
    await waitFor(() =>
      expect(screen.queryByRole("listbox", { name: "Scan runs" })).toBeNull(),
    );
  });

  it("dismisses only the nested dropdown on its first Escape", async () => {
    render(() => (
      <ScanRunPicker
        scans={SCANS}
        selectedId="scan-1"
        selectedScan={SCANS[0]!}
        onSelect={vi.fn()}
        runSortKey="date"
        runSortDir="desc"
        onRunSortKeyChange={vi.fn()}
        onRunSortDirToggle={vi.fn()}
        historyPage={0}
        onHistoryPageChange={vi.fn()}
        scannerLabels={{}}
      />
    ));

    fireEvent.click(screen.getByTestId("scans-run-picker"));
    const runs = await screen.findByRole("listbox", { name: "Scan runs" });
    fireEvent.click(screen.getByTestId("scans-history-sort"));
    const sort = await screen.findByRole("listbox", {
      name: "Sort scan history",
    });

    fireEvent.keyDown(sort, { key: "Escape" });

    await waitFor(() =>
      expect(
        screen.queryByRole("listbox", { name: "Sort scan history" }),
      ).toBeNull(),
    );
    expect(runs.isConnected).toBe(true);
  });
});
