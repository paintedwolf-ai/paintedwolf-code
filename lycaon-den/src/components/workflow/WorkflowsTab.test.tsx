import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { WorkflowsTab } from "./WorkflowsTab.tsx";
import type { WorkflowRun, WorkflowSummary } from "../../api/types.ts";

vi.mock("../../platform/files/save-file.ts", () => ({
  downloadExport: vi.fn(),
}));

import { downloadExport } from "../../platform/files/save-file.ts";

const noop = () => {};

function baseProps() {
  return {
    activeRun: null as WorkflowRun | null,
    catalog: [] as WorkflowSummary[],
    blueprintsById: {},
    pickerOpen: false,
    error: null as string | null,
    busy: false,
    onExit: noop,
    onPause: noop,
    onResume: noop,
    onAdvance: noop,
    onReviewInChat: noop,
    onOpenPicker: noop,
    onClosePicker: noop,
    onArmWorkflow: noop,
    onJumpToRun: noop,
  };
}

const planCatalog: WorkflowSummary[] = [
  {
    id: "plan",
    version: "1.0.0",
    name: "Plan",
    trigger: "/plan",
    phases: ["intake", "research", "expand", "review", "approve", "execute", "done"],
  },
  {
    id: "options",
    version: "1.0.0",
    name: "Options",
    trigger: "/options",
  },
  {
    id: "bugbash",
    version: "1.0.0",
    name: "Bugbash",
    trigger: "/bugbash",
  },
  {
    id: "security-survey",
    version: "1.0.0",
    name: "Security",
    trigger: "/security-survey",
    report_enabled: true,
  },
];

const runningPlanRun: WorkflowRun = {
  id: "run-1",
  session_id: "s",
  workflow_id: "plan",
  workflow_version: "1.0.0",
	revision: 1,
  status: "running",
  current_phase: "expand",
  ui: { current_phase_label: "Writing the plan" },
  created_at: "t",
  updated_at: "t",
};

const completeSurveyRun: WorkflowRun = {
  id: "run-survey",
  session_id: "s",
  workflow_id: "security-survey",
  workflow_version: "1.0.0",
	revision: 1,
  status: "complete",
  ui: { current_phase_label: "Done", report_available: true },
  current_phase: "done",
  created_at: "t",
  updated_at: "t",
};

