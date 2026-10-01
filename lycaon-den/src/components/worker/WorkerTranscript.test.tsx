import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { WorkerTranscript } from "./WorkerTranscript.tsx";

import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";

vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface: vi.fn() }));
vi.mock("../../chat/composer/add-to-chat.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../chat/composer/add-to-chat.ts")>(),
  addToChat: vi.fn(async () => ({ ok: false, reason: "test" })),
}));

function runningWorker(id = "job-1"): WorkerTask {
  return {
    id,
    agent_type: "implement",
    status: "running",
    brief: "Fix the handler",
    created_at: "2026-01-01T00:00:00Z",
  };
}

function completeWorker(id = "job-1"): WorkerTask {
  return {
    id,
    agent_type: "implement",
    status: "complete",
    brief: "Fix the handler",
    created_at: "2026-01-01T00:00:00Z",
  };
}

function addWorkerToolOutputToChat(worker: WorkerTask) {
  const appStore = createAppStore();
  appStore.actions.applyWorkerTranscriptRows(worker.id, [{
    id: "worker-result-chat", ord: 2, role: "tool", origin: "tool",
    authority: "none", trust_tier: "untrusted", worker_id: worker.id,
    content: "worker output", created_at: "2026-01-01T00:00:01Z",
    tool_result: { content: "worker output", tool: "command", tool_call_id: "worker-command-chat",
      assistant_message_id: "worker-assistant-chat", ui_visibility: "benign" },
  }]);
  const view = render(() => <WorkerTranscript workerId={worker.id} workers={[worker]}
    appStore={appStore} projectDir="/tmp/p" />);
  const activity = view.container.querySelector<HTMLDetailsElement>(".den-activity-span");
  if (activity && !activity.open) fireEvent.click(activity.querySelector("summary")!);
  const card = view.container.querySelector<HTMLDetailsElement>('[data-tool-call-id="worker-command-chat"]')!;
  if (!card.open) fireEvent.click(card.querySelector("summary")!);
  fireEvent.click(within(card).getByTestId("tool-raw-add-to-chat"));
}

