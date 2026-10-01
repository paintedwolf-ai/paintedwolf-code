import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import type {
  CostSummary,
  ProjectCostReport,
  SettingsLimitsResponse,
} from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
vi.mock("../../platform/files/save-file.ts", () => ({ downloadExport: vi.fn(async () => undefined) }));
import { NANO_PER_USD } from "../../cost/nano-usd.ts";
import { ProjectCostView } from "./ProjectCostView.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";

const cost = (scope: "project" | "session", usd: number): CostSummary => ({
  scope,
  estimated_nano_usd: Math.round(usd * 1e9),
  estimate_coverage: "complete",
  pricing_provenance: [{ source: "models-dev" }],
  token_totals: { prompt: usd * 1000, completion: usd * 200 },
  coordinator: {
    estimated_nano_usd: Math.round(usd * 0.6 * 1e9),
    token_totals: { prompt: usd * 600, completion: usd * 120 },
  },
  workers: {
    estimated_nano_usd: Math.round(usd * 0.3 * 1e9),
    token_totals: { prompt: usd * 300, completion: usd * 60 },
    task_count: 2,
  },
  summarizer: {
    estimated_nano_usd: Math.round(usd * 0.1 * 1e9),
    token_totals: { prompt: usd * 100, completion: usd * 20 },
  },
});

const report: ProjectCostReport = {
  total: 2, session_count: 2, archived_session_count: 1, worker_task_count: 4, max_session_nano_usd: 3_000_000_000, max_session_tokens: 3600,
  summary: cost("project", 4.75),
  project_utilities: cost("project", 0.25),
  retired_sessions: cost("project", 0.5),
  sessions: [
    {
      session: {
        id: "session-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "project-1",
        title: "Build account settings",
        posture: "build",
        status: "idle",
        message_count: 8,
        created_at: "2026-08-01T12:00:00Z",
        activity_at: "2026-08-02T12:00:00Z",
      },
      cost: cost("session", 3),
    },
    {
      session: {
        id: "session-2",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "project-1",
        title: "Review accessibility",
        posture: "vet",
        status: "idle",
        archived_at: "2026-08-02T13:00:00Z",
        message_count: 4,
        created_at: "2026-08-01T13:00:00Z",
        activity_at: "2026-08-01T13:00:00Z",
      },
      cost: cost("session", 1),
    },
  ],
};

const limits = (): SettingsLimitsResponse => ({
  scope: "project",
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
  session_spend_ceiling_nano_usd: 5 * NANO_PER_USD,
  spend_warning_ratio: 0.8,
  spend_ceiling_enabled: true,
  spend_soft_stop: true,
  merged_from: ["bundled", "project"],
});

const globalLimits = (): SettingsLimitsResponse => ({
  ...limits(),
  scope: "global",
  session_spend_ceiling_nano_usd: 0,
  spend_ceiling_enabled: false,
  merged_from: ["bundled", "global"],
});

function mockClient(tracking = true) {
  return {
    getProjectCostReport: vi.fn(async (_projectId: string, query?: { search?: string; sort?: string; limit?: number; cursor?: string }) => {
      const sessions = report.sessions.filter((row) => row.session.title?.toLowerCase().includes(query?.search?.toLowerCase() ?? ""));
      return { ...report, sessions, total: sessions.length };
    }),
    getLimitsSettings: vi.fn(async (projectId?: string) =>
      projectId ? limits() : globalLimits(),
    ),
    getPricingSettings: vi.fn(async () => ({
      cost_tracking_enabled: tracking,
      sources: [],
      available_sources: [],
    })),
    updateLimitsSettings: vi.fn(async (body) => ({
      ...limits(),
      ...body,
      scope: "project",
    })),
  };
}

