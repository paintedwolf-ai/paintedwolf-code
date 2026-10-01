import { resetProjectTrustForTests } from "../../settings/security/project-trust.ts";
import { resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type {
  ApprovalConfigResponse,
  CostSummary,
  ModelPolicy,
  ProjectTrust,
  SettingsLimitsResponse,
} from "../../api/types.ts";
import { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { ComposerStatusChips } from "./ComposerStatusChips.tsx";

const connection = vi.hoisted(() => ({ client: null as LycaonClient | null }));
vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => connection.client,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

const emptyTrust: ProjectTrust = {
  project_id: "p1",
  surfaces: [],

  unread_count: 0,
    review: { id: "review", project_id: "p1", changes: [] },

};

const approvalsDoc = (posture: ApprovalConfigResponse["approval_posture"]): ApprovalConfigResponse => ({
  scope: "project",
  rules: [],
  managed_rules: [],
  merged_from: ["bundled"],
  approval_posture: posture,
  ai_rationale_enabled: true,
  never_ask: false,
});

const policyDoc: ModelPolicy = {
  coordinator: { provider_id: "provider", model: "vendor/coordinator-1" },
  lite: { provider_id: "provider", model: "lite-1" },
  agent_pool: { selection: "round_robin", models: [] },
};

const limitsDoc: SettingsLimitsResponse = {
  scope: "global",
  max_iterations: 500,
  overlay_promote_max_iterations: 150,
  max_tool_result_bytes: 131072,
  coordinator_loop: true,
  max_coordinator_loop_cycles: 64,
  llm_turn_timeout_ms: 10800000,
  coordinator_host_turn_timeout_ms: 10800000,
  coordinator_max_sleep_ms: 10800000,
  await_parent_workers_timeout_ms: 10800000,
  worker_tool_budget_default: 20,
  worker_tool_budget_min: 2,
  worker_tool_budget_max: 120,
  spend_soft_stop: true,
  merged_from: ["bundled"],
};

const costDoc = (sessionId: string, usd: number): CostSummary => ({
  scope: "session",
  session_id: sessionId,
  estimated_nano_usd: Math.round(usd * 1e9),
  token_totals: { prompt: 10, completion: 2 },
  coordinator: { estimated_nano_usd: Math.round(usd * 1e9), token_totals: { prompt: 10, completion: 2 } },
  workers: { estimated_nano_usd: 0, token_totals: { prompt: 0, completion: 0 } },
  summarizer: { estimated_nano_usd: 0, token_totals: { prompt: 0, completion: 0 } },
  estimate_coverage: "complete",
  pricing_provenance: [],
});

type Stubs = {
  openProjectTrustReview?: ReturnType<typeof vi.fn>;
  getProjectTrust?: ReturnType<typeof vi.fn>;
  getApprovalsSettings?: ReturnType<typeof vi.fn>;
  getModelPolicySettings?: ReturnType<typeof vi.fn>;
  getLimitsSettings?: ReturnType<typeof vi.fn>;
  getCostSummary?: ReturnType<typeof vi.fn>;
};

function readyClient(stubs: Stubs = {}): LycaonClient {
  return stubClient({
    ...(stubs.openProjectTrustReview ? { openProjectTrustReview: stubs.openProjectTrustReview } : {}),
    getProjectTrust: stubs.getProjectTrust ?? vi.fn(async () => emptyTrust),
    getApprovalsSettings: stubs.getApprovalsSettings ?? vi.fn(async () => approvalsDoc("balanced")),
    getModelPolicySettings: stubs.getModelPolicySettings ?? vi.fn(async () => policyDoc),
    getLimitsSettings: stubs.getLimitsSettings ?? vi.fn(async () => limitsDoc),
    getCostSummary: stubs.getCostSummary ?? vi.fn(async (sessionId: string) => costDoc(sessionId, 0.12)),
  });
}

function mountChips(options: { roots?: readonly ProjectRoot[]; tracking?: boolean; sessionId?: () => string; appStore?: ReturnType<typeof createAppStore> } = {}) {
  const appStore = options.appStore ?? createAppStore();
  const settingsStore = createSettingsStore();
  if (options.tracking) {
    settingsStore.actions.setPricing({ cost_tracking_enabled: true, sources: [], available_sources: [] });
  }
  const costStore = createCostStore();
  const preparation = createPreparation();
  const sessionId = options.sessionId ?? (() => "s1");
  render(() => (
    <PresentationProvider preparation={preparation}>
      <ComposerStatusChips
        appStore={appStore}
        settingsStore={settingsStore}
        costStore={costStore}
        sessionId={sessionId()}
        projectId="p1"
        roots={options.roots}
      />
    </PresentationProvider>
  ));
  return { appStore, settingsStore, costStore, preparation };
}

beforeEach(() => {
  resetProjectTrustForTests();
  resetOpenFilesSurfaceForTests();
  resetSurfaceQueriesForTests();
  connection.client = null;
  setStatusChipNavigationSink(null);
});

describe("ComposerStatusChips preparation", () => {
  it("keeps board cost coverage in the chip and popover while the summary is pending", async () => {
    const pending = deferred<CostSummary>();
    connection.client = readyClient({ getCostSummary: vi.fn(() => pending.promise) });
    const appStore = createAppStore();
    appStore.actions.setBoard({
      summary: "", repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: { ...costDoc("s1", 3.1), estimate_coverage: "lower_bound", unpriced_tokens: 100 },
      pack_content_hash: "h", detail_level: "compact", board: "", board_chars: 0,
      truncated: false, generated_at: "t", now_line: "Now: t",
    });
    mountChips({ tracking: true, appStore });
    const chip = screen.getByTestId("status-chip-cost");
    // The dot carries lower-bound coverage.
    await waitFor(() => expect(chip.textContent).toBe("$3.10"));
    expect(chip.getAttribute("data-coverage")).toBe("lower_bound");
    expect(chip.querySelector(".den-status-chip__dot")).not.toBeNull();
    expect(chip.hasAttribute("data-tip")).toBe(false);
    fireEvent.click(chip);
    await waitFor(() => expect(screen.getByTestId("cost-summary-lower-bound").textContent).toContain("100 tokens"));
    expect(screen.getByTestId("cost-summary-total-usd").textContent).toBe("$3.10");
    expect(screen.getByTestId("cost-summary-total-usd-coverage").textContent).toBe("Lower bound");
    pending.resolve(costDoc("s1", 3.2));
    await waitFor(() => expect(chip.textContent).toBe("$3.20"));
    expect(chip.getAttribute("data-coverage")).toBe("complete");
    expect(chip.querySelector(".den-status-chip__dot")).toBeNull();
  });

  it("waits for the connect to name the cost tracking setting before the cost cell can paint", async () => {
    connection.client = readyClient();
    const appStore = createAppStore();
    appStore.actions.setLoading(true);
    const { preparation, settingsStore } = mountChips({ appStore });
    expect(preparation.pending()).toContain("cost-tracking-setting");
    expect(screen.queryByTestId("status-chip-cost")).toBeNull();

    settingsStore.actions.setPricing({ cost_tracking_enabled: true, sources: [], available_sources: [] });
    expect(preparation.pending()).not.toContain("cost-tracking-setting");
    expect(screen.getByTestId("status-chip-cost")).toBeTruthy();
    await waitFor(() => expect(preparation.ready()).toBe(true));
  });

  it("settles the cost tracking setting when the connect finishes without pricing", () => {
    connection.client = readyClient();
    const appStore = createAppStore();
    appStore.actions.setLoading(true);
    const { preparation } = mountChips({ appStore });
    expect(preparation.pending()).toContain("cost-tracking-setting");
    appStore.actions.setLoading(false);
    expect(preparation.pending()).not.toContain("cost-tracking-setting");
  });

  it("prepares approval level, model, limits, and cost while trust inventory loads", async () => {
    const trust = deferred<ProjectTrust>();
    const approvals = deferred<ApprovalConfigResponse>();
    const policy = deferred<ModelPolicy>();
    const cost = deferred<CostSummary>();
    connection.client = readyClient({
      getProjectTrust: vi.fn(() => trust.promise),
      getApprovalsSettings: vi.fn(() => approvals.promise),
      getModelPolicySettings: vi.fn(() => policy.promise),
      getCostSummary: vi.fn(() => cost.promise),
    });
    const { preparation } = mountChips({ tracking: true });

    expect(preparation.ready()).toBe(false);
    expect(preparation.pending()).toEqual(
      expect.arrayContaining(["project-approvals", "model-policy", "session-limits", "session-cost"]),
    );
    expect(preparation.pending()).not.toContain("project-trust");
    expect(screen.queryByTestId("status-chip-posture")).toBeNull();
    expect(screen.queryByTestId("status-chip-model")).toBeNull();

    approvals.resolve(approvalsDoc("balanced"));
    policy.resolve(policyDoc);
    await waitFor(() => expect(preparation.pending()).toEqual(["session-cost"]));
    expect(preparation.ready()).toBe(false);

    cost.resolve(costDoc("s1", 0.12));
    await waitFor(() => expect(preparation.ready()).toBe(true));
    expect(screen.getByTestId("status-chip-trust").textContent).toBe("Checking trust…");
    trust.resolve(emptyTrust);
    await waitFor(() => expect(screen.getByTestId("status-chip-trust").textContent).toBe("Trust"));
    expect(screen.getByTestId("status-chip-posture").textContent).toBe("Balanced");
    expect(screen.getByTestId("status-chip-model").textContent).toBe("coordinator-1");
    expect(screen.getByTestId("status-chip-cost").textContent).toBe("$0.12");
  });

  it("stays prepared while a loaded cost summary refreshes", async () => {
    const refresh = deferred<CostSummary>();
    const getCostSummary = vi.fn()
      .mockResolvedValueOnce(costDoc("s1", 0.12))
      .mockReturnValueOnce(refresh.promise);
    const client = readyClient({ getCostSummary });
    connection.client = client;
    const { preparation, costStore } = mountChips({ tracking: true });
    await waitFor(() => expect(preparation.ready()).toBe(true));

    void costStore.refreshSession(client, "s1");
    expect(costStore.state.loading).toBe(true);
    expect(preparation.ready()).toBe(true);

    refresh.resolve(costDoc("s1", 0.2));
    await waitFor(() => expect(screen.getByTestId("status-chip-cost").textContent).toBe("$0.20"));
  });

  it("keeps the project chips mounted while the presented chat changes", async () => {
    const secondCost = deferred<CostSummary>();
    const getCostSummary = vi.fn((sessionId: string) =>
      sessionId === "s1" ? Promise.resolve(costDoc("s1", 0.12)) : secondCost.promise,
    );
    const getProjectTrust = vi.fn(async () => emptyTrust);
    connection.client = readyClient({ getCostSummary, getProjectTrust });
    const [sessionId, setSessionId] = createSignal("s1");
    const { preparation } = mountChips({ tracking: true, sessionId });
    await waitFor(() => expect(preparation.ready()).toBe(true));
    const posture = screen.getByTestId("status-chip-posture");
    const trust = screen.getByTestId("status-chip-trust");
    const cost = screen.getByTestId("status-chip-cost");
    expect(cost.textContent).toBe("$0.12");

    setSessionId("s2");
    await waitFor(() => expect(getCostSummary).toHaveBeenCalledWith("s2"));
    expect(screen.getByTestId("status-chip-posture")).toBe(posture);
    expect(screen.getByTestId("status-chip-trust")).toBe(trust);
    expect(posture.textContent).toBe("Balanced");
    expect(screen.getByTestId("status-chip-cost")).toBe(cost);
    expect(cost.textContent).toBe("");
    expect(getProjectTrust).toHaveBeenCalledTimes(1);

    secondCost.resolve(costDoc("s2", 0.34));
    await waitFor(() => expect(cost.textContent).toBe("$0.34"));
  });

  it("holds the displayed approval level while a settings revision re-reads", async () => {
    const refresh = deferred<ApprovalConfigResponse>();
    const getApprovalsSettings = vi
      .fn<() => Promise<ApprovalConfigResponse>>()
      .mockResolvedValueOnce(approvalsDoc("balanced"))
      .mockReturnValueOnce(refresh.promise);
    connection.client = readyClient({ getApprovalsSettings });
    const { appStore, preparation } = mountChips();
    await waitFor(() => expect(preparation.ready()).toBe(true));
    const posture = screen.getByTestId("status-chip-posture");
    expect(posture.textContent).toBe("Balanced");

    appStore.actions.bumpApprovalsRevision();
    await waitFor(() => expect(getApprovalsSettings).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("status-chip-posture")).toBe(posture);
    expect(posture.textContent).toBe("Balanced");
    expect(preparation.ready()).toBe(true);

    refresh.resolve(approvalsDoc("strict"));
    await waitFor(() => expect(posture.textContent).toBe("Strict"));
  });

  it("re-reads trust on a project-trust revision without blanking the chip", async () => {
    const refresh = deferred<ProjectTrust>();
    const getProjectTrust = vi
      .fn<() => Promise<ProjectTrust>>()
      .mockResolvedValueOnce(emptyTrust)
      .mockReturnValueOnce(refresh.promise);
    connection.client = readyClient({ getProjectTrust });
    const { appStore, preparation } = mountChips();
    await waitFor(() => expect(preparation.ready()).toBe(true));
    const chip = screen.getByTestId("status-chip-trust");
    expect(chip.textContent).toBe("Trust");

    appStore.actions.bumpProjectTrustRevision();
    await waitFor(() => expect(getProjectTrust).toHaveBeenCalledTimes(2));
    expect(chip.textContent).toBe("Trust");
    expect(chip.getAttribute("aria-busy")).toBeNull();

    refresh.resolve({ ...emptyTrust, surfaces: [] });
    await waitFor(() => expect(chip.textContent).toBe("Trust"));
  });
});

describe("ComposerStatusChips project trust state", () => {
  it("rechecks current files after acknowledging the shared comparison", async () => {
    const changed = { ...emptyTrust, unread_count: 1 };
    const getProjectTrust = vi.fn(async () => changed);
    const openProjectTrustReview = vi.fn(async () => emptyTrust);
    connection.client = readyClient({ getProjectTrust, openProjectTrustReview });
    mountChips();
    const chip = screen.getByTestId("status-chip-trust");
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("unseen"));
    fireEvent.click(chip);
    fireEvent.click(screen.getByTestId("trust-open-review"));
    await waitFor(() => expect(getProjectTrust).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("unseen"));
    expect(openProjectTrustReview).toHaveBeenCalledExactlyOnceWith("p1");
  });

  it("marks all trusted by clearing unread changes and turning trust green", async () => {
    const change = {
      id: "c1",
      root_id: "r1",
      root_label: "Root",
      path: "AGENTS.md",
      surface_ids: ["agents_md" as const],
      kind: "added" as const,
      before: "",
      after: "instructions",
    };
    const unread = {
      ...emptyTrust,
      unread_count: 1,
      review: {
        id: "review",
        project_id: "p1",
        changes: [change],
      },
    };
    const reviewed = {
      ...emptyTrust,
      unread_count: 0,
      review: {
        id: "review",
        project_id: "p1",
        changes: [change],
      },
    };
    const getProjectTrust = vi.fn()
      .mockResolvedValueOnce(unread)
      .mockResolvedValueOnce(reviewed);
    const openProjectTrustReview = vi.fn(async () => reviewed);
    const navigate = vi.fn();
    setStatusChipNavigationSink(navigate);
    connection.client = readyClient({ getProjectTrust, openProjectTrustReview });
    mountChips();
    const chip = screen.getByTestId("status-chip-trust");
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("unseen"));
    fireEvent.click(chip);
    const markButton = screen.getByTestId("trust-mark-all-trusted");
    expect(markButton.textContent).toBe("Mark all trusted");
    expect(markButton.hasAttribute("disabled")).toBe(false);

    const popover = screen.getByTestId("status-popover-trust");
    const linkTexts = Array.from(popover.querySelectorAll(".den-status-popover__manage")).map((el) => el.textContent);
    expect(linkTexts).toEqual(["Open trust", "Trust settings", "Mark all trusted"]);

    fireEvent.click(markButton);
    await waitFor(() => expect(openProjectTrustReview).toHaveBeenCalledExactlyOnceWith("p1"));
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("seen"));
  });

  it("is a no-op when mark all trusted is clicked with nothing to update", async () => {
    const reviewed = {
      ...emptyTrust,
      unread_count: 0,
      review: {
        id: "review",
        project_id: "p1",
        changes: [{
          id: "c1",
          root_id: "r1",
          root_label: "Root",
          path: "AGENTS.md",
          surface_ids: ["agents_md" as const],
          kind: "added" as const,
          before: "",
          after: "instructions",
        }],
      },
    };
    const openProjectTrustReview = vi.fn(async () => reviewed);
    connection.client = readyClient({
      getProjectTrust: vi.fn(async () => reviewed),
      openProjectTrustReview,
    });
    mountChips();
    const chip = screen.getByTestId("status-chip-trust");
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("seen"));
    fireEvent.click(chip);
    const markButton = screen.getByTestId("trust-mark-all-trusted");
    fireEvent.click(markButton);
    expect(openProjectTrustReview).not.toHaveBeenCalled();
    expect(chip.getAttribute("data-state")).toBe("seen");
  });

  it("refreshes trust when a project folder is detached", async () => {
    const root: ProjectRoot = { id: "root", label: "Folder", path: "/fixture/root", is_primary: true, kind: "attached", added_at: "2026-09-15T00:00:00Z" };
    const [roots, setRoots] = createSignal<readonly ProjectRoot[]>([root]);
    const getProjectTrust = vi.fn<() => Promise<ProjectTrust>>()
      .mockResolvedValueOnce(emptyTrust)
      .mockResolvedValueOnce({ ...emptyTrust, unread_count: 1 });
    connection.client = readyClient({ getProjectTrust });
    mountChips({ get roots() { return roots(); } });
    const chip = screen.getByTestId("status-chip-trust");
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("empty"));
    setRoots([]);
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("unseen"));
    expect(getProjectTrust).toHaveBeenCalledTimes(2);
  });

  it("reports a failed refresh instead of showing a stale clear indicator", async () => {
    const getProjectTrust = vi.fn<() => Promise<ProjectTrust>>()
      .mockResolvedValueOnce(emptyTrust)
      .mockRejectedValueOnce(new Error("Configuration cannot be read"));
    connection.client = readyClient({ getProjectTrust });
    const { appStore } = mountChips();
    const chip = screen.getByTestId("status-chip-trust");
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("empty"));
    appStore.actions.bumpProjectTrustRevision();
    await waitFor(() => expect(chip.getAttribute("data-state")).toBe("unavailable"));
    expect(chip.textContent).toBe("Trust unavailable");
    fireEvent.click(chip);
    expect(screen.getByTestId("status-popover-trust").textContent).not.toContain("No unread configuration changes");
  });

  it("keeps an explicit chip for a project with no trust surfaces", async () => {
    connection.client = readyClient();
    const { preparation } = mountChips();
    await waitFor(() => expect(preparation.ready()).toBe(true));

    const chip = screen.getByTestId("status-chip-trust");
    expect(chip.textContent).toBe("Trust");
    expect(chip.getAttribute("data-state")).toBe("empty");

    fireEvent.click(chip);
    expect(screen.getByTestId("status-popover-trust").textContent).toContain(
      "No unread changes.",
    );
  });

  it("distinguishes loading from unavailable trust", async () => {
    const trust = deferred<ProjectTrust>();
    connection.client = readyClient({ getProjectTrust: vi.fn(() => trust.promise) });
    mountChips();
    const chip = screen.getByTestId("status-chip-trust");
    expect(chip.textContent).toBe("Checking trust…");
    expect(chip.getAttribute("aria-busy")).toBe("true");

    trust.reject(new Error("unavailable"));
    await waitFor(() => expect(chip.textContent).toBe("Trust unavailable"));
    expect(chip.hasAttribute("data-tip")).toBe(false);
    expect(chip.getAttribute("aria-busy")).toBeNull();
  });

  it("settles preparation when trust cannot load", async () => {
    connection.client = readyClient({ getProjectTrust: vi.fn(async () => { throw new Error("unavailable"); }) });
    const { preparation } = mountChips();
    await waitFor(() => expect(preparation.ready()).toBe(true));
    expect(screen.getByTestId("status-chip-trust").textContent).toBe("Trust unavailable");
  });

  it("reports trust unavailable without a backend connection", () => {
    const { preparation } = mountChips();
    expect(preparation.ready()).toBe(true);
    const chip = screen.getByTestId("status-chip-trust");
    expect(chip.textContent).toBe("Trust unavailable");
    expect(screen.queryByTestId("status-chip-posture")).toBeNull();
  });

});
