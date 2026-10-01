import { describe, expect, it, vi } from "vitest";
import { at } from "../../test/at.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { WorkersTab } from "./WorkersTab.tsx";
import type { WorkerTask } from "../../api/types.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";

const noop = () => {};

const running: WorkerTask = {
  id: "job-1",
  child_session_id: "child-1",
  parent_session_id: "sess-1",
  agent_type: "implementer",
  status: "running",
  brief: "Build the game",
  created_at: "2026-01-01T00:00:00Z",
} as WorkerTask;

// Completed but its branch is still open on the overlay — merge pending.
const mergePending: WorkerTask = {
  id: "job-2",
  parent_session_id: "sess-1",
  agent_type: "reviewer",
  status: "complete",
  merge_status: "pending",
  brief: "Review the diff",
  created_at: "2026-01-01T00:01:00Z",
} as WorkerTask;

// Completed and landed on primary — terminal, should not appear.
const merged: WorkerTask = {
  id: "job-3",
  parent_session_id: "sess-1",
  agent_type: "scout",
  status: "complete",
  merge_status: "merged",
  brief: "Survey the repo",
  created_at: "2026-01-01T00:02:00Z",
} as WorkerTask;

const failed: WorkerTask = {
  id: "job-fail",
  parent_session_id: "sess-1",
  agent_type: "implementer",
  status: "failed" as const,
  brief: "Broken dispatch",
  created_at: "2026-01-01T00:03:00Z",
  failure: {
    code: "PROGRESS_MISSING",
    title: "Progress-gated tools require a progress checklist first",
    message: "No progress checklist yet.",
  },
} as WorkerTask;

