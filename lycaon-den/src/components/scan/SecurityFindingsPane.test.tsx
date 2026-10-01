import { resetSecurityFoldersForTests } from "../../platform/files/security-folder-selection.ts";
import { createSignal } from "solid-js";
import { at } from "../../test/at.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { SCANS_LIVE_REFRESH_MS } from "../../lib/scan-display.ts";
import { createAppStore } from "../../store/app-state.ts";
import { SecurityFindingsPane } from "./SecurityFindingsPane.tsx";

const addFindingsToChat = vi.hoisted(() => vi.fn());
vi.mock("./add-findings-to-chat.ts", () => ({
  addFindingsToChat: (...args: unknown[]) => addFindingsToChat(...args),
}));
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { resetLayoutStoreForTests } from "../../shell/layout-store.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScanEvent, CodeScan, SecurityFullPass, SecurityOverview, ScanQueryRequest } from "../../api/types.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";

vi.mock("../../platform/files/save-file.ts", () => ({
  downloadExport: vi.fn(async () => undefined),
}));

vi.mock("../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    persistAppState: vi.fn(async () => undefined),
    getAppStateSnapshot: vi.fn(() => ({ version: 1, recents: [] })),
  };
});

import { downloadExport } from "../../platform/files/save-file.ts";

const summaryScan: CodeScan = {
  id: "scan-1",
  categories: ["sast"],
  scanner_id: "opengrep-sast",
  status: "complete",
  findings_count: 2,
  long_running: false,
  findings_stored: 2,
  findings_by_level: { high: 1, medium: 1 },
  scan_scope: "repo",
  created_at: "2026-01-01T00:00:00Z",
  guidance: [
    {
      code: "SCAN_SQL",
      message: "Use parameterized queries",
      rule_id: "opengrep:sql",
      file: "app.rb",
      line: 9,
      fix: "Use parameterized queries",
    },
  ],
};

const fullScan: CodeScan = {
  ...summaryScan,
  findings: [
    {
      rule_id: "opengrep:sql",
      level: "high",
      message: "SQL concat",
      locations: [{ uri: "app.rb", start_line: 9 }],
      fingerprints: { primary: "fp-one" },
      tool: { driver_id: "opengrep", name: "OpenGrep" },
      properties: { lycaon: { hint_code: "SCAN_SQL" } },
    },
    {
      rule_id: "gitleaks:token",
      level: "medium",
      message: "Secret token",
      locations: [{ uri: ".env", start_line: 3 }],
      fingerprints: { primary: "fp-two" },
      tool: { driver_id: "gitleaks", name: "Gitleaks" },
      properties: { lycaon: { hint_code: "SCAN_SECRET" } },
    },
  ],
  guidance: [
    {
      code: "SCAN_SQL",
      message: "Use parameterized queries",
      rule_id: "opengrep:sql",
      file: "app.rb",
      line: 9,
      fix: "Use parameterized queries",
    },
  ],
};

const emptyLedgerCounts = {
  open: 0,
  reopened: 0,
  fixed: 0,
  not_observed: 0,
  unverified: 0,
  ignored: 0,
};

const completeOverview: SecurityOverview = {
  project_id: "proj-1",
  enabled: true,
  baseline: { snapshot_id: "snap-1", created_at: "2026-01-01T09:41:00Z", file_count: 10, unobserved_directories: 0 },
  last_full: { assessment_id: "a0", requested_at: "2026-01-02T13:00:00Z", started_at: "2026-01-02T13:00:00Z", completed_at: "2026-01-02T14:02:00Z", coverage_status: "complete", members: [] },
  scanners: [
    { id: "opengrep-sast", label: "Static analysis", categories: ["sast"], available: true, running: false, watching: true, pass_superseded: false },
    { id: "gitleaks", label: "Secrets", categories: ["secret"], available: true, running: false, watching: true, pass_superseded: false },
  ],
  introduced_since_baseline: 0,
  fixed_since_baseline: 0,
};

const incrementalOverview: SecurityOverview = { ...completeOverview, last_full: undefined };

const historyFindings = (fullScan.findings ?? []).map((finding, index) => ({
  ...finding,
  history: { introduced_at: index === 0 ? "2026-01-01T10:00:00Z" : "2025-12-31T10:00:00Z" },
}));

function mockClient() {
  return {
    getProjectSecurity: vi.fn<LycaonClient["getProjectSecurity"]>(async () => completeOverview),
    startFullScan: vi.fn<LycaonClient["startFullScan"]>(async () => ({ passes: [], overview: completeOverview })),
    listCodeScans: vi.fn<LycaonClient["listCodeScans"]>(async () => ({ scans: [summaryScan] })),
    listProjectScanners: vi.fn(async () => ({
      scanners: [
        { id: "lycaon-sast", label: "Static analysis" },
        { id: "lycaon-secrets", label: "Secrets" },
        { id: "lycaon-sca", label: "Supply chain" },
      ],
      rejected: [],
    })),
    getCodeScan: vi.fn(async (...args: string[]) =>
      args.includes("full") ? fullScan : summaryScan,
    ),
    queryCodeScan: vi.fn<LycaonClient["queryCodeScan"]>(async () => ({
      scan_id: "scan-1",
      findings: fullScan.findings,
      guidance: fullScan.guidance ?? [],
      total_match: fullScan.findings?.length ?? 0,
    })),
    exportCodeScanSARIF: vi.fn(async () => ({
      blob: new Blob(['{"version":"2.1.0"}'], { type: "application/sarif+json" }),
      filename: "scan-1.sarif.json",
    })),
    queryProjectFindings: vi.fn<LycaonClient["queryProjectFindings"]>(async () => ({
      project_id: "proj-1",
      entries: [],
      counts: emptyLedgerCounts,
      by_level: {},
      total_match: 0,
      offset: 0,
    })),
    listProjectFindingIgnores: vi.fn<LycaonClient["listProjectFindingIgnores"]>(async () => ({
      project_id: "proj-1",
      path: "/tmp/proj/.paintedwolf/ignores.yaml",
      rules: [],
    })),
    createProjectFindingIgnore: vi.fn<LycaonClient["createProjectFindingIgnore"]>(async () => {
      throw new Error("not used");
    }),
    deleteProjectFindingIgnore: vi.fn<LycaonClient["deleteProjectFindingIgnore"]>(async () => {
      throw new Error("not used");
    }),
    exportProjectFindings: vi.fn<LycaonClient["exportProjectFindings"]>(async () => ({
      blob: new Blob(["{}"], { type: "application/json" }),
      filename: "findings.sarif",
    })),
  };
}