describe("WorkerTranscript", () => {
  it("adds worker tool output to the dispatching chat, citing the worker session", () => {
    vi.mocked(addToChat).mockClear();
    addWorkerToolOutputToChat({ ...completeWorker("job-chat"), project_id: "project-chat",
      child_session_id: "child-chat", parent_session_id: "parent-chat" });
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "search-hit", projectId: "project-chat",
        sessionId: "child-chat", sourceRef: "worker-command-chat" }),
      { destination: { projectId: "project-chat", sessionId: "parent-chat" } },
    );
  });

  it("asks for a chat when the worker has no dispatching chat", () => {
    vi.mocked(addToChat).mockClear();
    addWorkerToolOutputToChat({ ...completeWorker("job-orphan"), project_id: "project-chat",
      child_session_id: "child-chat" });
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "search-hit", sessionId: "child-chat" }),
      { destination: undefined },
    );
  });

  it("opens worker tool output in Files with the child session identity", () => {
    const appStore = createAppStore();
    appStore.actions.applyWorkerTranscriptRows("job-output", [{
      id: "worker-result-output", ord: 2, role: "tool", origin: "tool",
      authority: "none", trust_tier: "untrusted", worker_id: "job-output",
      content: "worker output", created_at: "2026-01-01T00:00:01Z",
      tool_result: { content: "worker output", tool: "command", tool_call_id: "worker-command-output",
        assistant_message_id: "worker-assistant-output", ui_visibility: "benign" },
    }]);
    const view = render(() => <WorkerTranscript workerId="job-output"
      workers={[{ ...completeWorker("job-output"), child_session_id: "child-output", project_id: "project-output" }]}
      appStore={appStore} projectDir="/tmp/p" />);
    const activity = view.container.querySelector<HTMLDetailsElement>(".den-activity-span");
    if (activity && !activity.open) fireEvent.click(activity.querySelector("summary")!);
    const card = view.container.querySelector<HTMLDetailsElement>('[data-tool-call-id="worker-command-output"]')!;
    if (!card.open) fireEvent.click(card.querySelector("summary")!);
    fireEvent.click(view.getByRole("button", { name: "Raw output in Files" }));
    expect(openFilesSurface).toHaveBeenCalledWith(expect.objectContaining({
      kind: "chat-content", projectId: "project-output", document: expect.objectContaining({
        sessionId: "child-output", messageId: "worker-result-output", toolCallId: "worker-command-output",
        content: { kind: "inline", text: "worker output", redaction: undefined },
      }),
    }));
    expect(card.querySelector("pre")).toBeNull();
  });

  it("shows authored HTML literally in the task brief", () => {
    const appStore = createAppStore();
    const { container } = render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[
          {
            ...runningWorker(),
            brief: "Use snippet() with <mark> tags.",
          },
        ]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(container.querySelector("mark")).toBeNull();
    expect(container.textContent).toContain("Use snippet() with <mark> tags.");
  });

  it("shows a settled approval chicklet only in its owning worker", () => {
    const appStore = createAppStore();
    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "worker-assistant",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        worker_id: "job-1",
        content: "",
        tool_calls: [{ id: "worker-command", name: "command", args: { command: "npm install" } }],
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "worker-result",
        ord: 2,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        worker_id: "job-1",
        content: "done",
        tool_result: {
          content: "done",
          tool: "command",
          tool_call_id: "worker-command",
          assistant_message_id: "worker-assistant",
          ui_visibility: "benign",
          checkpoint_decision: {
            checkpoint_id: "checkpoint-child",
            kind: "tool_approval",
            status: "approved",
            subject: "/Users/example/.npm",
            causing_command: "npm install",
          },
        },
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);
    const [selected, setSelected] = createSignal("job-1");
    render(() => (
      <WorkerTranscript
        workerId={selected()}
        workers={[
          { ...runningWorker("job-1"), child_session_id: "child-1" },
          { ...runningWorker("job-2"), child_session_id: "child-2" },
        ]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getAllByTestId("checkpoint-decision-chicklet")).toHaveLength(1);
    expect(screen.getByTestId("checkpoint-decision-chicklet").getAttribute("data-status")).toBe("approved");
    expect(screen.getByTestId("checkpoint-decision-subject").textContent).toBe("/Users/example/.npm");
    setSelected("job-2");
    expect(screen.queryByTestId("checkpoint-decision-chicklet")).toBeNull();
    setSelected("job-1");
    expect(screen.getAllByTestId("checkpoint-decision-chicklet")).toHaveLength(1);
  });

  it("renders Activity section with transcript messages and evidence separately", () => {
    const appStore = createAppStore();
    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "**Done**",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-activity")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: "Activity" })).toBeTruthy();
    expect(screen.getByTestId("message-stream")).toBeTruthy();
    expect(screen.queryByTestId("worker-working-indicator")).toBeNull();
  });

  it("shows a spinner at the bottom while the worker is still running", () => {
    const appStore = createAppStore();
    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "main.go" } }],
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[runningWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-working-indicator")).toBeTruthy();
    expect(screen.getByTestId("message-stream")).toBeTruthy();
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("shows only the spinner when running with no messages yet", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[runningWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-working-indicator")).toBeTruthy();
    expect(screen.queryByTestId("message-stream")).toBeNull();
    expect(screen.queryByText(/No activity yet/)).toBeNull();
  });

  it("shows pending child-session rows before coalesce flush", async () => {
    const appStore = createAppStore();
    const { queueWorkerTranscriptPatch } = await import(
      "../../chat/worker/worker-transcript-coalesce.ts"
    );
    queueWorkerTranscriptPatch(appStore, "job-1", {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [{ id: "tc1", name: "command", args: { command: "echo hi" } }],
      created_at: "2026-01-01T00:00:00Z",
    });

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[runningWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("message-stream")).toBeTruthy();
    expect(screen.getByTestId("worker-working-indicator")).toBeTruthy();
  });

  it("shows tool-turn preview while cache is cold, including after the worker finishes", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[
          {
            ...runningWorker(),
            tool_loops_used: 4,
            max_tool_loops: 40,
          },
        ]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-activity-preview").textContent).toBe(
      "4 of 40 tool turns so far",
    );
  });

  it("does not claim no activity when a finished worker has used tool turns", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[
          {
            ...completeWorker(),
            tool_loops_used: 12,
            max_tool_loops: 40,
          },
        ]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-activity-preview").textContent).toBe(
      "12 of 40 tool turns so far",
    );
    expect(screen.queryByText(/No activity yet/)).toBeNull();
  });

  it("flags the Activity section as working while the worker runs", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[runningWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    const activity = screen.getByTestId("worker-activity");
    expect(
      within(activity).getByTestId("worker-section-status").dataset.status,
    ).toBe("working");
  });

  it("settles the Task and Activity section cues when the worker is complete", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    const task = screen.getByTestId("worker-dispatch");
    const activity = screen.getByTestId("worker-activity");
    expect(
      within(task).getByTestId("worker-section-status").dataset.status,
    ).toBe("complete");
    expect(
      within(activity).getByTestId("worker-section-status").dataset.status,
    ).toBe("complete");
  });

  it("shows empty activity copy when the worker finished without messages", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByText("No activity yet for this worker.")).toBeTruthy();
    expect(screen.queryByTestId("worker-working-indicator")).toBeNull();
  });

  it("always shows coordination with an empty state when there are no notes or reservations", () => {
    const appStore = createAppStore();

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    expect(screen.getByTestId("worker-coordination")).toBeTruthy();
    expect(screen.getByText(/No coordination activity yet/i)).toBeTruthy();
  });

  it("renders coordination between activity and evidence with posted and received notes", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "session-1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project-1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    appStore.actions.setFindings("session-1", appStore.state.sessionViewEpoch, {
      findings: [
        { agent: "job-1", summary: "My note", ref: "mine.go" },
        { agent: "job-2", summary: "Peer note", ref: "peer.go" },
      ],
      revision: 1,
    });
    appStore.actions.setBoard({
      summary: "ok",
      repo: { languages: [], file_count: 0, generated_at: "2026-01-01T00:00:00Z" },
      roster: [
        {
          worker_id: "job-1",
          agent_type: "implement",
          status: "complete",
          reservations: ["pkg/reserved.go"],
        },
      ],
      cost: null,
      pack_content_hash: "hash",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "2026-01-01T00:00:00Z",
      now_line: "now",
    });

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    const coordination = screen.getByTestId("worker-coordination");
    expect(coordination).toBeTruthy();
    expect(screen.getByText("Notes posted")).toBeTruthy();
    expect(screen.getByText("My note")).toBeTruthy();
    expect(screen.getByText("Notes received")).toBeTruthy();
    expect(screen.getByText("Peer note")).toBeTruthy();
    expect(screen.getByText("Reservations")).toBeTruthy();
    expect(screen.getByText("pkg/reserved.go")).toBeTruthy();

    const pane = coordination.closest(".den-worker-transcript-pane");
    const sections = pane?.querySelectorAll("[data-section]");
    const keys = Array.from(sections ?? []).map((el) => el.getAttribute("data-section"));
    expect(keys.filter((key) => key !== "task")).toEqual([
      "activity", "coordination", "evidence",
    ]);
  });

  it("keeps scroll position when live worker and transcript rows update", async () => {
    const appStore = createAppStore();
    const [workers, setWorkers] = createSignal<WorkerTask[]>([runningWorker()]);
    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "First line",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);

    const { container } = render(() => (
      <WorkerTranscript workerId="job-1" workers={workers()} appStore={appStore} projectDir="/tmp/p" />
    ));

    const pane = () =>
      container.querySelector(".den-worker-transcript-pane") as HTMLDivElement | null;
    expect(pane()).toBeTruthy();
    Object.defineProperty(pane()!, "scrollHeight", { configurable: true, value: 2000 });
    Object.defineProperty(pane()!, "clientHeight", { configurable: true, value: 400 });
    await Promise.resolve();
    pane()!.scrollTop = 480;

    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "First line",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "a2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Second line",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "main.go" } }],
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);
    await Promise.resolve();
    expect(pane()?.scrollTop).toBe(480);

    setWorkers([
      {
        ...runningWorker(),
        tool_loops_used: 3,
        max_tool_loops: 40,
      },
    ]);
    await Promise.resolve();
    expect(pane()?.scrollTop).toBe(480);
    expect(pane()?.isConnected).toBe(true);
  });
});
