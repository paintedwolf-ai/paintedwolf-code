// @vitest-environment jsdom
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  FindingLedgerEntry,
  FindingLedgerResponse,
  SecurityOverview,
} from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { createAppStore } from "../../store/app-state.ts";
import { resetLayoutStoreForTests } from "../../shell/layout-store.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { SecurityFindingsPane } from "./SecurityFindingsPane.tsx";

const addFindingsToChat = vi.hoisted(() => vi.fn());
vi.mock("./add-findings-to-chat.ts", () => ({
  addFindingsToChat: (...args: unknown[]) => addFindingsToChat(...args),
}));

function entry(
  overrides: Partial<FindingLedgerEntry> & { rule: string },
): FindingLedgerEntry {
  const { rule, ...rest } = overrides;
  return {
    finding: {
      rule_id: rule,
      level: "high",
      message: `${rule} message`,
      locations: [{ uri: "internal/api/authz.go", start_line: 11 }],
      fingerprints: { primary: `fp-${rule}` },
      tool: { driver_id: "opengrep-sast", name: "opengrep" },
    },
    state: "open",
    scanner_id: "opengrep-sast",
    first_seen_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-10T10:00:00Z",
    observations: 1,
    last_scan_id: "scan-9",
    ...rest,
  };
}

const counts = {
  open: 12,
  reopened: 1,
  fixed: 4,
  not_observed: 2,
  unverified: 3,
  ignored: 5,
};

const overview: SecurityOverview = {
  project_id: "proj-1",
  enabled: true,
  baseline: {
    snapshot_id: "snap-1",
    created_at: "2026-09-01T09:00:00Z",
    file_count: 10,
    unobserved_directories: 0,
  },
  last_full: {
    assessment_id: "a0",
    requested_at: "2026-09-01T09:00:00Z",
    started_at: "2026-09-01T09:00:00Z",
    completed_at: "2026-09-01T09:10:00Z",
    coverage_status: "complete",
    members: [],
  },
  scanners: [
    {
      id: "opengrep-sast",
      label: "Static analysis",
      categories: ["sast"],
      available: true,
      running: false,
      watching: true,
      // A completed full pass, superseded by an engine change.
      last_full_at: "2026-09-01T09:10:00Z",
      pass_superseded: true,
    },
    {
      id: "gitleaks",
      label: "Secrets",
      categories: ["secret"],
      available: true,
      running: false,
      watching: true,
      pass_superseded: false,
    },
  ],
  introduced_since_baseline: 0,
  fixed_since_baseline: 0,
};

const runningOverview: SecurityOverview = {
  ...overview,
  running: {
    assessment_id: "a1",
    requested_at: "2026-09-10T11:00:00Z",
    started_at: "2026-09-10T11:00:00Z",
    members: [
      {
        scanner_id: "gitleaks",
        phase: "started",
        scan: {
          id: "scan-secrets", categories: ["secret"], scanner_id: "gitleaks", status: "complete",
          long_running: false, findings_count: 1, created_at: "2026-09-10T11:00:00Z",
        },
      },
      {
        scanner_id: "opengrep-sast",
        phase: "started",
        scan: {
          id: "scan-sast", categories: ["sast"], scanner_id: "opengrep-sast", status: "running",
          long_running: false, findings_count: 0, created_at: "2026-09-10T11:00:00Z",
          progress: { chunks: 8, completed: 2, files: 900 },
        },
      },
    ],
  },
};

function ledgerPage(entries: FindingLedgerEntry[]): FindingLedgerResponse {
  return {
    project_id: "proj-1",
    entries,
    counts,
    by_level: { critical: 0, high: entries.length, medium: 0, low: 0, info: 0, unknown: 0 },
    total_match: entries.length,
  };
}

