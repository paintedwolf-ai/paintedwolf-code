import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { SecurityOverview } from "../../api/types.ts";
import { SecurityCoverage } from "./SecurityCoverage.tsx";

const overview: SecurityOverview = {
  project_id: "proj-1",
  enabled: true,
  baseline: { snapshot_id: "snap-1", created_at: "2026-09-09T09:41:00Z", file_count: 10, unobserved_directories: 0 },
  scanners: [
    { id: "sast", label: "Static analysis", categories: ["sast"], available: true, running: false, watching: true, pass_superseded: false, last_full_at: new Date(Date.now() - 2 * 3600_000).toISOString() },
    { id: "secrets", label: "Secrets", categories: ["secret"], available: true, running: false, watching: true, pass_superseded: false },
    { id: "sca", label: "Supply chain", categories: ["sca"], available: false, unavailable_reason: "No lockfile in this project", running: false, watching: false, pass_superseded: false },
  ],
  introduced_since_baseline: 0,
  fixed_since_baseline: 0,
};

const runningOverview: SecurityOverview = {
  ...overview,
  running: {
    assessment_id: "a1",
    requested_at: "2026-09-09T11:00:00Z",
    started_at: "2026-09-09T11:00:00Z",
    members: [
      {
        scanner_id: "sast",
        phase: "started",
        scan: {
          id: "scan-a", categories: ["sast"], scanner_id: "sast", status: "running", long_running: false,
          findings_count: 0, created_at: "2026-09-09T11:00:00Z", progress: { chunks: 7, completed: 4, files: 12_702 },
        },
      },
      {
        scanner_id: "secrets",
        phase: "started",
        scan: {
          id: "scan-b", categories: ["secret"], scanner_id: "secrets", status: "complete", long_running: false,
          findings_count: 3, created_at: "2026-09-09T11:00:00Z",
        },
      },
    ],
  },
};

function mount(value: SecurityOverview, onStart = vi.fn(async () => undefined)) {
  const onToggle = vi.fn();
  render(() => (
    <SecurityCoverage
      projectId={value.project_id}
      overview={value}
      displayed={value.running ? { pass: value.running, live: true } : null}
      newSince={false}
      passSuperseded={false}
      onToggleNewSince={onToggle}
      onStart={onStart}
      starting={false}
    />
  ));
  return { onStart, onToggle };
}

