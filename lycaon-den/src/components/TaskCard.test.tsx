import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import {
  TASK_CARD_ACTIVITY_COOLDOWN_MS,
  TaskCard,
  TaskCardShell,
} from "./TaskCard.tsx";
import type { ToolPartView } from "../chat/tool/tool-part-model.ts";
import type { WorkerTask } from "../api/types.ts";

const runningPart: ToolPartView = {
  id: "tc-1",
  toolCallId: "tc-1",
  assistantMessageId: "assistant-message",
  messageId: "m1",
  tool: "task",
  kind: "task",
  status: "completed",
  args: {
    agent_type: "implementer",
    brief: { goal: "Build game", done_when: ["Return the completed game."] },
  },
  output: JSON.stringify({ job_id: "job-1", status: "enqueued" }),
  error: null,
};

const runningWorker = (): WorkerTask => ({
  id: "job-1",
  parent_session_id: "sess-1",
  agent_type: "implementer",
  status: "running",
  created_at: "2026-01-01T00:00:00Z",
});

describe("TaskCard", () => {
  it.each([false, true])(
    "shows cancellation distinctly with persisted summary=%s",
    (withSummary) => {
      const worker: WorkerTask = {
        ...runningWorker(),
        status: "canceled",
        tool_loops_used: 2,
      };
      const part: ToolPartView = {
        ...runningPart,
        ...(withSummary
          ? {
              workerSummary: {
                worker_id: worker.id,
                child_session_id: "child",
                agent_type: "implementer",
                status: "canceled" as const,
                envelope: '<task state="canceled"></task>',
              },
            }
          : {}),
      };
      const { container } = render(() => (
        <TaskCard part={part} layout="chat" worker={() => worker} />
      ));
      expect(
        container.querySelector(".den-task-card")?.getAttribute("data-task-status"),
      ).toBe("canceled");
    },
  );

  it("updates an open completion card when the worker merge lands", async () => {
    const [worker, setWorker] = createSignal<WorkerTask>({
      ...runningWorker(),
      status: "complete",
      merge_status: "pending",
      result: { status: "open", summary: "Built game" },
    });
    const part: ToolPartView = {
      ...runningPart,
      workerSummary: {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "open",
        envelope: '<task job_id="job-1" state="open"></task>',
      },
    };
    const { container } = render(() => (
      <TaskCard part={part} layout="chat" worker={worker} />
    ));
    const card = container.querySelector(".den-task-card");
    expect(card?.getAttribute("data-task-status")).toBe("open");

    setWorker({ ...worker(), merge_status: "merged" });
    await Promise.resolve();

    expect(card?.getAttribute("data-task-status")).toBe("done");
    expect(screen.getByTestId("task-card-status").classList.contains("den-task-card-status--done")).toBe(true);
    expect(part.workerSummary?.status).toBe("open");
  });

  it("applies running animation class when worker is active", () => {
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={runningWorker} />
    ));
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeNull();
    const card = container.querySelector(".den-task-card");
    expect(card).toBeTruthy();
    expect(container.querySelector(".den-task-card-header")).toBeTruthy();
    expect(screen.getByTestId("task-card-status").classList.contains("den-task-card-status--running")).toBe(true);
    expect(screen.queryByTestId("citation-grounding-badge")).toBeNull();
    expect(screen.queryByTestId("task-card-evidence")).toBeNull();
    expect(screen.queryByTestId("worker-evidence-section")).toBeNull();
  });

  it("shows the turns chip and no progress bar between batches", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      tool_loops_used: 2,
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    expect(container.querySelector(".den-worker-turns")?.textContent).toBe("2 turns");
    expect(container.querySelector('[data-testid="task-card-progress"]')).toBeNull();
  });

  it("updates the turns chip as the round advances", async () => {
    const [worker, setWorker] = createSignal<WorkerTask>({
      ...runningWorker(),
      tool_loops_used: 2,
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    expect(container.querySelector(".den-worker-turns")?.textContent).toBe("2 turns");
    setWorker({ ...worker(), tool_loops_used: 5 });
    await Promise.resolve();
    expect(container.querySelector(".den-worker-turns")?.textContent).toBe("5 turns");
  });

  it("drops the progress bar once the worker is finished", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      status: "complete",
      tool_loops_used: 12,
      turn_tool_calls: 4,
      turn_tools_done: 1,
      result: { status: "complete", summary: "done" },
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    expect(container.querySelector('[data-testid="task-card-progress"]')).toBeNull();
  });

  // The tools inside a turn are most of its wall-clock; this is the mark that moves while they run.
  it("renders the in-flight tool batch", async () => {
    const [worker, setWorker] = createSignal<WorkerTask>({
      ...runningWorker(),
      tool_loops_used: 34,
      tool_calls_used: 128,
      turn_tool_calls: 4,
      turn_tools_done: 1,
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    const batchFill = () =>
      container.querySelector(".den-task-card-progress-fill--batch") as HTMLElement;
    expect(batchFill()?.style.width).toBe("25%");
    expect(
      container
        .querySelector('[data-testid="task-card-progress-batch"]')
        ?.getAttribute("data-batch-total"),
    ).toBe("4");

    setWorker({ ...worker(), turn_tools_done: 3 });
    await Promise.resolve();
    expect(batchFill()?.style.width).toBe("75%");

    setWorker({ ...worker(), tool_loops_used: 35, turn_tools_done: undefined, turn_tool_calls: undefined });
    await Promise.resolve();
    expect(screen.queryByTestId("task-card-progress-batch")).toBeNull();
  });

  it("shows turns and tool calls as separate chips", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      tool_loops_used: 12,
      tool_calls_used: 128,
      turn_tool_calls: 6,
      turn_tools_done: 2,
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    expect(container.querySelector(".den-worker-turns")?.textContent).toBe("12 turns");
    expect(screen.getByTestId("worker-tool-calls").textContent).toBe("128 tools · 2/6");
  });

  it("omits the tool chip until the first call settles", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      tool_loops_used: 1,
    });
    render(() => <TaskCard part={runningPart} layout="chat" worker={worker} />);
    expect(screen.queryByTestId("worker-tool-calls")).toBeNull();
  });

  it("names the in-flight batch in the progress aria label", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      tool_loops_used: 12,
      turn_tool_calls: 6,
      turn_tools_done: 2,
    });
    const { container } = render(() => (
      <TaskCard part={runningPart} layout="chat" worker={worker} />
    ));
    expect(
      container
        .querySelector('[data-testid="task-card-progress"]')
        ?.getAttribute("aria-label"),
    ).toBe("Tool 2 of 6 in this turn");
  });

  it("renders the worker context ring from context_usage", () => {
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      context_usage: {
        prompt_tokens: 150_000,
        window: 200_000,
        compaction_threshold: 140_000,
      },
    });
    render(() => <TaskCard part={runningPart} layout="chat" worker={worker} />);
    const ring = screen.getByTestId("worker-context-meter");
    expect(ring.textContent).toContain("75%");
    expect(ring.getAttribute("data-tier")).toBe("warn");
    expect(ring.getAttribute("aria-label")).toBe(
      "150,000 / 200,000 tokens (75% of context) · compacts at 140,000",
    );
  });

  it("hides the worker context ring until context_usage arrives", () => {
    render(() => (
      <TaskCard part={runningPart} layout="chat" worker={runningWorker} />
    ));
    expect(screen.queryByTestId("worker-context-meter")).toBeNull();
  });

  it("keeps the context ring consistent as worker state arrives, updates, and clears", async () => {
    const [worker, setWorker] = createSignal<WorkerTask | undefined>(undefined);
    render(() => <TaskCard part={runningPart} layout="chat" worker={worker} />);

    setWorker({
      ...runningWorker(),
      context_usage: {
        prompt_tokens: 50_000,
        window: 200_000,
        compaction_threshold: 140_000,
      },
    });
    await Promise.resolve();
    expect(screen.getByTestId("worker-context-meter").textContent).toContain("25%");

    setWorker({
      ...runningWorker(),
      context_usage: {
        prompt_tokens: 150_000,
        window: 200_000,
        compaction_threshold: 140_000,
      },
    });
    await Promise.resolve();
    expect(screen.getByTestId("worker-context-meter").textContent).toContain("75%");

    setWorker(undefined);
    await Promise.resolve();
    expect(screen.queryByTestId("worker-context-meter")).toBeNull();
  });

  it("opens worker evidence in drawer when traced is clicked", () => {
    const onOpenWorkerEvidence = vi.fn();
    const grounding = {
      traced: true,
      checks: [
        {
          id: "url_citations",
          label: "URL citations",
          status: "passed" as const,
          kind: "citation" as const,
          summary: "1 URL(s) matched web tool evidence",
          matched: ["https://example.com/a"],
        },
        {
          id: "implementer_artifact",
          label: "Workspace artifact",
          status: "passed" as const,
          kind: "lifecycle" as const,
          summary: "Mutation tools or workspace changes recorded",
        },
      ],
    };
    const worker = (): WorkerTask => ({
      ...runningWorker(),
      status: "complete",
      result: { status: "complete", summary: "done", grounding },
    });
    render(() => (
      <TaskCard
        part={runningPart}
        layout="chat"
        worker={worker}
        onOpenWorkerEvidence={onOpenWorkerEvidence}
      />
    ));
    expect(screen.getByTestId("citation-grounding-badge")).toBeTruthy();
    screen.getByTestId("citation-grounding-badge").click();
    expect(onOpenWorkerEvidence).toHaveBeenCalledTimes(1);
  });

  it("offers Open worker and Copy job ID on the card context menu", async () => {
    const onOpen = vi.fn();
    const { container } = render(() => (
      <TaskCard
        part={runningPart}
        layout="chat"
        worker={runningWorker}
        onOpenWorker={onOpen}
      />
    ));
    const card = container.querySelector('[data-testid="task-card"]') as HTMLElement;
    expect(card.getAttribute("data-job-id")).toBe("job-1");
    fireEvent.contextMenu(card);
    expect(screen.getByTestId("task-card-menu-open")).toBeTruthy();
    expect(screen.getByTestId("task-card-menu-copy-job-id")).toBeTruthy();
    fireEvent.click(screen.getByTestId("task-card-menu-open"));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  function renderActivity(activity: () => string) {
    render(() => (
      <TaskCardShell
        agent={() => "implementer"}
        status={() => "running"}
        body={() => "Build game"}
        activity={activity}
        grounding={() => undefined}
        worker={() => undefined}
      />
    ));
  }

  it("shows the first activity change at once, then rate-limits the rest", async () => {
    vi.useFakeTimers();
    const [activity, setActivity] = createSignal("Reading files");
    renderActivity(activity);
    expect(screen.getByText("Reading files")).toBeTruthy();

    setActivity("Scanning src");
    await Promise.resolve();
    expect(screen.getByText("Scanning src")).toBeTruthy();

    setActivity("Editing main.go");
    await Promise.resolve();
    expect(screen.getByText("Scanning src")).toBeTruthy();

    setActivity("Running tests");
    await Promise.resolve();
    expect(screen.getByText("Scanning src")).toBeTruthy();

    // Intermediate lines collapse into one commit at the end of the cooldown.
    await vi.advanceTimersByTimeAsync(TASK_CARD_ACTIVITY_COOLDOWN_MS);
    expect(screen.getByText("Running tests")).toBeTruthy();

    vi.useRealTimers();
  });

  // Continuous updates still commit within each cooldown.
  it("keeps advancing while the line changes faster than the cooldown", async () => {
    vi.useFakeTimers();
    const [activity, setActivity] = createSignal("Reading files");
    renderActivity(activity);

    setActivity("thinking · 1 tokens");
    await Promise.resolve();

    for (let tick = 2; tick <= 40; tick++) {
      setActivity(`thinking · ${tick} tokens`);
      await vi.advanceTimersByTimeAsync(200);
    }

    expect(screen.queryByText("Reading files")).toBeNull();
    const shown = screen.getByText(/^thinking · \d+ tokens$/).textContent ?? "";
    const tick = Number(shown.match(/(\d+)/)?.[1]);
    // Never more than one cooldown behind the newest line.
    expect(tick).toBeGreaterThanOrEqual(34);

    vi.useRealTimers();
  });
});