function mockClient() {
  return {
    getProjectSecurity: vi.fn<LycaonClient["getProjectSecurity"]>(async () => overview),
    listCodeScans: vi.fn<LycaonClient["listCodeScans"]>(async () => ({
      scans: [],
    })),
    listProjectScanners: vi.fn(async () => ({
      scanners: [{ id: "opengrep-sast", label: "Static analysis" }],
      rejected: [],
    })),
    getCodeScan: vi.fn(async () => {
      throw new Error("the ledger does not read a run");
    }),
    queryCodeScan: vi.fn(async () => {
      throw new Error("the ledger does not read a run");
    }),
    startFullScan: vi.fn<LycaonClient["startFullScan"]>(async () => ({
      passes: [],
      overview,
    })),
    queryProjectFindings: vi.fn<LycaonClient["queryProjectFindings"]>(async () =>
      ledgerPage([entry({ rule: "rule-open" })]),
    ),
    listProjectFindingIgnores: vi.fn<LycaonClient["listProjectFindingIgnores"]>(async () => ({
      project_id: "proj-1",
      path: "/tmp/proj/.paintedwolf/ignores.yaml",
      rules: [
        {
          id: "entry-1",
          path: "test/**",
          reason: "fixture material",
          source: "project",
          withdrawable: true,
          matches: 3,
        },
        // No file id, so not withdrawable.
        {
          id: "project:kind: secret",
          kind: "secret",
          reason: "handled by the vault rotation job",
          source: "project",
          withdrawable: false,
          matches: 0,
        },
      ],
    })),
    createProjectFindingIgnore: vi.fn<LycaonClient["createProjectFindingIgnore"]>(async () => ({
      project_id: "proj-1",
      path: "/tmp/proj/.paintedwolf/ignores.yaml",
      rules: [],
    })),
    deleteProjectFindingIgnore: vi.fn<LycaonClient["deleteProjectFindingIgnore"]>(async () => {}),
    exportProjectFindings: vi.fn<LycaonClient["exportProjectFindings"]>(async () => ({
      blob: new Blob(["{}"], { type: "application/sarif+json" }),
      filename: "proj-findings.sarif",
    })),
  };
}

let client: ReturnType<typeof mockClient>;

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return { ...actual, getLycaonClient: () => client };
});

function renderStage(repoRoot?: string) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  render(() => (
    <SecurityFindingsPane appStore={appStore} projectId="proj-1" repoRoot={repoRoot} liveScan={appStore.state.latestCodeScan} />
  ));
  return appStore;
}

function openFilterPanel() {
  fireEvent.click(screen.getByTestId("ledger-filter"));
}

function lastLedgerRequest() {
  const calls = client.queryProjectFindings.mock.calls;
  return calls[calls.length - 1]?.[1] ?? {};
}