describe("ProjectCostView", () => {
  it("shows project metrics, composition, and filterable session costs", async () => {
    const client = mockClient();
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    expect(await screen.findByTestId("project-cost-kpis")).toBeTruthy();
    expect(
      screen
        .getByTestId("project-cost-stage")
        .querySelector(".den-browse-main--scroll .den-browse-main__content > .project-cost-content"),
    ).toBeTruthy();
    expect(screen.getByText("$4.75")).toBeTruthy();
    expect(screen.getByText("Build account settings")).toBeTruthy();
    expect(screen.getByTestId("project-cost-utilities-row")).toBeTruthy();
    expect(screen.getByTestId("project-cost-retired-row")).toBeTruthy();
    expect(screen.getByText("Project utilities")).toBeTruthy();
    expect(screen.getByTestId("project-cost-composition-usd")).toBeTruthy();
    expect(screen.getByTestId("project-cost-composition-tokens")).toBeTruthy();
    const charts = screen.getAllByRole("img", { hidden: true });
    expect(charts).toHaveLength(2);
    expect(charts[0]?.getAttribute("aria-label")).toContain("Coordinator 60%");
    expect(charts[1]?.getAttribute("aria-label")).toContain("Summarizer 10%");
    expect(screen.getByText("By estimated cost")).toBeTruthy();
    expect(screen.getByText("By tokens")).toBeTruthy();

    fireEvent.input(screen.getByTestId("project-cost-filter"), {
      target: { value: "accessibility" },
    });
    await waitFor(() => expect(screen.queryByText("Build account settings")).toBeNull());
    expect(screen.getByText("Review accessibility")).toBeTruthy();
  });

  it("marks a lower-bound session amount beside the figure, never inside it", async () => {
    const client = mockClient();
    client.getProjectCostReport.mockResolvedValue({
      ...report,
      sessions: [
        {
          ...report.sessions[0]!,
          cost: { ...report.sessions[0]!.cost, estimate_coverage: "lower_bound", unpriced_tokens: 40 },
        },
      ],
    });
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    const amount = await screen.findByText("$3.00");
    const mark = amount.parentElement?.querySelector(".den-status-mark");
    expect(amount.parentElement?.getAttribute("data-coverage")).toBe("lower_bound");
    expect(mark?.textContent).toBe("Lower bound");
    expect(screen.queryByText(/At least/)).toBeNull();
  });

  it("badges the project total as a lower bound when provider calls went unreported", async () => {
    const client = mockClient();
    client.getProjectCostReport.mockResolvedValue({
      ...report,
      summary: {
        ...report.summary,
        estimate_coverage: "lower_bound",
        unknown_calls: 2,
        unknown_charged_calls: 2,
      },
      sessions: [
        {
          ...report.sessions[0]!,
          cost: {
            ...report.sessions[0]!.cost,
            estimate_coverage: "lower_bound",
            unknown_calls: 2,
            unknown_charged_calls: 2,
          },
        },
      ],
    });
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    const badge = await screen.findByTestId("project-cost-lower-bound-badge");
    expect(badge.textContent).toBe("Lower bound · 2 provider calls with incomplete usage");
    expect(screen.getByTestId("project-cost-estimated-spend-coverage").textContent).toBe("Lower bound");
    expect(screen.getByText("$3.00")).toBeTruthy();
  });

  it("notes free local unreported calls without discrediting the amount", async () => {
    const client = mockClient();
    client.getProjectCostReport.mockResolvedValue({
      ...report,
      summary: { ...report.summary, unknown_calls: 2 },
      sessions: [
        {
          ...report.sessions[0]!,
          cost: { ...report.sessions[0]!.cost, unknown_calls: 2 },
        },
      ],
    });
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    const badge = await screen.findByTestId("project-cost-unreported-local-badge");
    expect(badge.textContent).toContain("2 local calls with incomplete usage");
    expect(screen.queryByTestId("project-cost-lower-bound-badge")).toBeNull();
    expect(screen.queryByTestId("project-cost-estimated-spend-coverage")).toBeNull();
    expect(screen.getByText("$3.00")).toBeTruthy();
  });

  it("saves configurable warning and stop values at project scope", async () => {
    const client = mockClient();
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    const warning = await screen.findByTestId("project-cost-warning-percent");
    fireEvent.input(warning, { target: { value: "70" } });
    fireEvent.input(screen.getByTestId("project-cost-ceiling-usd"), {
      target: { value: "12" },
    });
    fireEvent.click(screen.getByTestId("project-cost-soft-stop"));
    expect(await screen.findByRole("heading", { name: "Warn, then close out" })).toBeTruthy();
    expect(screen.getByLabelText("Warn at $8.40 then close out at $12.00")).toBeTruthy();
    fireEvent.click(screen.getByTestId("project-cost-save-guardrails"));

    await waitFor(() => expect(client.updateLimitsSettings).toHaveBeenCalledOnce());
    expect(client.updateLimitsSettings).toHaveBeenCalledWith(
      {
        spend_warning_ratio: 0.7,
        session_spend_ceiling_nano_usd: 12 * NANO_PER_USD,
        spend_soft_stop: false,
      },
      "project-1",
    );
    expect(screen.getByText("Saved to this project")).toBeTruthy();
  });

  it.each([
    [true, true, "land"],
    [true, false, "close out"],
    [false, true, "close out"],
    [false, false, "close out"],
  ] as const)("describes the effective landing mode (device %s, project %s)", async (deviceSoft, projectSoft, action) => {
    const client = mockClient();
    client.getLimitsSettings.mockImplementation(async (projectId) => projectId
      ? { ...limits(), spend_soft_stop: projectSoft }
      : { ...globalLimits(), spend_soft_stop: deviceSoft });
    render(() => <ProjectCostView projectId="project-1" projectName="Demo project"
      appStore={createAppStore()} client={client as never} onOpenSession={vi.fn()} />);
    expect(await screen.findByRole("heading", { name: `Warn, then ${action}` })).toBeTruthy();
    expect(screen.getByLabelText(`Warn at $4.00 then ${action} at $5.00`)).toBeTruthy();
    if (action === "close out") {
      expect(screen.queryByText(/can land its work before pausing/)).toBeNull();
      expect(screen.getByText(/closes out without another tool round/)).toBeTruthy();
    }
  });

  it("keeps existing data visible when tracking is off and offers the settings path", async () => {
    const client = mockClient(false);
    const onOpenCostSettings = vi.fn();
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
        onOpenCostSettings={onOpenCostSettings}
      />
    ));

    expect(await screen.findByTestId("project-cost-tracking-off")).toBeTruthy();
    fireEvent.click(screen.getByText("Open Settings → Cost"));
    expect(onOpenCostSettings).toHaveBeenCalledOnce();
    expect(screen.getByText("Build account settings")).toBeTruthy();
  });

  it("makes an inherited device stop explicit and prevents weakening it", async () => {
    const client = mockClient();
    client.getLimitsSettings.mockImplementation(async (projectId) =>
      projectId ? limits() : { ...limits(), scope: "global" },
    );
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    const enabled = await screen.findByTestId(
      "project-cost-guardrail-enabled",
    ) as HTMLInputElement;
    expect(enabled.disabled).toBe(true);
    expect(screen.getByText(/Device Settings requires a \$5\.00 stop/)).toBeTruthy();
    fireEvent.input(screen.getByTestId("project-cost-ceiling-usd"), {
      target: { value: "12" },
    });
    expect(screen.getByText(/cannot exceed the device limit of \$5\.00/)).toBeTruthy();
    expect(
      (screen.getByTestId("project-cost-save-guardrails") as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });

  it("stays live on cost SSE without stomping an in-progress ceiling edit", async () => {
    const client = mockClient();
    const costStore = createCostStore();
    const { unmount } = render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Demo project"
        appStore={createAppStore()}
        client={client as never}
        costStore={costStore}
        onOpenSession={vi.fn()}
      />
    ));

    expect(await screen.findByTestId("project-cost-kpis")).toBeTruthy();
    expect(screen.getByText("$4.75")).toBeTruthy();
    expect(costStore.state.liveConsumers).toBe(1);

    fireEvent.input(screen.getByTestId("project-cost-ceiling-usd"), {
      target: { value: "12" },
    });
    const limitCalls = client.getLimitsSettings.mock.calls.length;

    client.getProjectCostReport.mockResolvedValue({
      ...report,
      summary: cost("project", 9),
    });
    costStore.emitInvalidated();

    await waitFor(() => expect(screen.getByText("$9.00")).toBeTruthy());
    expect(
      (screen.getByTestId("project-cost-ceiling-usd") as HTMLInputElement).value,
    ).toBe("12");
    expect(client.getLimitsSettings.mock.calls.length).toBe(limitCalls);
    expect(client.getPricingSettings).toHaveBeenCalledTimes(1);

    unmount();
    expect(costStore.state.liveConsumers).toBe(0);
  });

  it("releases live cost work while its resident surface is idle", async () => {
    const client = mockClient();
    const costStore = createCostStore();
    const [presence, setPresence] = createSignal<"active" | "idle" | "pending">(
      "active",
    );
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectCostView
          projectId="project-1"
          projectName="Demo project"
          appStore={createAppStore()}
          client={client as never}
          costStore={costStore}
          onOpenSession={vi.fn()}
        />
      </ResidentPresenceProvider>
    ));

    await screen.findByTestId("project-cost-kpis");
    expect(costStore.state.liveConsumers).toBe(1);
    setPresence("idle");
    expect(costStore.state.liveConsumers).toBe(0);
    const calls = client.getProjectCostReport.mock.calls.length;
    costStore.emitInvalidated();
    await Promise.resolve();
    expect(client.getProjectCostReport).toHaveBeenCalledTimes(calls);
    setPresence("pending");
    await waitFor(() => {
      expect(client.getProjectCostReport.mock.calls.length).toBeGreaterThan(calls);
    });
    expect(costStore.state.liveConsumers).toBe(1);
  });

  it("keeps utility-only projects visible and exportable", async () => {
    const client = mockClient();
    client.getProjectCostReport.mockResolvedValue({
      ...report, total: 0, session_count: 0, archived_session_count: 0, worker_task_count: 0, max_session_nano_usd: 0, max_session_tokens: 0,
      summary: cost("project", 1),
      project_utilities: cost("project", 1),
      retired_sessions: cost("project", 0),
      sessions: [],
    });
    render(() => (
      <ProjectCostView
        projectId="project-1"
        projectName="Utility project"
        appStore={createAppStore()}
        client={client as never}
        onOpenSession={vi.fn()}
      />
    ));

    expect(await screen.findByTestId("project-cost-utilities-row")).toBeTruthy();
    expect((screen.getByTestId("project-cost-export") as HTMLButtonElement).disabled).toBe(false);
  });

  it("does not let an old project's live refresh overwrite the new project", async () => {
    let resolveOld!: (value: ProjectCostReport) => void;
    const oldRefresh = new Promise<ProjectCostReport>((resolve) => {
      resolveOld = resolve;
    });
    const client = mockClient();
    let projectOneCalls = 0;
    client.getProjectCostReport.mockImplementation(async (projectId: string) => {
      if (projectId === "project-2") {
        return { ...report, summary: cost("project", 7) };
      }
      projectOneCalls += 1;
      return projectOneCalls === 1 ? report : oldRefresh;
    });
    const costStore = createCostStore();
    const [projectId, setProjectId] = createSignal("project-1");
    render(() => (
      <ProjectCostView
        projectId={projectId()}
        projectName="Switching project"
        appStore={createAppStore()}
        client={client as never}
        costStore={costStore}
        onOpenSession={vi.fn()}
      />
    ));

    expect(await screen.findByText("$4.75")).toBeTruthy();
    costStore.emitInvalidated();
    await waitFor(() => expect(projectOneCalls).toBe(2));
    setProjectId("project-2");
    expect(await screen.findByText("$7.00")).toBeTruthy();

    resolveOld({ ...report, summary: cost("project", 99) });
    await Promise.resolve();
    expect(screen.queryByText("$99.00")).toBeNull();
    expect(screen.getByText("$7.00")).toBeTruthy();
  });
});

