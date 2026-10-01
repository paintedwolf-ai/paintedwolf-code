import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { JSX } from "solid-js";
import { WorklogPanel } from "./WorklogPanel.tsx";
import { loadFailed, loaded } from "../../store/load-state.ts";
import { ContextDrawerHost } from "../shell/ContextDrawer.tsx";
import type { BoardView, Message } from "../../api/types.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";

const findings = {
  findings: [
    { agent: "implementer", summary: "Score class lives in score.py", ref: "score.py" },
  ],
  revision: 1,
};

const board: BoardView = {
  summary: "ok",
  repo: { languages: [], file_count: 0, generated_at: "2026-01-01T00:00:00Z" },
  roster: [
    {
      worker_id: "job-a",
      agent_type: "implementer",
      status: "running",
      reservations: ["shellsim/builtins.py"],
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
};

const progressMessages: Message[] = [
  {
    id: "pu-1",
    role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
    content: "",
    kind: "progress_update",
    ord: 1,
    created_at: "2026-01-01T00:00:01Z",
    progress_update: {
      seq: 1,
      initial: true,
      steps: [
        { state: "pending", label: "Wire login form" },
        { state: "pending", label: "Add tests" },
      ],
    },
  },
  {
    id: "pu-2",
    role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
    content: "",
    kind: "progress_update",
    ord: 2,
    created_at: "2026-01-01T00:00:02Z",
    progress_update: {
      seq: 2,
      initial: false,
      changes: [
        { kind: "done", label: "Wire login form", state: "done" },
        {
          kind: "updated",
          label: "Verify build",
          prev_label: "Add tests",
          state: "pending",
        },
      ],
    },
  },
  {
    id: "pc-1",
    role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
    content: "",
    kind: "progress_complete",
    ord: 3,
    created_at: "2026-01-01T00:00:03Z",
    progress_complete: {
      seq: 1,
      steps: [
        { state: "done", label: "Wire login form" },
        { state: "done", label: "Verify build" },
      ],
    },
  },
];

function renderInChatStage(children: () => JSX.Element) {
  return render(() => (
    <ContextDrawerHost>
      <div class="den-shell-stage--chat den-shell-stage">
        <header>Chat tabs</header>
        <main>{children()}</main>
      </div>
    </ContextDrawerHost>
  ));
}

describe("WorklogPanel", () => {

  it("keeps exact finding detail in a collapsed disclosure", () => {
    const body = "type Scene = {\n  text: string;\n}";
    renderInChatStage(() => <WorklogPanel open findings={loaded({ findings: [{ id: 1, agent: "implementer", summary: "Scene interface", ref: "scene.ts", body, has_body: true }], revision: 1 })} onClose={() => undefined} />);
    const detail = screen.getByText("Details").closest("details");
    expect(detail).toBeTruthy();
    expect(detail?.open).toBe(false);
    expect(detail?.querySelector("p")?.textContent).toBe(body);
  });
  it("renders findings and reservations", () => {
    renderInChatStage(() => (
      <WorklogPanel
        open
        findings={loaded(findings)}
        board={board}
        onClose={() => undefined}
      />
    ));
    expect(screen.getByTestId("worklog-panel")).toBeTruthy();
    expect(screen.getByTestId("worklog-finding-row")).toBeTruthy();
    expect(screen.getByTestId("worklog-reservation-path")).toBeTruthy();
    expect(screen.getByText("Score class lives in score.py")).toBeTruthy();
    expect(screen.getByText("shellsim/builtins.py")).toBeTruthy();
  });

  it("renders a chronological progress change log", () => {
    renderInChatStage(() => (
      <WorklogPanel
        open
        findings={loaded(findings)}
        messages={progressMessages}
        onClose={() => undefined}
      />
    ));
    const rows = screen.getAllByTestId("worklog-progress-row");
    expect(rows).toHaveLength(6);
    expect(rows[0]?.textContent).toContain("Created");
    expect(rows[0]?.textContent).toContain("Wire login form");
    expect(rows[2]?.textContent).toContain("Done");
    expect(rows[3]?.textContent).toContain("Add tests → Verify build");
    expect(rows[4]?.textContent).toContain("Completed");
    expect(rows[5]?.textContent).toContain("Completed");
    expect(rows[5]?.textContent).toContain("Verify build");
  });

  it("shows empty states for findings, reservations, and progress", () => {
    renderInChatStage(() => (
      <WorklogPanel
        open
        findings={loaded({ findings: [], revision: 0 })}
        onClose={() => undefined}
      />
    ));
    expect(screen.getByText("No progress changes yet.")).toBeTruthy();
    expect(screen.getByText("No worker findings yet.")).toBeTruthy();
    expect(screen.getByText("No path reservations yet.")).toBeTruthy();
    expect(screen.getByText("No workers need approval.")).toBeTruthy();
  });

  it("links a worker approval entry to the child checkpoint", () => {
    const onOpen = vi.fn();
    renderInChatStage(() => (
      <WorklogPanel
        open
        findings={loaded(findings)}
        board={board}
        workers={[
          {
            id: "job-a",
            child_session_id: "child-a",
            parent_session_id: "parent-a",
            agent_type: "implementer",
            status: "running",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
        pendingCheckpoints={[
          {
            checkpointId: "checkpoint-a",
            sessionId: "child-a",
            kind: "tool_approval",
            status: "pending",
            issuedAt: "2026-01-01T00:00:01Z",
            tool_approval: toolApprovalFixture({ command: "git push origin main" }),
          },
        ]}
        onOpenWorkerCheckpoint={onOpen}
        onClose={() => undefined}
      />
    ));
    const row = screen.getByTestId("worklog-worker-approval");
    expect(row.textContent).toContain("Worker needs approval");
    expect(row.textContent).toContain("git push origin main");
    fireEvent.click(screen.getByRole("button", { name: /Worker needs approval/i }));
    expect(onOpen).toHaveBeenCalledWith("job-a", "checkpoint-a");
  });

  it("is not mounted when closed", () => {
    renderInChatStage(() => (
      <WorklogPanel
        open={false}
        findings={loaded(findings)}
        onClose={() => undefined}
      />
    ));
    expect(screen.queryByTestId("worklog-panel")).toBeNull();
  });

  it("uses contextual drawer chrome without a backdrop", () => {
    renderInChatStage(() => (
      <WorklogPanel open findings={loaded(findings)} onClose={() => undefined} />
    ));
    const panel = screen.getByTestId("worklog-panel");
    expect(panel.classList.contains("den-context-drawer")).toBe(true);
    expect(screen.queryByTestId("worklog-panel-backdrop")).toBeNull();
    expect(panel.querySelector(".den-context-drawer-scroll")).toBeTruthy();
    expect(panel.querySelector(".den-worker-transcript-pane")).toBeTruthy();
    expect(
      panel.querySelectorAll(".den-worker-transcript-section-summary"),
    ).toHaveLength(4);
  });

  it("stays open on outside pointerdown until its close control is used", () => {
    const onClose = vi.fn();
    renderInChatStage(() => (
      <WorklogPanel open findings={loaded(findings)} onClose={onClose} />
    ));
    fireEvent.pointerDown(document.body);
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByTestId("worklog-panel")).toBeTruthy();
  });

  it("a failed findings read never renders as \"No worker findings yet.\"", () => {
    renderInChatStage(() => (
      <WorklogPanel
        open
        findings={loadFailed(new Error("sidecar restarting"))}
        onClose={() => undefined}
      />
    ));
    expect(screen.queryByText("No worker findings yet.")).toBeNull();
    expect(screen.getByTestId("worklog-findings-load-error")).toBeTruthy();
  });
});