let client: ReturnType<typeof mockClient>;

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return {
    ...actual,
    getLycaonClient: () => client,
  };
});

async function selectRunsView() {
  fireEvent.click(await screen.findByTestId("scans-overflow-trigger"));
  fireEvent.click(await screen.findByTestId("ledger-open-runs"));
  // Opening the run view starts its queries.
  await screen.findByTestId("scans-run-picker");
}

async function renderPane(
  props: {
    projectId?: string;
    latestScanId?: string;
    liveScan?: CodeScanEvent;
    repoRoot?: string;
    view?: "ledger" | "runs";
  } = {},
) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  render(() => (
    <SecurityFindingsPane
      appStore={appStore}
      projectId={props.projectId ?? "proj-1"}
      latestScanId={props.latestScanId ?? "scan-1"}
      liveScan={props.liveScan}
      repoRoot={props.repoRoot}
    />
  ));
  if ((props.view ?? "runs") === "runs") await selectRunsView();
  return appStore;
}

function clickChip(testId: string) {
  const chip = screen.getByTestId(testId);
  const btn = chip.querySelector("button");
  fireEvent.click(btn ?? chip);
}

describe("SecurityFindingsPane", () => {
  beforeEach(() => {
    resetSecurityFoldersForTests();
    client = mockClient();
    // Layout preferences are shared across component instances.
    resetLayoutStoreForTests();
  });

  it("isolates folder coverage and discards a late response from the previous folder", async () => {
    let resolvePrimary!: (value: SecurityOverview) => void;
    const [live, setLive] = createSignal<CodeScanEvent>();
    client.getProjectSecurity.mockResolvedValue(completeOverview);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const [roots, setRoots] = createSignal([
      { id: "root-primary", path: "/fixture/primary", label: "Primary fixture", is_primary: true, added_at: "2026-01-01T00:00:00Z", kind: "attached" as const },
      { id: "root-secondary", path: "/fixture/secondary", label: "Secondary fixture", is_primary: false, added_at: "2026-01-01T00:00:00Z", kind: "attached" as const },
    ]);
    render(() => <SecurityFindingsPane appStore={appStore} projectId="proj-1" roots={roots()} liveScan={live()} />);
    await screen.findByRole("button", { name: "Security folder" });
    client.getProjectSecurity.mockImplementation((_project, rootId) => rootId === "root-primary"
      ? new Promise((resolve) => { resolvePrimary = resolve; })
      : Promise.resolve({ ...completeOverview, coverage_status: "bounded" }));
    setLive({ scan_id: "scan-1", categories: ["sast"], status: "complete", findings_count: 2, long_running: false });
    await waitFor(() => expect(resolvePrimary).toBeTypeOf("function"));
    fireEvent.click(screen.getByRole("button", { name: "Security folder" }));
    fireEvent.click(screen.getByRole("option", { name: "Secondary fixture" }));
    await waitFor(() => expect(client.getProjectSecurity).toHaveBeenCalledWith("proj-1", "root-secondary"));
    await selectRunsView();
    await waitFor(() => expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Bounded"));
    expect(client.queryProjectFindings.mock.calls.at(-1)?.[2]).toBe("root-secondary");
    expect(client.listCodeScans.mock.calls.at(-1)?.[1]?.root_id).toBe("root-secondary");
    resolvePrimary({ ...completeOverview, coverage_status: "complete" });
    await Promise.resolve();
    expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Bounded");
    setRoots((previous) => previous.slice(0, 1));
    await waitFor(() => expect(client.getProjectSecurity.mock.calls.at(-1)?.[1]).toBe("root-primary"));
    expect(screen.queryByRole("button", { name: "Security folder" })).toBeNull();
  });

  it("keeps the displayed ledger and coverage when the project reloads the same folders", async () => {
    client.queryProjectFindings.mockResolvedValue({
      project_id: "proj-1",
      entries: [],
      counts: { ...emptyLedgerCounts, open: 3, ignored: 1 },
      by_level: {},
      total_match: 3,
    });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const folder = () => ({ id: "root-primary", path: "/fixture/primary", label: "Primary fixture", is_primary: true, added_at: "2026-01-01T00:00:00Z", kind: "attached" as const });
    const [roots, setRoots] = createSignal([folder()]);
    render(() => <SecurityFindingsPane appStore={appStore} projectId="proj-1" roots={roots()} />);
    await waitFor(() => expect(screen.getByTestId("ledger-tab-open").textContent).toBe("Open 3"));
    await screen.findByTestId("scans-coverage-chip");
    const reads = client.getProjectSecurity.mock.calls.length + client.queryProjectFindings.mock.calls.length;

    setRoots([folder()]);

    expect(screen.getByTestId("ledger-tab-open").textContent).toBe("Open 3");
    expect(screen.getByTestId("scans-coverage-chip")).toBeTruthy();
    await Promise.resolve();
    expect(client.getProjectSecurity.mock.calls.length + client.queryProjectFindings.mock.calls.length).toBe(reads);
  });

  it("waits through queued and running states before loading findings and fixes", async () => {
    let current: CodeScan = { ...summaryScan, status: "pending", findings_count: 0, findings_stored: 0 };
    client.getProjectSecurity.mockResolvedValue(incrementalOverview);
    client.listCodeScans.mockImplementation(async () => ({ scans: [current] }));
    client.getCodeScan.mockImplementation(async () => current);
    const [event, setEvent] = createSignal<CodeScanEvent>();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <SecurityFindingsPane appStore={appStore} projectId="proj-1" liveScan={event()} />);
    await selectRunsView();
    await screen.findByText("Scan queued");
    fireEvent.click(screen.getByTestId("scans-new-since-chip"));
    expect(client.queryCodeScan).not.toHaveBeenCalled();
    expect(screen.queryByTestId("scans-counts")).toBeNull();
    expect(screen.queryByTestId("scans-error")).toBeNull();
    current = { ...current, status: "running", progress: { completed: 1, chunks: 3, files: 20 } };
    setEvent({ scan_id: current.id, categories: current.categories, status: current.status, findings_count: 0, long_running: false });
    await screen.findByText("Scan running");
    expect(screen.getByTestId("scans-empty").textContent).toContain("1 of 3 chunks");
    expect(client.queryCodeScan).not.toHaveBeenCalled();
    current = summaryScan;
    setEvent({ scan_id: current.id, categories: current.categories, status: "complete", findings_count: 2, long_running: false });
    await waitFor(() => expect(client.queryCodeScan).toHaveBeenCalledWith("proj-1", "scan-1", {
      limit: 500, fixed_since_at: "2026-01-01T09:41:00Z",
    }));
    expect(screen.queryByTestId("scans-error")).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry loading" })).toBeNull();
  });

  it.each([
    ["failed", "Scan failed"], ["timed_out", "Scan timed out"],
    ["canceled", "Scan canceled"], ["superseded", "Scan superseded"],
  ] as const)("shows %s without querying nonexistent results", async (status, title) => {
    const stopped: CodeScan = { ...summaryScan, status, error: "Recorded scanner outcome" };
    client.listCodeScans.mockResolvedValue({ scans: [stopped] });
    client.getCodeScan.mockResolvedValue(stopped);
    await renderPane();
    await screen.findByText(title);
    expect(screen.getByTestId("scans-empty").textContent).toContain("Recorded scanner outcome");
    expect(client.queryCodeScan).not.toHaveBeenCalled();
    expect(screen.queryByTestId("scans-counts")).toBeNull();
    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    expect(screen.getByTestId("scans-export-run-sarif").hasAttribute("disabled")).toBe(true);
  });

  it("polls a selected run after it leaves the history page and recovers a missed completion event", async () => {
    vi.useFakeTimers();
    try {
      let current: CodeScan = { ...summaryScan, status: "running", findings_count: 0, findings_stored: 0 };
      client.listCodeScans.mockResolvedValueOnce({ scans: [current] })
        .mockResolvedValue({ scans: [{ ...summaryScan, id: "newer" }] });
      client.getCodeScan.mockImplementation(async () => current);
      await renderPane({ liveScan: { scan_id: current.id, categories: current.categories, status: "running", findings_count: 0, long_running: false } });
      await vi.advanceTimersByTimeAsync(0);
      expect(client.queryCodeScan).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(SCANS_LIVE_REFRESH_MS);
      current = summaryScan;
      await vi.advanceTimersByTimeAsync(SCANS_LIVE_REFRESH_MS * 2);
      expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2);
      expect(client.queryCodeScan).toHaveBeenCalledTimes(1);
      const reads = client.getCodeScan.mock.calls.length;
      await vi.advanceTimersByTimeAsync(60_000);
      expect(client.getCodeScan).toHaveBeenCalledTimes(reads);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps run status available when its initial findings read fails", async () => {
    client.queryCodeScan.mockRejectedValueOnce(new Error("Findings unavailable"));
    await renderPane();
    await screen.findByText("Findings unavailable");
    expect(screen.getByTestId("scans-run-picker").textContent).toContain("Completed");
    expect(screen.queryByTestId("scans-counts")).toBeNull();
    expect(screen.queryByTestId("scans-empty")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Retry loading" }));
    await screen.findAllByTestId("scans-finding-row");
    expect(screen.queryByTestId("scans-error")).toBeNull();
  });

  it("reports and retries missing coverage without claiming scanning has not started", async () => {
    client.getProjectSecurity.mockRejectedValueOnce(new Error("Coverage unavailable"));
    await renderPane();
    await screen.findByText("Coverage unavailable");
    expect(screen.getByTestId("scans-coverage-line").textContent).toBe("Coverage has not loaded.");
    expect(screen.queryByTestId("scans-coverage-chip")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Retry loading" }));
    await waitFor(() => expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Complete"));
    expect(screen.queryByTestId("scans-error")).toBeNull();
  });

  it("shows findings without introduction history before any full pass", async () => {
    client.getProjectSecurity.mockResolvedValue(incrementalOverview);
    await renderPane();
    await screen.findAllByTestId("scans-finding-row");
    expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2);
    expect(screen.getByTestId("scans-new-since-chip").getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(screen.getByTestId("scans-new-since-chip"));
    await screen.findByTestId("scans-filter-empty");
    expect(screen.getByTestId("scans-history-unavailable").textContent).toContain("Introduction time is unavailable for 2 findings");
  });

  it("never shows a previous run's findings under a queued selection", async () => {
    const pending: CodeScan = { ...summaryScan, id: "queued", status: "pending", findings_count: 0, findings_stored: 0 };
    client.listCodeScans.mockResolvedValue({ scans: [summaryScan, pending] });
    let finish!: (value: CodeScan) => void;
    client.getCodeScan.mockImplementation(async (...args: string[]) =>
      args.includes("queued")
        ? new Promise<CodeScan>((resolve) => { finish = resolve; }) : summaryScan);
    await renderPane();
    await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(screen.getByTestId("scans-run-picker"));
    fireEvent.click(screen.getAllByTestId("scans-run-card")[1]!);
    await waitFor(() => expect(client.getCodeScan).toHaveBeenCalledWith("proj-1", "queued", "summary"));
    expect(screen.queryByTestId("scans-finding-row")).toBeNull();
    finish(pending);
    await screen.findByText("Scan queued");
    expect(screen.queryByTestId("scans-finding-row")).toBeNull();
    expect(client.queryCodeScan.mock.calls.every((args) => args.includes("scan-1"))).toBe(true);
  });

  it("suspends selected-run polling while inactive and reads completion on return", async () => {
    vi.useFakeTimers();
    try {
      let current: CodeScan = { ...summaryScan, status: "running", findings_count: 0, findings_stored: 0 };
      client.listCodeScans.mockImplementation(async () => ({ scans: [current] }));
      client.getCodeScan.mockImplementation(async () => current);
      const [presence, setPresence] = createSignal<"active" | "idle">("active");
      const appStore = createAppStore();
      appStore.actions.setSidecarStatus("connected");
      render(() => <ResidentPresenceProvider presence={presence()}>
        <SecurityFindingsPane appStore={appStore} projectId="proj-1" />
      </ResidentPresenceProvider>);
      await vi.advanceTimersByTimeAsync(0);
      await selectRunsView();
      setPresence("idle");
      await vi.advanceTimersByTimeAsync(0);
      const reads = client.getCodeScan.mock.calls.length;
      await vi.advanceTimersByTimeAsync(60_000);
      expect(client.getCodeScan).toHaveBeenCalledTimes(reads);
      current = summaryScan;
      setPresence("active");
      await vi.advanceTimersByTimeAsync(0);
      expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("refreshes coverage on an externally started full pass and keeps the final scanner outcome", async () => {
    const [event, setEvent] = createSignal<CodeScanEvent>();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <SecurityFindingsPane appStore={appStore} projectId="proj-1" liveScan={event()} />);
    await selectRunsView();
    await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    const pending: CodeScan = { ...summaryScan, id: "external", status: "pending", findings_count: 0, findings_stored: 0 };
    const pass: SecurityFullPass = {
      assessment_id: "external-pass", requested_at: "2026-01-03T10:00:00Z", started_at: "2026-01-03T10:00:00Z",
      members: [{ scanner_id: pending.scanner_id ?? "", phase: "started", scan: pending }],
    };
    client.getProjectSecurity.mockResolvedValue({ ...completeOverview, running: pass });
    setEvent({ scan_id: pending.id, categories: pending.categories, status: pending.status, findings_count: 0, long_running: false });
    await waitFor(() => expect(screen.getByTestId("scans-full-scan-status").textContent).toBe("Pending"));
    expect((await screen.findByRole("progressbar")).hasAttribute("aria-valuenow")).toBe(false);
    client.getProjectSecurity.mockResolvedValue({ ...completeOverview, coverage_status: "unavailable",
      last_full: { ...pass, completed_at: "2026-01-03T10:01:00Z", coverage_status: "unavailable",
        members: [{ scanner_id: pending.scanner_id ?? "", phase: "started", scan: { ...pending, status: "timed_out" } }] } });
    setEvent({ scan_id: pending.id, categories: pending.categories, status: "timed_out", findings_count: 0, long_running: false });
    await waitFor(() => expect(screen.getByTestId("scans-full-scan-status").textContent).toBe("Timed out"));
    expect(screen.getByTestId("scans-coverage-line").textContent).toContain("no scanner established coverage");
    expect((screen.getByTestId("scans-full-scan-start") as HTMLButtonElement).disabled).toBe(false);
  });

  it("keeps a late full-scan action failure in its original project", async () => {
    client.getProjectSecurity.mockImplementation(async (projectId) => ({ ...completeOverview, project_id: projectId }));
    let fail!: (error: Error) => void;
    client.startFullScan.mockImplementationOnce(() => new Promise((_resolve, reject) => { fail = reject; }));
    const [projectId, setProjectId] = createSignal("proj-1");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <SecurityFindingsPane appStore={appStore} projectId={projectId()} />);
    await selectRunsView();
    await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    fireEvent.click(screen.getByTestId("scans-full-scan-start"));
    await waitFor(() => expect(client.startFullScan).toHaveBeenCalled());
    setProjectId("proj-2");
    await waitFor(() => expect(client.getProjectSecurity).toHaveBeenCalledWith("proj-2"));
    expect(screen.queryByTestId("scans-full-scan-panel")).toBeNull();
    fail(new Error("Previous project scan failed"));
    await selectRunsView();
    fireEvent.click(await screen.findByTestId("scans-full-scan-toggle"));
    await waitFor(() => expect((screen.getByTestId("scans-full-scan-start") as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByTestId("scans-error")).toBeNull();
  });

  it("pages and sorts history on the server while preserving the selected detail", async () => {
    const older = { ...summaryScan, id: "scan-older", scanner_id: "older-engine", created_at: "2025-01-01T00:00:00Z" };
    const list = vi.fn<LycaonClient["listCodeScans"]>(async (_project, query) => query?.cursor
      ? { scans: [older] }
      : { scans: [summaryScan], next_cursor: "older-page" });
    client.listCodeScans = list;
    await renderPane();
    await screen.findAllByTestId("scans-finding-row");
    expect(list).toHaveBeenLastCalledWith("proj-1", { limit: 6, cursor: "", sort: "date", order: "desc" });
    const detailReads = client.getCodeScan.mock.calls.length;
    fireEvent.click(screen.getByTestId("scans-run-picker"));
    fireEvent.click(screen.getByTestId("scans-history-pager-next"));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith("proj-1", { limit: 6, cursor: "older-page", sort: "date", order: "desc" }));
    await waitFor(() => expect(screen.getByTestId("scans-history").textContent).toContain("older-engine"));
    expect(client.getCodeScan.mock.calls).toHaveLength(detailReads);
    expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2);
    fireEvent.click(screen.getByTestId("scans-history-sort"));
    fireEvent.click(await screen.findByRole("option", { name: "Engine ID" }));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith("proj-1", { limit: 6, cursor: "", sort: "engine", order: "asc" }));
    expect(client.listProjectScanners).toHaveBeenCalledTimes(1);
  });

  it("publishes counts and findings together after both reads settle", async () => {
    let finish!: (value: Awaited<ReturnType<typeof client.queryCodeScan>>) => void;
    client.queryCodeScan.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await renderPane();
    await waitFor(() => expect(client.getCodeScan).toHaveBeenCalled());
    expect(screen.queryByTestId("scans-counts")).toBeNull();
    expect(screen.queryByText("No findings in this run.")).toBeNull();
    finish({ scan_id: "scan-1", findings: fullScan.findings, guidance: [], total_match: 2 });
    await waitFor(() => expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2));
    expect(screen.getByTestId("scans-counts").textContent).toContain("2 findings");
  });

  it("retains a successful scan when reactivation cannot refresh its findings", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <ResidentPresenceProvider presence={presence()}>
      <SecurityFindingsPane appStore={appStore} projectId="proj-1" latestScanId="scan-1" />
    </ResidentPresenceProvider>);
    await selectRunsView();
    const rows = await screen.findAllByTestId("scans-finding-row");
    client.queryCodeScan.mockRejectedValueOnce(new Error("Findings unavailable"));
    setPresence("idle");
    setPresence("active");
    await waitFor(() => expect(screen.getByTestId("scans-error").textContent).toContain("Findings unavailable"));
    expect(screen.getAllByTestId("scans-finding-row")[0]).toBe(rows[0]);
    expect(screen.queryByText("No findings in this run.")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Retry loading" }));
    await waitFor(() => expect(screen.queryByTestId("scans-error")).toBeNull());
  });

  it("keeps partial-scan findings usable when diagnostic loading fails", async () => {
    const partial: CodeScan = { ...summaryScan, coverage_status: "partial",
      warning_summary: [{ kind: "file_partial_semantics", count: 14, files: 3, rules: 0 }] };
    client.getCodeScan.mockImplementation(async (...args: string[]) => {
      if (args.includes("full")) throw new Error("Diagnostic service unavailable");
      return partial;
    });
    await renderPane();
    const rows = await screen.findAllByTestId("scans-finding-row");
    expect(screen.queryByTestId("scans-limitations")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Analysis issues · 14" }));
    await screen.findByText("Could not load diagnostics. Available findings are unaffected.");
    expect(screen.getAllByTestId("scans-finding-row")[0]).toBe(rows[0]);
    fireEvent.click(screen.getByRole("button", { name: "Close analysis issues" }));
    fireEvent.click(at(rows, 0));
    expect(await screen.findByTestId("scans-drill-down")).toBeTruthy();
    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    const exportItem = screen.getByTestId("scans-export-run-sarif");
    expect(exportItem.hasAttribute("disabled")).toBe(false);
    fireEvent.click(exportItem);
    await waitFor(() => expect(client.exportCodeScanSARIF).toHaveBeenCalledWith("proj-1", "scan-1"));
    expect(screen.queryByTestId("scans-empty")).toBeNull();
  });

  it.each(["partial", "bounded", "unavailable"] as const)("qualifies empty results for %s coverage", async (coverage) => {
    client.getCodeScan.mockResolvedValue({ ...summaryScan, findings_count: 0,
      findings_stored: 0, findings_by_level: {}, coverage_status: coverage });
    client.queryCodeScan.mockResolvedValue({ scan_id: "scan-1", findings: [], guidance: [], total_match: 0 });
    await renderPane();
    const empty = await screen.findByTestId("scans-empty");
    expect(empty.textContent).toContain(coverage === "unavailable" ? "No findings available" : "No findings in analyzed code");
    expect(screen.queryByText("No findings in this run.")).toBeNull();
    expect(screen.getByTestId("scans-issues-trigger")).toBeTruthy();
    expect(screen.queryByTestId("scans-limitations")).toBeNull();
  });

  it("renders severity chips and finding rows from locations", async () => {
    await renderPane();
    await waitFor(() => {
      expect(screen.getByTestId("scans-severity-chip-high").textContent).toContain(
        "High",
      );
      expect(screen.getByTestId("scans-severity-chip-high").textContent).toContain(
        "1",
      );
    });
    expect(screen.getByTestId("scans-counts").textContent).toContain(
      "2 findings",
    );
    expect(screen.getByTestId("scans-scope").textContent).toContain("repo");
    const rows = await screen.findAllByTestId("scans-finding-row");
    expect(at(rows, 0).textContent).toContain("app.rb:9");
    expect(at(rows, 0).textContent).toContain("SCAN_SQL");
  });

  it("adds a historical finding with its scan identity to chat", async () => {
    addFindingsToChat.mockReset().mockResolvedValue({ ok: true });
    await renderPane();
    fireEvent.click(at(await screen.findAllByTestId("scans-finding-row"), 0));
    fireEvent.click(await screen.findByTestId("scans-add-to-chat"));
    await vi.waitFor(() => expect(addFindingsToChat).toHaveBeenCalledWith("proj-1", [{
      finding: fullScan.findings![0],
      last_scan_id: "scan-1",
    }]));
  });

  it("drill-down shows rule, fingerprint, advisory fields and the hint overlay", async () => {
    await renderPane();
    const row = await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(at(row, 0));
    const drill = await screen.findByTestId("scans-drill-down");
    expect(drill.textContent).toContain("opengrep:sql");
    expect(drill.textContent).toContain("fp-one");
    expect(drill.textContent).toContain("parameterized");
    fireEvent.click(screen.getByTestId("scans-hints-toggle"));
    expect(screen.getByTestId("scans-hints-snippet").textContent).toContain("SCAN_SQL");
  });

  it("drill-down names a malware report and lists every advisory id", async () => {
    const malware = {
      ...fullScan,
      findings: [
        {
          rule_id: "osv:MAL-2025-2544",
          level: "critical" as const,
          message: "Malicious code in hypert (npm)",
          locations: [{ uri: "package-lock.json" }],
          fingerprints: { primary: "fp-mal" },
          tool: { driver_id: "lycaon-sca", name: "osv-scalibr" },
          properties: {
            lycaon: {
              kind: "sca" as const,
              advisory: {
                osv_id: "MAL-2025-2544",
                kind: "malicious_package" as const,
                aliases: ["MAL-2025-2544"],
                ghsa_ids: ["GHSA-6vm3-jj99-7229"],
                package: { name: "hypert", version: "1.0.0", ecosystem: "npm" },
              },
            },
          },
        },
      ],
    };
    client = {
      ...mockClient(),
      getCodeScan: vi.fn(async (...args: string[]) => (args.includes("full") ? malware : summaryScan)),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: malware.findings,
        guidance: [],
        total_match: 1,
      })),
    };
    await renderPane();
    const rows = await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(at(rows, 0));
    const drill = await screen.findByTestId("scans-drill-down");
    expect(screen.getByTestId("scans-advisory-kind").textContent).toBe("Malicious package");
    expect(drill.textContent).toContain("MAL-2025-2544 · GHSA-6vm3-jj99-7229");
  });

  it("arms live poll at the LIVE_REFRESH floor while scans run", async () => {
    const runningScan: CodeScan = {
      ...summaryScan,
      status: "running",
      findings_count: 0,
      findings_stored: 0,
      findings_by_level: undefined,
    };
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    client = {
      ...mockClient(),
      listCodeScans: vi.fn(async () => ({ scans: [runningScan] })),
      getCodeScan: vi.fn(async () => runningScan),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: [],
        guidance: [],
        total_match: 0,
      })),
    };

    await renderPane({
      liveScan: {
        scan_id: "scan-1",
        categories: ["sast"],
        status: "running",
        findings_count: 0,
        long_running: false,
      },
    });

    await waitFor(() => {
      expect(screen.getByTestId("scans-live-refresh")).toBeTruthy();
    });

    const livePoll = setTimeoutSpy.mock.calls.find(
      (call) => call[1] === SCANS_LIVE_REFRESH_MS,
    );
    expect(livePoll).toBeTruthy();
    setTimeoutSpy.mockRestore();
  });

  it("refreshes history when a live scan completes via SSE", async () => {
    const runningScan: CodeScan = {
      ...summaryScan,
      status: "running",
      findings_count: 0,
      long_running: false,
      findings_stored: 0,
      findings_by_level: undefined,
    };
    // Keep the run active until the test explicitly completes it.
    let listed: CodeScan[] = [runningScan];
    const listCodeScans = vi.fn(async () => ({ scans: listed }));
    client = {
      ...mockClient(),
      listCodeScans,
      getCodeScan: vi.fn(async (...args: string[]) =>
        args.includes("full") ? fullScan : (listed[0] ?? summaryScan),
      ),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: fullScan.findings,
        guidance: fullScan.guidance ?? [],
        total_match: fullScan.findings?.length ?? 0,
      })),
    };

    const [liveScan, setLiveScan] = createSignal<CodeScanEvent | undefined>({
      scan_id: "scan-1",
      categories: ["sast"],
      status: "running",
      findings_count: 0,
      long_running: false,
    });

    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <SecurityFindingsPane
        appStore={appStore}
        projectId="proj-1"
        latestScanId="scan-1"
        liveScan={liveScan()}
      />
    ));
    await selectRunsView();

    await waitFor(() => {
      expect(screen.getByTestId("scans-live-refresh")).toBeTruthy();
    });
    const callsBefore = listCodeScans.mock.calls.length;

    listed = [summaryScan];
    setLiveScan({
      scan_id: "scan-1",
      categories: ["sast"],
      status: "complete",
      findings_count: 2,
      long_running: false,
    });

    await waitFor(() => {
      expect(screen.queryByTestId("scans-live-refresh")).toBeNull();
    });
    expect(listCodeScans.mock.calls.length).toBeGreaterThanOrEqual(callsBefore);
  });

  it("treats an empty completed findings query as loaded", async () => {
    vi.useFakeTimers();
    const runningScan: CodeScan = {
      ...summaryScan,
      id: "scan-running",
      status: "running",
      findings_count: 0,
      findings_stored: 0,
      findings_by_level: undefined,
    };
    const emptyScan: CodeScan = {
      ...summaryScan,
      findings_count: 0,
      findings_stored: 0,
      findings_by_level: {},
    };
    client = {
      ...mockClient(),
      listCodeScans: vi.fn(async () => ({
        scans: [emptyScan, runningScan],
      })),
      getCodeScan: vi.fn(async () => emptyScan),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: [],
        guidance: [],
        total_match: 0,
      })),
    };

    await renderPane();
    await vi.advanceTimersByTimeAsync(0);
    await waitFor(() => expect(client.queryCodeScan).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(SCANS_LIVE_REFRESH_MS);
    await vi.advanceTimersByTimeAsync(0);
    expect(client.queryCodeScan).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it("sorts findings when a column header is clicked", async () => {
    const manyFindings = Array.from({ length: 3 }, (_, index) => ({
      rule_id: `rule-${index}`,
      level: index === 0 ? "high" : index === 1 ? "medium" : "low",
      message: `Finding ${index}`,
      locations: [{ uri: `file-${index}.go`, start_line: index + 1 }],
      fingerprints: { primary: `fp-${index}` },
      tool: { driver_id: "opengrep", name: "OpenGrep" },
      properties: { lycaon: { hint_code: `H_${index}` } },
    })) as CodeScan["findings"];

    client = {
      ...mockClient(),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: manyFindings,
        guidance: [],
        total_match: manyFindings?.length ?? 0,
      })),
    };

    await renderPane();
    await screen.findAllByTestId("scans-finding-row");
    expect(at(await screen.findAllByTestId("scans-finding-row"), 0).textContent)
      .toContain("Finding 0");

    fireEvent.click(screen.getByTestId("list-sort-location"));
    let rows = await screen.findAllByTestId("scans-finding-row");
    expect(at(rows, 0).textContent).toContain("file-0.go:1");
    expect(
      screen
        .getByTestId("scans-findings-header")
        .querySelector('[data-column="location"]')
        ?.getAttribute("aria-sort"),
    ).toBe("ascending");

    fireEvent.click(screen.getByTestId("list-sort-location"));
    rows = await screen.findAllByTestId("scans-finding-row");
    expect(at(rows, 0).textContent).toContain("file-2.go:3");

    fireEvent.click(screen.getByTestId("list-sort-location"));
    rows = await screen.findAllByTestId("scans-finding-row");
    expect(at(rows, 0).textContent).toContain("Finding 0");
  });

  it("keeps pagination outside the row scroller and starts each view at the top", async () => {
    const manyFindings = Array.from({ length: 30 }, (_, index) => ({
      rule_id: `rule-${index}`,
      level: "high" as const,
      message: `Finding ${index}`,
      locations: [
        {
          uri: `file-${index.toString().padStart(2, "0")}.go`,
          start_line: 1,
        },
      ],
      fingerprints: { primary: `fp-${index}` },
      tool: { driver_id: "opengrep", name: "OpenGrep" },
    }));
    client = {
      ...mockClient(),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: manyFindings,
        guidance: [],
        total_match: manyFindings.length,
      })),
    };

    await renderPane();
    await waitFor(() => {
      expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(25);
    });

    const frame = screen.getByTestId("scans-findings-scroll");
    const scroller = frame.querySelector<HTMLElement>(":scope > .den-scrollport__viewport")!;
    const content = scroller.querySelector<HTMLElement>(":scope > .den-scrollport__content")!;
    bindScrollportMotion(frame, scroller, content);
    const pager = screen.getByTestId("scans-findings-pager");
    expect(frame.contains(pager)).toBe(false);
    expect(frame.nextElementSibling).toBe(pager);

    scroller.scrollTop = 240;
    fireEvent.click(screen.getByTestId("scans-findings-pager-next"));
    await waitFor(() => {
      expect(
        at(screen.getAllByTestId("scans-finding-row"), 0).textContent,
      ).toContain("Finding 25");
    });
    expect(scroller.scrollTop).toBe(0);

    bindScrollportMotion(frame, scroller, content);
    scroller.scrollTop = 180;
    fireEvent.click(screen.getByTestId("list-sort-location"));
    await waitFor(() => {
      expect(
        at(screen.getAllByTestId("scans-finding-row"), 0).textContent,
      ).toContain("Finding 0");
    });
    expect(scroller.scrollTop).toBe(0);
    unbindScrollportMotion(frame);
  });

  it("closes the detail pane from the shared close control", async () => {
    await renderPane();
    const rows = await screen.findAllByTestId("scans-finding-row");
    fireEvent.click(at(rows, 0));
    await screen.findByTestId("scans-drill-down");
    fireEvent.click(screen.getByTestId("detail-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("scans-drill-down")).toBeNull();
    });
  });

  it("switches workspace between static analysis, secrets, and supply chain runs", async () => {
    const sast: CodeScan = {
      id: "scan-sast",
      categories: ["sast"],
      scanner_id: "lycaon-sast",
      status: "complete",
      findings_count: 1,
      long_running: false,
      findings_stored: 1,
      findings_by_level: { high: 1 },
      created_at: "2026-01-03T00:00:00Z",
    };
    const secrets: CodeScan = {
      id: "scan-secrets",
      categories: ["secret"],
      scanner_id: "lycaon-secrets",
      status: "complete",
      findings_count: 1,
      long_running: false,
      findings_stored: 1,
      findings_by_level: { medium: 1 },
      created_at: "2026-01-02T00:00:00Z",
    };
    const sca: CodeScan = {
      id: "scan-sca",
      categories: ["sca"],
      scanner_id: "lycaon-sca",
      status: "complete",
      findings_count: 1,
      long_running: false,
      findings_stored: 1,
      findings_by_level: { low: 1 },
      created_at: "2026-01-01T00:00:00Z",
    };
    const findingsByScan: Record<string, NonNullable<CodeScan["findings"]>> = {
      "scan-sast": [
        {
          rule_id: "sast:sql",
          level: "high",
          message: "SQL concat",
          locations: [{ uri: "app.go", start_line: 1 }],
          fingerprints: { primary: "fp-sast" },
          tool: { driver_id: "lycaon-sast", name: "Static analysis" },
        },
      ],
      "scan-secrets": [
        {
          rule_id: "secrets:token",
          level: "medium",
          message: "API token",
          locations: [{ uri: ".env", start_line: 2 }],
          fingerprints: { primary: "fp-secrets" },
          tool: { driver_id: "lycaon-secrets", name: "Secrets" },
        },
      ],
      "scan-sca": [
        {
          rule_id: "sca:cve",
          level: "low",
          message: "Vulnerable dep",
          locations: [{ uri: "go.mod", start_line: 3 }],
          fingerprints: { primary: "fp-sca" },
          tool: { driver_id: "lycaon-sca", name: "Supply chain" },
        },
      ],
    };
    const byId: Record<string, CodeScan> = {
      "scan-sast": sast,
      "scan-secrets": secrets,
      "scan-sca": sca,
    };

    client = {
      listCodeScans: vi.fn(async () => ({
        scans: [sast, secrets, sca],
      })),
      listProjectScanners: vi.fn(async () => ({
        scanners: [
          { id: "lycaon-sast", label: "Static analysis" },
          { id: "lycaon-secrets", label: "Secrets" },
          { id: "lycaon-sca", label: "Supply chain" },
        ],
        rejected: [],
      })),
      getCodeScan: vi.fn(async (...args: string[]) => {
        const id = args.find((a) => a in byId);
        return id ? byId[id]! : summaryScan;
      }),
      queryCodeScan: vi.fn(async (...args: string[]) => {
        const id = args.find((a) => a in findingsByScan) ?? "scan-sast";
        return {
          scan_id: id,
          findings: findingsByScan[id] ?? [],
          guidance: [],
          total_match: findingsByScan[id]?.length ?? 0,
        };
      }),
      exportCodeScanSARIF: vi.fn(async () => ({
        blob: new Blob(["{}"], { type: "application/sarif+json" }),
        filename: "scan.sarif.json",
      })),
    } as unknown as ReturnType<typeof mockClient>;

    await renderPane({ latestScanId: "scan-sast" });

    await waitFor(() => {
      expect(screen.getByTestId("scans-run-picker").textContent).toContain(
        "Static analysis",
      );
    });
    fireEvent.click(screen.getByTestId("scans-run-picker"));
    await waitFor(() => {
      expect(screen.getAllByTestId("scans-run-card").length).toBeGreaterThan(0);
    });
    const cards = screen.getAllByTestId("scans-run-card");
    const secretsCard = cards.find((c) => c.textContent?.includes("Secrets"));
    const scaCard = cards.find((c) => c.textContent?.includes("Supply chain"));
    expect(secretsCard).toBeTruthy();
    expect(scaCard).toBeTruthy();

    fireEvent.click(secretsCard!);
    await waitFor(() => {
      expect(secretsCard!.getAttribute("aria-selected")).toBe("true");
      expect(screen.getByTestId("scans-finding-row").textContent).toContain("API token");
    });

    fireEvent.click(screen.getByTestId("scans-run-picker"));
    await waitFor(() => {
      expect(screen.getAllByTestId("scans-run-card").length).toBeGreaterThan(0);
    });
    const scaCards = screen.getAllByTestId("scans-run-card");
    const scaCardAgain = scaCards.find((c) => c.textContent?.includes("Supply chain"));
    fireEvent.click(scaCardAgain!);
    await waitFor(() => {
      expect(scaCardAgain!.getAttribute("aria-selected")).toBe("true");
      expect(screen.getByTestId("scans-finding-row").textContent).toContain(
        "Vulnerable dep",
      );
    });
  });

  it("filters findings by severity chip and exports SARIF for the full run", async () => {
    vi.mocked(downloadExport).mockClear();
    await renderPane();
    await screen.findAllByTestId("scans-finding-row");

    clickChip("scans-severity-chip-high");
    await waitFor(() => {
      expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(1);
      expect(screen.getByTestId("scans-counts").textContent).toContain(
        "1 of 2 findings",
      );
    });

    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    expect(screen.getByTestId("scans-export-visible-csv")).toBeTruthy();
    fireEvent.click(screen.getByTestId("scans-export-run-sarif"));
    await waitFor(() => {
      expect(client.exportCodeScanSARIF).toHaveBeenCalledWith("proj-1", "scan-1");
      expect(downloadExport).toHaveBeenCalled();
    });
  });

  it("shows finding locations relative to the repo root", async () => {
    client = {
      ...mockClient(),
      queryCodeScan: vi.fn(async () => ({
        scan_id: "scan-1",
        findings: [
          {
            rule_id: "opengrep:sql",
            level: "high",
            message: "SQL concat",
            locations: [{ uri: "/tmp/proj/app.rb", start_line: 9 }],
            fingerprints: { primary: "fp-one" },
            tool: { driver_id: "opengrep", name: "OpenGrep" },
            properties: { lycaon: { hint_code: "SCAN_SQL" } },
          },
        ],
        guidance: [],
        total_match: 1,
      })),
    };

    await renderPane({ repoRoot: "/tmp/proj" });
    const row = await screen.findByTestId("scans-finding-row");
    expect(row.textContent).toContain("app.rb:9");
    expect(row.textContent).not.toContain("/tmp/proj");
  });

  it("reads coverage from the overview: no full pass, complete, bounded", async () => {
    client.getProjectSecurity.mockResolvedValue(incrementalOverview);
    const { unmount } = render(() => {
      const appStore = createAppStore();
      appStore.actions.setSidecarStatus("connected");
      return <SecurityFindingsPane appStore={appStore} projectId="proj-1" latestScanId="scan-1" />;
    });
    // Coverage is hidden on an empty ledger but visible in the run view.
    await selectRunsView();
    await waitFor(() => expect(screen.getByTestId("scans-coverage-line").textContent).toMatch(/^Incremental since .*· no full scan yet$/));
    expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Incremental");
    unmount();

    client = mockClient();
    await renderPane();
    await waitFor(() => expect(screen.getByTestId("scans-coverage-line").textContent).toMatch(/^Complete as of /));
    expect(screen.getByTestId("scans-coverage-line").textContent).not.toContain("·");
    expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Complete");
  });

  it("qualifies a bounded pass", async () => {
    client.getProjectSecurity.mockResolvedValue({
      ...completeOverview,
      baseline: { ...completeOverview.baseline!, unobserved_directories: 2 },
      last_full: { ...completeOverview.last_full!, coverage_status: "bounded" },
    });
    await renderPane();
    await waitFor(() => expect(screen.getByTestId("scans-coverage-line").textContent).toContain("· some directories were not analyzed"));
    expect(screen.getByTestId("scans-coverage-chip").textContent).toBe("Bounded");
  });

  it("shows all findings by default and filters by introduced time on request", async () => {
    client.getProjectSecurity.mockResolvedValue(incrementalOverview);
    client.queryCodeScan.mockImplementation(async (...args: [string, ...(string | ScanQueryRequest | undefined)[]]) => {
      const req = args.find((a): a is ScanQueryRequest => typeof a === "object" && a !== null);
      return {
        scan_id: "scan-1",
        findings: historyFindings,
        guidance: [],
        total_match: historyFindings.length,
        fixed_findings: req?.fixed_since_at ? [historyFindings[1]!] : undefined,
      };
    });
    await renderPane();
    await waitFor(() => expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2));
    expect(screen.getByTestId("scans-new-since-chip").getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(screen.getByTestId("scans-new-since-chip"));
    await waitFor(() => expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(1));
    const chip = screen.getByTestId("scans-new-since-chip");
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByTestId("scans-finding-introduced").textContent).toContain("introduced");
    expect(screen.getByTestId("scans-counts").textContent).toBe("1 of 2 findings");
    await waitFor(() => expect(screen.getByTestId("scans-fixed")).toBeTruthy());
    expect(screen.getAllByTestId("scans-fixed-row")).toHaveLength(1);
    expect(screen.getByTestId("scans-fixed-row").textContent).toContain("Secret token");
    expect(client.queryCodeScan).toHaveBeenCalledWith("proj-1", "scan-1", { fixed_since_at: "2026-01-01T09:41:00Z", limit: expect.any(Number) });

    fireEvent.click(chip);
    await waitFor(() => expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2));
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByTestId("scans-fixed")).toBeNull();
  });

  it("keeps New since off after a full pass until the chip is flipped", async () => {
    client.queryCodeScan.mockResolvedValue({ scan_id: "scan-1", findings: historyFindings, guidance: [], total_match: 2 });
    await renderPane();
    await waitFor(() => expect(screen.getAllByTestId("scans-finding-row")).toHaveLength(2));
    await waitFor(() => expect(screen.getByTestId("scans-new-since-chip").getAttribute("aria-pressed")).toBe("false"));
    fireEvent.click(screen.getByTestId("scans-new-since-chip"));
    // The later full pass is the baseline, and nothing was introduced after it.
    await waitFor(() => expect(screen.getByTestId("scans-filter-empty").textContent).toContain("No findings with a recorded introduction since the baseline."));
  });

  it("starts a full scan with the checked scanners and polls the overview while it runs", async () => {
    const running: SecurityOverview = {
      ...completeOverview,
      running: {
        assessment_id: "a1",
        requested_at: "2026-01-03T10:00:00Z",
        started_at: "2026-01-03T10:00:00Z",
        members: [{
          scanner_id: "opengrep-sast",
          phase: "started",
          scan: {
            id: "scan-run", categories: ["sast"], scanner_id: "opengrep-sast", status: "running", long_running: false,
            findings_count: 0, created_at: "2026-01-03T10:00:00Z", progress: { chunks: 7, completed: 4, files: 12_702 },
          },
        }],
      },
    };
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    await renderPane();
    await waitFor(() => expect(screen.getByTestId("scans-coverage-line").textContent).toMatch(/^Complete as of /));
    fireEvent.click(screen.getByTestId("scans-full-scan-toggle"));
    fireEvent.click(screen.getByTestId("scans-full-scan-scanner-gitleaks"));
    // The command answers with the overview that already holds its pass.
    client.startFullScan.mockResolvedValueOnce({ passes: [running.running!], overview: running });
    client.getProjectSecurity.mockResolvedValue(running);
    fireEvent.click(screen.getByTestId("scans-full-scan-start"));
    await waitFor(() => expect(client.startFullScan).toHaveBeenCalledWith("proj-1", { kind: "full", scanner_ids: ["opengrep-sast"] }));
    await waitFor(() => expect(screen.getByTestId("scans-full-scan-readout").textContent).toBe("4 of 7 chunks · 12,702 files"));
    expect(screen.getByTestId("scans-full-scan-status").textContent).toBe("Running");
    expect(screen.queryByTestId("scans-full-scan-start")).toBeNull();
    await waitFor(() => expect(setTimeoutSpy.mock.calls.some((call) => call[1] === SCANS_LIVE_REFRESH_MS)).toBe(true));
    setTimeoutSpy.mockRestore();
  });
});