it("pages cost rows on the server and exports every page independently of the visible page", async () => {
  const client = mockClient();
  client.getProjectCostReport.mockImplementation(async (_id, query) => ({
    ...report, total: 80, session_count: 80,
    sessions: [report.sessions[query?.cursor ? 1 : 0]!],
    next_cursor: query?.cursor ? undefined : "page-2",
  }));
  render(() => <ProjectCostView projectId="project-1" projectName="Demo project" appStore={createAppStore()} client={client as never} onOpenSession={vi.fn()} />);
  await screen.findByText("Build account settings");
  fireEvent.click(screen.getByTestId("project-cost-pager-next"));
  await screen.findByText("Review accessibility");
  expect(screen.queryByText("Build account settings")).toBeNull();
  expect(client.getProjectCostReport).toHaveBeenCalledWith("project-1", expect.objectContaining({ cursor: "page-2", limit: 40 }));
  expect(client.getLimitsSettings).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByTestId("project-cost-export"));
  await waitFor(() => expect(downloadExport).toHaveBeenCalled());
  const calls = vi.mocked(downloadExport).mock.calls;
  const blob = calls[calls.length - 1]![0];
  const csv = await new Promise<string>((resolve) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.readAsText(blob); });
  expect(csv).toContain("Build account settings");
  expect(csv).toContain("Review accessibility");
  expect(client.getProjectCostReport).toHaveBeenCalledWith("project-1", { limit: 200, sort: "id" });
  expect(client.getProjectCostReport).toHaveBeenCalledWith("project-1", { limit: 200, sort: "id", cursor: "page-2" });
});