describe("WorkflowsTab", () => {
  it("shows resting launch state when no active run", () => {
    render(() => <WorkflowsTab {...baseProps()} />);
    expect(screen.getByTestId("workflows-tab-empty")).toBeTruthy();
    expect(screen.getByTestId("pick-workflow-button").textContent).toBe("Pick a workflow");
  });

  it("renders human run header, phase progress, and status pill", () => {
    render(() => (
      <WorkflowsTab {...baseProps()} activeRun={runningPlanRun} catalog={planCatalog} />
    ));
    expect(screen.getByTestId("workflow-active-row").textContent).toContain("Plan");
    expect(screen.getByTestId("workflow-active-row").textContent).toContain("Writing the plan");
    expect(screen.getByTestId("workflow-phase-progress").textContent).toContain("step 3 of 7");
    expect(screen.getByTestId("workflow-status-pill").textContent).toBe("Running");
    expect(screen.queryByTestId("workflow-posture-chip")).toBeNull();
  });

  it("shows pause, advance, and leave while running — no overflow menu", () => {
    render(() => (
      <WorkflowsTab {...baseProps()} activeRun={runningPlanRun} catalog={planCatalog} />
    ));
    expect(screen.getByTestId("workflow-pause")).toBeTruthy();
    expect(screen.getByTestId("workflow-advance")).toBeTruthy();
    expect(screen.getByTestId("workflow-leave")).toBeTruthy();
    expect(screen.queryByTestId("workflow-actions-overflow")).toBeNull();
    expect(screen.queryByTestId("workflow-approve")).toBeNull();
  });

  it("leaves the run displayed by the panel", () => {
	const onExit = vi.fn();
	render(() => (
	  <WorkflowsTab {...baseProps()} activeRun={runningPlanRun} catalog={planCatalog} onExit={onExit} />
	));
	fireEvent.click(screen.getByTestId("workflow-leave"));
	expect(onExit).toHaveBeenCalledWith(runningPlanRun);
  });

  it("points an open approval gate to chat instead of approving in-tab", () => {
    const onReviewInChat = vi.fn();
    render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={{
          ...runningPlanRun,
          current_phase: "approve",
          ui: { current_phase_label: "Waiting for approval", human_approval_awaiting: true },
        }}
        catalog={planCatalog}
        onReviewInChat={onReviewInChat}
      />
    ));
    const review = screen.getByTestId("workflow-gate-review");
    expect(review.textContent).toContain("Awaiting your approval");
    expect(review.textContent).toContain("Review in chat");
    fireEvent.click(review);
    expect(onReviewInChat).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("workflow-pause")).toBeNull();
    expect(screen.getByTestId("workflow-leave")).toBeTruthy();
  });

  it("hides picker when arm is blocked by an active run", () => {
    render(() => (
      <WorkflowsTab {...baseProps()} activeRun={runningPlanRun} catalog={planCatalog} pickerOpen />
    ));
    expect(screen.queryByTestId("workflow-picker")).toBeNull();
    expect(screen.getByTestId("workflows-arm-blocked-hint")).toBeTruthy();
  });

  it("lists catalog workflows in the picker", () => {
    render(() => <WorkflowsTab {...baseProps()} pickerOpen catalog={planCatalog} />);
    expect(screen.getByTestId("workflow-picker")).toBeTruthy();
    expect(screen.getByTestId("workflow-picker-row-plan").textContent).toContain("/plan");
    expect(screen.getByTestId("workflow-picker-row-options").textContent).toContain("/options");
    expect(screen.getByTestId("workflow-picker-row-bugbash").textContent).toContain("/bugbash");
    expect(screen.getByTestId("workflow-picker-row-security-survey")).toBeTruthy();
  });

  it("toggles picker label without Cancel", () => {
    render(() => <WorkflowsTab {...baseProps()} pickerOpen catalog={planCatalog} />);
    expect(screen.getByTestId("pick-workflow-button").textContent).toBe("Hide workflows");
  });

  it("opens picker from the pick button", () => {
    const onOpenPicker = vi.fn();
    render(() => <WorkflowsTab {...baseProps()} onOpenPicker={onOpenPicker} />);
    fireEvent.click(screen.getByTestId("pick-workflow-button"));
    expect(onOpenPicker).toHaveBeenCalledOnce();
  });

  it("shows Download report only when the host authorizes it", () => {
    const downloadReport = vi.fn();
    const { unmount: unmountOk } = render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={completeSurveyRun}
        catalog={planCatalog}
        downloadReport={downloadReport}
      />
    ));
    expect(screen.getByTestId("workflow-download-report")).toBeTruthy();
    unmountOk();

    const { unmount: unmountRunning } = render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={{ ...completeSurveyRun, status: "running", ui: { current_phase_label: "Done", report_available: false } }}
        catalog={planCatalog}
        downloadReport={downloadReport}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
    unmountRunning();

    render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={{ ...completeSurveyRun, workflow_id: "plan", status: "complete", ui: { current_phase_label: "Done", report_available: false } }}
        catalog={planCatalog}
        downloadReport={downloadReport}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });

  it("offers the incomplete report and lifecycle controls for a blocked review", () => {
    const onResume = vi.fn();
    const onExit = vi.fn();
    render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={{ ...completeSurveyRun, status: "paused", pause_reason: "review_blocked" }}
        catalog={planCatalog}
        downloadReport={vi.fn()}
        onResume={onResume}
        onExit={onExit}
      />
    ));
    expect(screen.getByTestId("workflow-download-report")).toBeTruthy();
    expect(screen.getByTestId("workflow-state-hint").textContent).toContain("incomplete report");
    fireEvent.click(screen.getByTestId("workflow-resume"));
    expect(onResume).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId("workflow-leave"));
    expect(onExit).toHaveBeenCalledOnce();
  });

  it("downloads the report blob via downloadReport then downloadExport", async () => {
    const blob = new Blob(["%PDF"], { type: "application/pdf" });
    const downloadReport = vi.fn().mockResolvedValue({ blob, filename: "survey.pdf" });
    vi.mocked(downloadExport).mockClear();

    render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={completeSurveyRun}
        catalog={planCatalog}
        downloadReport={downloadReport}
      />
    ));
    fireEvent.click(screen.getByTestId("workflow-download-report"));
    await waitFor(() => {
      expect(downloadReport).toHaveBeenCalledWith("run-survey");
      expect(downloadExport).toHaveBeenCalledWith(blob, "survey.pdf");
    });
    expect(screen.getByTestId("workflow-download-report").getAttribute("data-state")).toBe(
      "ready",
    );
  });

  it("surfaces an error state when downloadReport rejects", async () => {
    const downloadReport = vi.fn().mockRejectedValue(new Error("gone"));
    vi.mocked(downloadExport).mockClear();

    render(() => (
      <WorkflowsTab
        {...baseProps()}
        activeRun={completeSurveyRun}
        catalog={planCatalog}
        downloadReport={downloadReport}
      />
    ));
    fireEvent.click(screen.getByTestId("workflow-download-report"));
    await waitFor(() => {
      expect(screen.getByTestId("workflow-download-report").getAttribute("data-state")).toBe(
        "error",
      );
      expect(screen.getByTestId("workflow-download-report-error")).toBeTruthy();
    });
    expect(downloadExport).not.toHaveBeenCalled();
  });
});