describe("WorkersTab", () => {
  it("shows the empty hint when there are no session workers", () => {
    render(() => (
      <WorkersTab workers={[]} selectedId={null} onSelect={noop} onCancel={noop} />
    ));
    expect(screen.getByTestId("workers-tab").textContent).toMatch(
      /No workers in this session/i,
    );
    expect(screen.queryByTestId("workers-tab-row")).toBeNull();
  });

  it("lists all session workers and hides failed ones when verbose is off", () => {
    const onSelect = vi.fn();
    render(() => (
      <WorkersTab
        workers={[running, mergePending, merged, failed]}
        selectedId={null}
        onSelect={onSelect}
        onCancel={noop}
        verboseMode={false}
      />
    ));
    expect(screen.getAllByTestId("workers-tab-row")).toHaveLength(3);
    fireEvent.click(at(screen.getAllByRole("button", { pressed: false }), 0));
    expect(onSelect).toHaveBeenCalled();
  });

  it("lists failed workers when verbose is on", () => {
    render(() => (
      <WorkersTab
        workers={[merged, failed]}
        selectedId={null}
        onSelect={noop}
        onCancel={noop}
        verboseMode
      />
    ));
    expect(screen.getAllByTestId("workers-tab-row")).toHaveLength(2);
    expect(screen.getByTestId("workers-tab").textContent).toMatch(/Broken dispatch/i);
  });

  it("shows pending child checkpoint status on the worker row", () => {
    render(() => (
      <WorkersTab
        workers={[running]}
        pendingCheckpoints={[
          {
            checkpointId: "checkpoint-1",
            sessionId: "child-1",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "2026-01-01T00:00:01Z",
            tool_approval: toolApprovalFixture({ command: "git push origin main" }),
          },
        ]}
        selectedId={null}
        onSelect={noop}
        onCancel={noop}
      />
    ));
    expect(screen.getByTestId("workers-tab-approval").textContent).toBe(
      "Waiting for your approval",
    );
    expect(
      screen
        .getByTestId("workers-tab-row")
        .classList.contains("den-workers-tab-row--approval"),
    ).toBe(true);
  });

  it.each(["waiting", "held"] as const)("keeps Cancel visible for a %s worker", (status) => {
    const onCancel = vi.fn();
    render(() => <WorkersTab workers={[{ ...running, status }]} selectedId={null} onSelect={noop} onCancel={onCancel} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel implementer worker" }));
    expect(onCancel).toHaveBeenCalledWith(running.id);
  });

  it("offers Cancel only for cancellable workers and fires onCancel", () => {
    const onCancel = vi.fn();
    render(() => (
      <WorkersTab
        workers={[running, mergePending]}
        selectedId={null}
        onSelect={noop}
        onCancel={onCancel}
      />
    ));
    const cancels = screen.getAllByText("Cancel");
    expect(cancels).toHaveLength(1); // only the running worker
    fireEvent.click(at(cancels, 0));
    expect(onCancel).toHaveBeenCalledWith("job-1");
  });

  it("disables + acks the Cancel button while busy, no-ops a second click, and re-enables when it clears", () => {
    const [busy, setBusy] = createSignal<string | null>("job-1");
    const onCancel = vi.fn();
    render(() => (
      <WorkersTab
        workers={[running]}
        selectedId={null}
        onSelect={noop}
        onCancel={onCancel}
        busyWorkerId={busy}
      />
    ));
    const cancelBtn = screen.getByRole("button", { name: /Cancel implementer worker/i }) as HTMLButtonElement;
    expect(cancelBtn.disabled).toBe(true);
    expect(cancelBtn.textContent).toMatch(/Cancelling…/);
    expect(cancelBtn.classList.contains("den-workers-tab-cancel--busy")).toBe(true);

    // Second click while busy is a no-op — a disabled button does not fire onClick.
    fireEvent.click(cancelBtn);
    expect(onCancel).not.toHaveBeenCalled();

    // Re-enable after the busy signal clears; a click now fires onCancel.
    setBusy(null);
    expect(cancelBtn.disabled).toBe(false);
    expect(cancelBtn.textContent).toMatch(/^Cancel$/);
    fireEvent.click(cancelBtn);
    expect(onCancel).toHaveBeenCalledWith("job-1");
  });

  it("leaves another worker's Cancel button enabled while a different worker is busy", () => {
    const pendingRunning: WorkerTask = {
      ...running,
      id: "job-9",
      agent_type: "scout",
    };
    render(() => (
      <WorkersTab
        workers={[running, pendingRunning]}
        selectedId={null}
        onSelect={noop}
        onCancel={noop}
        busyWorkerId={() => "job-1"}
      />
    ));
    const busyBtn = screen.getByRole("button", { name: /Cancel implementer worker/i }) as HTMLButtonElement;
    const otherBtn = screen.getByRole("button", { name: /Cancel scout worker/i }) as HTMLButtonElement;
    expect(busyBtn.disabled).toBe(true);
    expect(otherBtn.disabled).toBe(false);
    expect(otherBtn.textContent).toMatch(/^Cancel$/);
  });

  it("does not surface worker needs_decision prompts", () => {
    const needsDecision: WorkerTask = {
      ...running,
      id: "job-dec",
      status: "complete",
      result: { status: "needs_decision" },
    };
    render(() => (
      <WorkersTab
        workers={[needsDecision]}
        selectedId={null}
        onSelect={noop}
        onCancel={noop}
      />
    ));
    expect(screen.queryByTestId("workers-tab-decision")).toBeNull();
  });

  it("marks the open worker for assistive tech without a persistent highlight", () => {
    render(() => (
      <WorkersTab
        workers={[running, mergePending]}
        selectedId="job-2"
        onSelect={noop}
        onCancel={noop}
      />
    ));
    expect(screen.getByRole("button", { pressed: true }).textContent).toMatch(/Review/i);
    expect(
      screen
        .getAllByTestId("workers-tab-row")
        .some((el) => el.classList.contains("den-workers-tab-row--selected")),
    ).toBe(false);
  });

  it("briefly acknowledges a worker row click", () => {
    render(() => (
      <WorkersTab
        workers={[running, mergePending]}
        selectedId={null}
        onSelect={noop}
        onCancel={noop}
      />
    ));
    const row = at(screen.getAllByTestId("workers-tab-row"), 0);
    const select = row.querySelector(".den-workers-tab-select");
    if (!select) throw new Error("worker row has no select target");
    fireEvent.click(select);
    expect(row.classList.contains("den-workers-tab-row--ack")).toBe(true);
  });
  it("shows rounds and tool calls as separate chips on a row", () => {
    const busy = {
      ...running,
      max_tool_loops: 40,
      tool_loops_used: 34,
      tool_calls_used: 128,
      turn_tool_calls: 6,
      turn_tools_done: 2,
    } as WorkerTask;
    const { container } = render(() => (
      <WorkersTab workers={[busy]} selectedId={null} onSelect={noop} onCancel={noop} />
    ));
    expect(container.querySelector(".den-worker-budget")?.textContent).toBe("34/40");
    expect(screen.getByTestId("worker-tool-calls").textContent).toBe("128 tools · 2/6");
  });
});
