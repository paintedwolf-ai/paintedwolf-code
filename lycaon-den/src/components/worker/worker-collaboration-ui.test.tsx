import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal, For } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BoardView, Message, WorkerTask } from "../../api/types.ts";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { createTranscriptDisplayProjector } from "../../chat/transcript/projection/transcript-display-projection.ts";
import { sessionWorkerBranch } from "../../chat/worker/worker-branch-model.ts";
import { matchTaskPartsToWorkers } from "../../chat/worker/workers-model.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createShellWorkerDrawer } from "../shell/shell-worker-drawer.ts";
import { TranscriptItemRow } from "../transcript/TranscriptItemRow.tsx";
import { WorkerEvidenceSection } from "./WorkerEvidenceSection.tsx";
import { WorkersTab } from "./WorkersTab.tsx";

afterEach(() => {
  setLycaonClientForTest(null);
});

describe("persistent worker collaboration UI", () => {
  it("renders one canonical TaskCard across rounds, updates disposition/unread badges, and renders chronological notices", () => {
    const appStore = createAppStore();
    const round1: WorkerTask = {
      id: "job-r1",
      worker_record_id: "rec-auth",
      worker_name: "auth-impl",
      round: 1,
      child_session_id: "child-auth",
      parent_session_id: "sess-1",
      project_id: "proj-1",
      agent_type: "implementer",
      brief: "Implement session refresh rotation",
      status: "complete",
      result_handle: "00000000-0000-4000-8000-0000000000c1",
      result_disposition: "landed",
      result: { summary: "Implemented initial refresh token handler." },
      created_at: "2026-11-15T01:00:00Z",
    };
    const round2: WorkerTask = {
      id: "job-r2",
      worker_record_id: "rec-auth",
      worker_name: "auth-impl",
      round: 2,
      child_session_id: "child-auth",
      parent_session_id: "sess-1",
      project_id: "proj-1",
      agent_type: "implementer",
      status: "complete",
      result_handle: "00000000-0000-4000-8000-0000000000c2",
      result_disposition: "unaccounted",
      result: { summary: "Added clock-skew tolerance and replay check." },
      created_at: "2026-11-15T01:10:00Z",
    };
    const board: BoardView = {
      session_id: "sess-1",
      roster: [],
      workers: [
        {
          worker_id: "rec-auth",
          name: "auth-impl",
          agent_type: "implementer",
          state: "delivered",
          round: 2,
          result_handle: "00000000-0000-4000-8000-0000000000c2",
          result_disposition: "unaccounted",
          unread_message_count: 2,
        },
      ],
    };
    appStore.actions.setWorkers([round1, round2]);
    appStore.actions.setBoard(board);

    const messages: Message[] = [
      {
        id: "u1",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "Build refresh rotation",
        created_at: "2026-11-15T01:00:00Z",
      },
      {
        id: "a1",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [
          {
            id: "tc-spawn",
            name: "task",
            args: {
              name: "auth-impl",
              agent_type: "implementer",
              brief: { goal: "Implement session refresh rotation", done_when: ["Tests pass"] },
            },
          },
        ],
        created_at: "2026-11-15T01:00:01Z",
      },
      {
        id: "tr1",
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "<task />",
        tool_result: {
          content: "<task />",
          tool: "task",
          tool_call_id: "tc-spawn",
          assistant_message_id: "a1",
          dispatch: { worker_id: "job-r1", child_session_id: "child-auth", agent_type: "implementer" },
        },
        worker_summary: {
          worker_id: "job-r1",
          worker_record_id: "rec-auth",
          worker_name: "auth-impl",
          round: 1,
          child_session_id: "child-auth",
          agent_type: "implementer",
          status: "complete",
          envelope: "<task />",
        },
        created_at: "2026-11-15T01:02:00Z",
      },
      {
        id: "u2",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "Also guard clock skew.",
        created_at: "2026-11-15T01:09:00Z",
      },
      {
        id: "a2",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [
          {
            id: "tc-instruct",
            name: "instruct_worker",
            args: { worker: "auth-impl", instructions: "Add 30s clock-skew tolerance." },
          },
        ],
        created_at: "2026-11-15T01:09:05Z",
      },
      {
        id: "tr2",
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: JSON.stringify({
          worker: "auth-impl",
          outcome: "started",
          round: 2,
          instruction_seq: 3,
        }),
        tool_result: {
          content: JSON.stringify({
            worker: "auth-impl",
            outcome: "started",
            round: 2,
            instruction_seq: 3,
          }),
          tool: "instruct_worker",
          tool_call_id: "tc-instruct",
          assistant_message_id: "a2",
          dispatch: { worker_id: "job-r2", child_session_id: "child-auth", agent_type: "implementer" },
        },
        worker_summary: {
          worker_id: "job-r2",
          worker_record_id: "rec-auth",
          worker_name: "auth-impl",
          round: 2,
          child_session_id: "child-auth",
          agent_type: "implementer",
          status: "complete",
          envelope: "<task />",
        },
        created_at: "2026-11-15T01:10:00Z",
      },
    ];

    const collapsed = sessionWorkers(appStore, "sess-1");
    expect(collapsed).toHaveLength(1);
    expect(collapsed[0]?.id).toBe("job-r2");

    const getWorkerHistory = vi.fn();
    setLycaonClientForTest({ getWorkerHistory } as never);

    const items = createTranscriptDisplayProjector()(messages, undefined)[0]!;
    const toolParts = items.flatMap((item) =>
      item.kind === "worker_group"
        ? item.parts
        : item.kind === "tool"
          ? [item.part]
          : [],
    );
    const matches = matchTaskPartsToWorkers(toolParts, collapsed);
    const msgMap = new Map(messages.map((m) => [m.id, m]));
    const emptyReviews = new Map<string, string>();

    render(() => (
      <div>
        <For each={items}>
          {(entry) => (
            <TranscriptItemRow
              item={() => entry}
              taskMatches={() => matches}
              workers={collapsed}
              messages={messages}
              messageById={() => msgMap}
              reviewTurns={() => emptyReviews}
              layout="chat"
              visibleTurnActive={false}
              sessionId="sess-1"
              checkpointAppStore={appStore}
              projectId="proj-1"
              onOpenWorker={vi.fn()}
            />
          )}
        </For>
        <WorkersTab
          workers={collapsed}
          board={board}
          selectedId={null}
          onSelect={vi.fn()}
          onCancel={vi.fn()}
        />
      </div>
    ));

    expect(screen.getAllByTestId("task-card")).toHaveLength(1);
    expect(screen.getAllByTestId("worker-collaboration-notice")).toHaveLength(1);
    expect(screen.getByTestId("worker-round-badge").textContent).toBe("Round 2");
    expect(screen.getAllByTestId("worker-disposition-badge")[0]?.textContent).toBe(
      "Not accounted for",
    );
    expect(screen.getAllByTestId("worker-unread-badge")[0]?.textContent).toBe(
      "2 unread messages",
    );
    expect(screen.getByTestId("task-card-status").textContent).toBe("Delivered");
    expect(getWorkerHistory).not.toHaveBeenCalled();
  });

  it("restores chat scroll offset and originating button focus when closing worker detail", async () => {
    const appStore = createAppStore();
    appStore.actions.setWorkers([
      {
        id: "job-r2",
        worker_record_id: "rec-1",
        worker_name: "auth-impl",
        round: 2,
        parent_session_id: "sess-1",
        project_id: "proj-1",
        agent_type: "implementer",
        status: "complete",
        created_at: "2026-11-15T01:00:00Z",
      },
    ]);
    const [activeChat] = createSignal({ projectId: "proj-1", sessionId: "sess-1" });
    const drawer = createShellWorkerDrawer({
      appStore,
      recents: { state: { recents: [] } } as never,
      activeChat,
      revealConversation: async () => {},
      resumeSession: async () => {},
      navigateFromSearch: async () => {},
      revealFromSearch: () => {},
    });

    const view = render(() => (
      <div class="den-chat-stream">
        <button
          type="button"
          data-testid="origin-notice"
          onClick={() =>
            drawer.openWorkers("rec-1", { scrollTo: "history", messageSeq: 4, roundOrdinal: 2 })
          }
        >
          Sent instruction to auth-impl · Round 2
        </button>
      </div>
    ));
    const stream = view.container.querySelector(".den-chat-stream") as HTMLDivElement;
    stream.scrollTop = 640;
    const noticeBtn = screen.getByTestId("origin-notice");
    noticeBtn.focus();

    fireEvent.click(noticeBtn);
    expect(drawer.workersOpen()).toBe(true);
    expect(drawer.selectedWorkerId()).toBe("job-r2");
    expect(drawer.workerDrawerFocus()).toEqual({
      section: "history",
      messageSeq: 4,
      roundOrdinal: 2,
    });

    stream.scrollTop = 0;
    noticeBtn.blur();
    drawer.closeWorkers();
    await waitFor(() => {
      expect(drawer.workersOpen()).toBe(false);
      expect(stream.scrollTop).toBe(640);
      expect(document.activeElement).toBe(noticeBtn);
    });
  });

  it("renders sealed reviewer subjects and closes branch overlays across landed, rejected, and shelved dispositions", () => {
    const reviewer: WorkerTask = {
      id: "job-rev",
      worker_record_id: "rec-rev",
      worker_name: "reviewer",
      round: 1,
      review_of: "00000000-0000-4000-8000-0000000000d9",
      agent_type: "reviewer",
      status: "complete",
      created_at: "2026-11-15T01:00:00Z",
    };
    render(() => <WorkerEvidenceSection worker={reviewer} onSelect={vi.fn()} />);
    expect(screen.getByTestId("worker-reviewed-subject").textContent).toContain(
      "00000000-0000-4000-8000-0000000000d9",
    );

    const openOverlayWorker: WorkerTask = {
      id: "job-impl",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "complete",
      merge_status: "ready",
      touched_paths: ["src/auth.ts"],
      result_disposition: "open",
      created_at: "2026-11-15T01:00:00Z",
    };
    expect(sessionWorkerBranch([openOverlayWorker], "sess-1")?.state).toBe("open");
    expect(
      sessionWorkerBranch([{ ...openOverlayWorker, result_disposition: "shelved" }], "sess-1"),
    ).toBeNull();
    expect(
      sessionWorkerBranch([{ ...openOverlayWorker, result_disposition: "landed" }], "sess-1"),
    ).toBeNull();
    expect(
      sessionWorkerBranch([{ ...openOverlayWorker, result_disposition: "rejected" }], "sess-1"),
    ).toBeNull();
  });
});
