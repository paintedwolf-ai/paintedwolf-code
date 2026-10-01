import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { SecurityOverview } from "../../api/types.ts";
import type { DisplayedFullPass } from "../../lib/scan-coverage.ts";
import { SecurityEmptyState, emptyReasonFor } from "./SecurityEmptyState.tsx";

const justNow = new Date(Date.now() - 5_000).toISOString();

const unscanned: SecurityOverview = {
  project_id: "proj-1",
  enabled: true,
  scanners: [
    { id: "sast", label: "OpenGrep", categories: ["sast"], available: true, running: false, watching: false, pass_superseded: false },
  ],
  introduced_since_baseline: 0,
  fixed_since_baseline: 0,
};

const scanned: SecurityOverview = {
  ...unscanned,
  scanners: [
    { id: "sast", label: "OpenGrep", categories: ["sast"], available: true, running: false, watching: true, pass_superseded: false, last_full_at: justNow },
  ],
  last_full: {
    assessment_id: "a1",
    requested_at: justNow,
    started_at: justNow,
    completed_at: justNow,
    coverage_status: "complete",
    members: [
      {
        scanner_id: "sast",
        phase: "started",
        scan: { id: "scan-1", categories: ["sast"], scanner_id: "sast", status: "complete", long_running: false, findings_count: 0, created_at: justNow },
      },
    ],
  },
};

function mount(overview: SecurityOverview, displayed: DisplayedFullPass | null = null) {
  const reason = emptyReasonFor({
    enabled: true,
    filtersActive: false,
    running: displayed != null,
    fullPassCompleted: Boolean(overview.last_full?.completed_at),
    changesScanned: overview.scanners.some((scanner) => Boolean(scanner.last_completed_at)),
  });
  render(() => (
    <SecurityEmptyState
      reason={reason}
      overview={overview}
      displayed={displayed}
      starting={false}
      onStartFullScan={vi.fn()}
      onClearFilters={vi.fn()}
    />
  ));
  return screen.getByTestId("scans-empty");
}

describe("SecurityEmptyState", () => {
  it("asks for a full pass when none has completed", () => {
    const empty = mount(unscanned);
    expect(empty.dataset.reason).toBe("unscanned");
    expect(empty.textContent).toContain("Nothing scanned yet");
  });

  it("says a completed full pass reported nothing instead of claiming nothing ran", () => {
    const empty = mount(scanned);
    expect(empty.dataset.reason).toBe("clean");
    expect(empty.textContent).not.toContain("Nothing scanned yet");
    expect(empty.textContent).toContain("No findings reported");
    expect(empty.textContent).toContain("reported nothing. Changes since then are scanned");
    expect(screen.getByTestId("scans-empty-start").textContent).toBe("Run full scan again");
  });

  it("shows every scanner of a running pass", () => {
    const pass = { ...scanned.last_full!, completed_at: undefined, members: [{ scanner_id: "sast", phase: "waiting_for_scanner" as const }] };
    const empty = mount({ ...unscanned, running: pass }, { pass, live: true });
    expect(empty.dataset.reason).toBe("scanning");
    expect(empty.textContent).toContain("Scanning this project");
    expect(screen.getByTestId("scans-empty-progress").textContent).toContain("Finishing an earlier scan first");
  });

  it("keeps a pass that just finished on screen as finished", () => {
    const empty = mount(scanned, { pass: scanned.last_full!, live: false });
    expect(empty.dataset.reason).toBe("scanning");
    expect(empty.textContent).toContain("Full scan finished");
    expect(empty.textContent).not.toContain("Scanning this project");
    expect(screen.getByTestId("scans-full-scan-status").textContent).toBe("Complete");
  });

  it("qualifies a clean pass that did not cover the whole project", () => {
    const empty = mount({ ...scanned, last_full: { ...scanned.last_full!, coverage_status: "partial" } });
    expect(empty.textContent).toContain("did not cover the whole project");
  });

  it("says changed files were scanned when no full pass has run", () => {
    const empty = mount({
      ...unscanned,
      scanners: [{ ...unscanned.scanners[0]!, watching: true, last_completed_at: justNow }],
    });
    expect(empty.dataset.reason).toBe("changes_only");
    expect(empty.textContent).toContain("No full scan yet");
    expect(empty.textContent).toContain("Changed files have been scanned as they were written");
    expect(empty.textContent).not.toContain("Nothing scanned yet");
    expect(screen.getByTestId("scans-empty-start").textContent).toBe("Run full scan");
  });

  it("names the scanners a clean pass left out", () => {
    const secrets = { id: "secrets", label: "Gitleaks", categories: ["secret" as const], available: true, running: false, watching: true, pass_superseded: false };
    const sca = { id: "sca", label: "OSV-Scalibr", categories: ["sca" as const], available: true, running: false, watching: true, pass_superseded: false };
    const empty = mount({
      ...scanned,
      scanners: [...scanned.scanners, secrets, sca],
      last_full: {
        ...scanned.last_full!,
        coverage_status: "partial",
        members: [...scanned.last_full!.members, { scanner_id: "secrets", phase: "not_started" }],
      },
    });
    expect(empty.textContent).toContain("The secret scanner did not start.");
    expect(empty.textContent).toContain("The dependency scanner was not part of it.");
  });
});