describe("SecurityCoverage", () => {
  it.each([
    ["sast", "Code scanner"],
    ["sca", "Dependency scanner"],
    ["secret", "Secret scanner"],
    ["custom", "Scanner"],
  ] as const)("retains historical %s job labels after the scanner selection changes", (category, label) => {
    const pass = runningOverview.running!;
    const member = pass.members[0]!;
    mount({
      ...overview,
      scanners: [],
      last_full: {
        ...pass,
        members: [{ ...member, scanner_id: "retired-internal-id", scan: {
          ...member.scan!, scanner_id: "retired-internal-id", categories: [category], status: "complete",
        } }],
      },
    });
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    expect(screen.getByRole("progressbar").getAttribute("aria-label")).toBe(`${label} progress`);
    expect(screen.getByTestId("scans-full-scan-panel").textContent).not.toContain("retired-internal-id");
  });

  it("checks available scanners by default and explains unavailable ones", () => {
    mount(overview);
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    const sast = screen.getByTestId("scans-full-scan-scanner-sast") as HTMLInputElement;
    const secrets = screen.getByTestId("scans-full-scan-scanner-secrets") as HTMLInputElement;
    const sca = screen.getByTestId("scans-full-scan-scanner-sca") as HTMLInputElement;
    expect(sast.checked).toBe(true);
    expect(secrets.checked).toBe(true);
    expect(sca.checked).toBe(false);
    expect(sca.disabled).toBe(true);
    const panel = screen.getByTestId("scans-full-scan-panel");
    expect(panel.textContent).toContain("No lockfile in this project");
    // No next-run prediction; a quiet scanner is watching.
    expect(panel.textContent).toContain("Watching for changes · full pass 2h ago");
    expect(panel.textContent).toContain("not scanned yet");
    expect(panel.textContent).not.toContain("next");
  });

  it("starts with the checked scanner ids only", () => {
    const { onStart } = mount(overview);
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    fireEvent.click(screen.getByTestId("scans-full-scan-scanner-secrets"));
    fireEvent.click(screen.getByTestId("scans-full-scan-start"));
    expect(onStart).toHaveBeenCalledWith(["sast"]);
  });

  it("shows the running pass with progress and withdraws Start", () => {
    mount(runningOverview);
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    expect(screen.queryByTestId("scans-full-scan-scanners")).toBeNull();
    const readouts = screen.getAllByTestId("scans-full-scan-readout").map((el) => el.textContent);
    expect(readouts).toEqual(["4 of 7 chunks · 12,702 files", "Complete"]);
    const statuses = screen.getAllByTestId("scans-full-scan-status").map((el) => el.textContent);
    expect(statuses).toEqual(["Running", "Complete"]);
    const bars = screen.getAllByRole("progressbar");
    expect(bars[0]?.getAttribute("aria-valuenow")).toBe("57");
    expect(bars[1]?.getAttribute("aria-valuenow")).toBe("100");
    // Start is hidden while a pass runs.
    expect(screen.queryByTestId("scans-full-scan-start")).toBeNull();
    expect(screen.queryByText("Cancel")).toBeNull();
  });

  it("names the control by the pass on screen at one reserved width", () => {
    mount(overview);
    const label = screen.getByTestId("scans-full-scan-toggle-label");
    expect(label.textContent).toBe("Run full scan");
    const reserve = [...(label.parentElement?.querySelectorAll(".den-stable-label-sizer") ?? [])].map(
      (el) => el.textContent,
    );
    expect(reserve).toEqual(["Run full scan", "Full scan finished", "Full scan · 0 of 0 done"]);
  });

  it("discloses the running pass from a control that counts finished scanners", () => {
    mount(runningOverview);
    const toggle = screen.getByTestId("scans-full-scan-toggle");
    expect(toggle.getAttribute("data-pass")).toBe("running");
    expect(screen.getByTestId("scans-full-scan-toggle-label").textContent).toBe("Full scan · 1 of 2 done");
  });

  it("keeps a row for every scanner of a pass that is still waiting to start", () => {
    mount({
      ...runningOverview,
      running: {
        assessment_id: "a2",
        requested_at: "2026-09-09T11:00:00Z",
        members: [
          { scanner_id: "sast", phase: "waiting_for_scanner" },
          { scanner_id: "secrets", phase: "waiting_for_pass" },
        ],
      },
    });
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    const statuses = screen.getAllByTestId("scans-full-scan-status").map((el) => el.textContent);
    expect(statuses).toEqual(["Waiting", "Waiting"]);
    const readouts = screen.getAllByTestId("scans-full-scan-readout").map((el) => el.textContent);
    expect(readouts).toEqual(["Finishing an earlier scan first", "Waiting for the code scanner"]);
    for (const bar of screen.getAllByRole("progressbar")) {
      expect(bar.hasAttribute("aria-valuenow")).toBe(false);
    }
  });

  it("reports coverage through the chip and toggles New since", () => {
    const { onToggle } = mount({ ...overview, last_full: { assessment_id: "a0", requested_at: "t", completed_at: "2026-09-08T14:02:00Z", coverage_status: "bounded", members: [] } });
    const chip = screen.getByTestId("scans-coverage-chip");
    expect(chip.textContent).toBe("Bounded");
    expect(chip.getAttribute("data-state")).toBe("partial");
    fireEvent.click(screen.getByTestId("scans-new-since-chip"));
    expect(onToggle).toHaveBeenCalledTimes(1);
  });
});