describe("Security ledger", () => {
  beforeEach(() => {
    client = mockClient();
    addFindingsToChat.mockReset().mockResolvedValue({ ok: true });
    resetLayoutStoreForTests();
  });

  it("refreshes when finding history arrives after an identical completed scan event", async () => {
    const store = renderStage();
    await screen.findByText("rule-open message");
    const event = { scan_id: "scan-9", status: "complete" as const, categories: ["sast" as const], findings_count: 1, long_running: false };
    client.queryProjectFindings.mockResolvedValue(ledgerPage([entry({ rule: "first-complete" })]));
    store.actions.addCodeScan(event);
    await screen.findByText("first-complete message");

    client.queryProjectFindings.mockResolvedValue(ledgerPage([entry({ rule: "history-committed" })]));
    store.actions.addCodeScan({ ...event });
    await screen.findByText("history-committed message");
    expect(screen.queryByText("first-complete message")).toBeNull();
  });

  it("adds the opened finding to chat", async () => {
    renderStage();
    fireEvent.click(await screen.findByTestId("ledger-row"));
    fireEvent.click(await screen.findByTestId("ledger-add-to-chat"));
    await vi.waitFor(() => expect(addFindingsToChat).toHaveBeenCalledWith("proj-1", [entry({ rule: "rule-open" })]));
  });

  it("adds all selected findings in one action and retains the selection on failure", async () => {
    const rows = [entry({ rule: "first" }), entry({ rule: "second" })];
    client.queryProjectFindings.mockResolvedValue(ledgerPage(rows));
    addFindingsToChat.mockResolvedValue({ ok: false, reason: "Upload unavailable." });
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("ledger-select-all"));
    fireEvent.click(await screen.findByTestId("ledger-selection-add-to-chat"));
    await vi.waitFor(() => expect(addFindingsToChat).toHaveBeenCalledWith("proj-1", rows));
    expect(await screen.findByText("Upload unavailable.")).toBeTruthy();
    expect(screen.getByTestId("ledger-selection").textContent).toContain("2 selected");
  });

  it("opens on the project rather than a run", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    expect(client.queryProjectFindings).toHaveBeenCalled();
    expect(client.queryCodeScan).not.toHaveBeenCalled();
    expect(screen.queryByTestId("scans-run-picker")).toBeNull();
  });

  it("reports the project's totals, never the loaded page", async () => {
    renderStage();
    const meta = await screen.findByTestId("ledger-counts");
    // The count line reflects the project, not the page.
    expect(meta.textContent).toContain("12 open");
    expect(meta.textContent).toContain("4 fixed");
    expect(meta.textContent).toContain("3 unverified");
    expect(meta.textContent).toContain("2 not observed");
  });

  // The Open tab filters to open and reopened.
  it("asks only for what is reported on the open tab", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    await vi.waitFor(() =>
      expect(lastLedgerRequest().states).toEqual(["open", "reopened"]),
    );
    fireEvent.click(screen.getByTestId("ledger-tab-all"));
    await vi.waitFor(() => expect(lastLedgerRequest().states).toBeUndefined());
  });

  it("keeps fixed, unverified, and not observed as separate filters", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("ledger-tab-all"));
    openFilterPanel();
    for (const state of ["fixed", "unverified", "not_observed", "ignored"]) {
      expect(screen.getByTestId(`ledger-filter-state-${state}`)).toBeTruthy();
    }
    fireEvent.click(screen.getByTestId("ledger-filter-state-fixed"));
    await vi.waitFor(() => expect(lastLedgerRequest().states).toEqual(["fixed"]));
  });

  it("sends the filter text and a column sort to the store", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.input(screen.getByTestId("ledger-filter-input"), {
      target: { value: "authz" },
    });
    await vi.waitFor(() => expect(lastLedgerRequest().text).toBe("authz"));

    fireEvent.click(screen.getByTestId("list-sort-last_seen"));
    await vi.waitFor(() => expect(lastLedgerRequest().sort).toBe("last_seen"));
    // Filtering returns to the first page.
    expect(lastLedgerRequest().cursor).toBeUndefined();
  });

  it("says what a bounded pass established, which is nothing", async () => {
    client.queryProjectFindings.mockResolvedValue(
      ledgerPage([
        entry({
          rule: "rule-gone",
          state: "not_observed",
          absence: {
            observed_at: "2026-09-09T10:00:00Z",
            coverage_status: "bounded",
            execution_moved: false,
          },
        }),
      ]),
    );
    renderStage();
    fireEvent.click(await screen.findByTestId("ledger-row"));
    const absence = await screen.findByTestId("ledger-detail-absence");
    expect(absence.textContent).toContain("bounded");
    expect(absence.textContent).toContain("establishes nothing");
    expect(absence.textContent).not.toContain("fixed");
  });

  // Writing requires a reason.
  it("writes an ignore entry with the predicate and reason the reader chose", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("ledger-row-check"));
    fireEvent.click(await screen.findByTestId("ledger-selection-ignore"));

    const commit = await screen.findByTestId("ignore-commit");
    expect(commit.hasAttribute("disabled")).toBe(true);

    fireEvent.click(screen.getByTestId("ignore-scope-rule"));
    fireEvent.input(screen.getByTestId("ignore-reason"), {
      target: { value: "known safe in this codebase" },
    });
    fireEvent.click(screen.getByTestId("ignore-expiry-90"));

    // The YAML preview is shown before writing.
    const preview = await screen.findByTestId("ignore-preview");
    expect(preview.textContent).toContain("rule: rule-open");
    expect(preview.textContent).toContain("reason: known safe in this codebase");

    fireEvent.click(screen.getByTestId("ignore-commit"));
    await vi.waitFor(() => expect(client.createProjectFindingIgnore).toHaveBeenCalled());
    const [, written] = client.createProjectFindingIgnore.mock.calls[0]!;
    expect(written.rule).toBe("rule-open");
    expect(written.scanner).toBe("opengrep-sast");
    expect(written.reason).toBe("known safe in this codebase");
    expect(written.expires_on).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    // A write re-reads the page.
    await vi.waitFor(() =>
      expect(client.queryProjectFindings.mock.calls.length).toBeGreaterThan(1),
    );
  });

  // The catalog lists entries that match nothing.
  it("lists every decision the project holds, with what each one covers", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    fireEvent.click(await screen.findByTestId("ledger-open-catalog"));
    const rows = await screen.findAllByTestId("ignore-catalog-row");
    expect(rows[0]!.textContent).toContain("fixture material");
    expect(rows[0]!.textContent).toContain("path: test/**");
    expect(rows[0]!.textContent).toContain("3 matching findings");
    // Non-withdrawable entries show no Withdraw action.
    expect(rows[1]!.textContent).toContain("Edit in the file");
    expect(screen.getAllByTestId("ignore-catalog-withdraw")).toHaveLength(1);
    fireEvent.click(screen.getByTestId("ignore-catalog-withdraw"));
    await vi.waitFor(() =>
      expect(client.deleteProjectFindingIgnore).toHaveBeenCalledWith("proj-1", "entry-1"),
    );
  });

  // The rules replace the findings until the reader goes back.
  it("shows ignore rules in place of the findings list", async () => {
    renderStage("/tmp/proj");
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    fireEvent.click(await screen.findByTestId("ledger-open-catalog"));
    const catalog = await screen.findByTestId("ignore-catalog");
    expect(screen.queryByTestId("ledger-rows")).toBeNull();
    expect(screen.queryByTestId("ledger-filter-input")).toBeNull();
    // The file is named relative to the project.
    expect(catalog.querySelector("code")?.textContent).toBe(".paintedwolf/ignores.yaml");
    fireEvent.click(screen.getByTestId("ignore-catalog-back"));
    await screen.findByTestId("ledger-rows");
    expect(screen.queryByTestId("ignore-catalog")).toBeNull();
  });

  // Ignored rows show their entry; reopened rows show a lapsed one.
  it("shows the decision covering a row, and that it lapsed", async () => {
    client.queryProjectFindings.mockResolvedValue(
      ledgerPage([
        entry({
          rule: "rule-open",
          ignore: {
            entry_id: "entry-1",
            reason: "was meant to be temporary",
            matched_on: "path: test/**",
            expires_on: "2026-01-01",
            expired: true,
          },
        }),
      ]),
    );
    renderStage();
    fireEvent.click(await screen.findByTestId("ledger-row"));
    const block = await screen.findByTestId("ledger-detail-ignore");
    expect(block.textContent).toContain("lapsed");
    expect(block.textContent).toContain("open again");
  });

  // Export sends the query, not the loaded rows.
  it("exports what the query matches rather than the loaded page", async () => {
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.input(screen.getByTestId("ledger-filter-input"), {
      target: { value: "authz" },
    });
    await vi.waitFor(() => expect(lastLedgerRequest().text).toBe("authz"));
    fireEvent.click(screen.getByTestId("scans-overflow-trigger"));
    fireEvent.click(await screen.findByTestId("ledger-export-sarif"));
    await vi.waitFor(() => expect(client.exportProjectFindings).toHaveBeenCalled());
    const [, request] = client.exportProjectFindings.mock.calls[0]!;
    expect(request.format).toBe("sarif");
    expect(request.query?.text).toBe("authz");
    // The export query has no paging.
    expect(request.query?.limit).toBeUndefined();
    expect(request.query?.cursor).toBeUndefined();
  });

  it("opens the run behind a row as the evidence for it", async () => {
    renderStage();
    fireEvent.click(await screen.findByTestId("ledger-row"));
    fireEvent.click(await screen.findByTestId("ledger-open-run"));
    await screen.findByTestId("scans-run-picker");
    expect(screen.queryByTestId("ledger-rows")).toBeNull();
  });

  it("scopes an empty open tab to what the scans covered", async () => {
    client.queryProjectFindings.mockResolvedValue({
      ...ledgerPage([]),
      total_match: 0,
    });
    client.getProjectSecurity.mockResolvedValue({ ...overview, last_full: undefined });
    renderStage();
    const empty = await screen.findByTestId("ledger-list-empty");
    await vi.waitFor(() =>
      expect(empty.textContent).toBe(
        "No open findings reported. No full scan has run, so only changed files have been scanned.",
      ),
    );
  });

  it("selects every row on the page from the header", async () => {
    client.queryProjectFindings.mockResolvedValue(
      ledgerPage([entry({ rule: "rule-a" }), entry({ rule: "rule-b" })]),
    );
    renderStage();
    await screen.findByTestId("ledger-rows");
    fireEvent.click(screen.getByTestId("ledger-select-all"));
    const band = await screen.findByTestId("ledger-selection");
    expect(band.textContent).toContain("2 selected");
    fireEvent.click(screen.getByTestId("ledger-selection-clear"));
    await vi.waitFor(() => expect(screen.queryByTestId("ledger-selection")).toBeNull());
  });

  it("keeps the pager on the page on screen until the next page lands", async () => {
    const rows = (prefix: string) =>
      Array.from({ length: 25 }, (_, index) => entry({ rule: `${prefix}-${index}` }));
    const first = { ...ledgerPage(rows("first")), total_match: 60, next_cursor: "cursor-2" };
    let publishSecond: (page: FindingLedgerResponse) => void = () => undefined;
    client.queryProjectFindings.mockImplementation(async (_projectId, req) =>
      req?.cursor === "cursor-2"
        ? new Promise<FindingLedgerResponse>((resolve) => {
            publishSecond = resolve;
          })
        : first,
    );
    renderStage();
    await vi.waitFor(() =>
      expect(screen.getByTestId("ledger-pager-page").textContent).toBe("Page 1 of 3"),
    );
    fireEvent.click(screen.getByTestId("ledger-pager-next"));
    await vi.waitFor(() => expect(lastLedgerRequest().cursor).toBe("cursor-2"));

    const next = screen.getByTestId("ledger-pager-next") as HTMLButtonElement;
    expect(next.textContent).toBe("Next");
    expect(next.disabled).toBe(true);
    fireEvent.click(next);
    expect((screen.getByTestId("ledger-pager-prev") as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getByTestId("ledger-pager-page").textContent).toBe("Page 1 of 3");
    expect(screen.getByTestId("ledger-pager-range").textContent).toBe("1–25 of 60");

    publishSecond({ ...ledgerPage(rows("second")), total_match: 60, next_cursor: "cursor-3" });
    await vi.waitFor(() =>
      expect(screen.getByTestId("ledger-pager-page").textContent).toBe("Page 2 of 3"),
    );
    expect(screen.getByTestId("ledger-pager-range").textContent).toBe("26–50 of 60");
    expect(screen.getByTestId("ledger-pager-next")).toBe(next);
    expect(next.disabled).toBe(false);
    fireEvent.click(next);
    await vi.waitFor(() => expect(lastLedgerRequest().cursor).toBe("cursor-3"));
  });

  it("keeps a running full pass reachable once its findings take the stage", async () => {
    client.getProjectSecurity.mockResolvedValue(runningOverview);
    renderStage();
    await screen.findByTestId("ledger-rows");
    expect(screen.queryByTestId("scans-empty")).toBeNull();
    const toggle = screen.getByTestId("scans-full-scan-toggle");
    await vi.waitFor(() =>
      expect(screen.getByTestId("scans-full-scan-toggle-label").textContent).toBe(
        "Full scan · 1 of 2 done",
      ),
    );
    fireEvent.click(toggle);
    const readouts = screen.getAllByTestId("scans-full-scan-readout").map((el) => el.textContent);
    expect(readouts).toEqual(["Complete", "2 of 8 chunks · 900 files"]);
  });

  it("returns to a running pass without painting the empty ledger it held before", async () => {
    const empty = { ...ledgerPage([]), counts: { open: 0, reopened: 0, fixed: 0, not_observed: 0, unverified: 0, ignored: 0 } };
    client.getProjectSecurity.mockResolvedValue(runningOverview);
    client.queryProjectFindings.mockResolvedValue(empty);
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <SecurityFindingsPane appStore={appStore} projectId="proj-1" />
      </ResidentPresenceProvider>
    ));
    await screen.findByTestId("scans-empty-progress");

    setPresence("idle");
    let settle!: (page: FindingLedgerResponse) => void;
    client.queryProjectFindings.mockImplementationOnce(() => new Promise((resolve) => { settle = resolve; }));
    setPresence("active");

    await vi.waitFor(() => expect(settle).toBeTypeOf("function"));
    expect(screen.queryByTestId("scans-empty")).toBeNull();
    settle(ledgerPage([entry({ rule: "rule-open" })]));
    await screen.findByText("rule-open message");
    expect(screen.queryByTestId("scans-empty")).toBeNull();
  });

  it("keeps the empty ledger on return when the activation read confirms it", async () => {
    const empty = { ...ledgerPage([]), counts: { open: 0, reopened: 0, fixed: 0, not_observed: 0, unverified: 0, ignored: 0 } };
    client.getProjectSecurity.mockResolvedValue(runningOverview);
    client.queryProjectFindings.mockResolvedValue(empty);
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <SecurityFindingsPane appStore={appStore} projectId="proj-1" />
      </ResidentPresenceProvider>
    ));
    await screen.findByTestId("scans-empty-progress");
    setPresence("idle");
    setPresence("active");
    await screen.findByTestId("scans-empty-progress");
  });

  it("warns when a scanner's engine moved after its last full pass", async () => {
    renderStage();
    const note = await screen.findByTestId("scans-pass-stale");
    expect(note.textContent).toContain("no longer applies");
  });
});
