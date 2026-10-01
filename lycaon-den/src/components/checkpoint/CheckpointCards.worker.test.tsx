import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import type { NoticeReporter } from "../../notices/notice-store.ts";
import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { createAppStore } from "../../store/app-state.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { dockVisibleCheckpointId } from "../../chat/checkpoint/redirect-target.ts";
import { approvalReviewTarget, reviewPendingApproval } from "../../chat/checkpoint/approval-review.ts";
import { CheckpointCards } from "./CheckpointCards.tsx";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";

const parentSession = {
  id: "parent-1",
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "00000000-0000-4000-8000-000000000001",
  workspace_path: "/tmp/p",
  posture: "build" as const,
  status: "busy" as const,
  created_at: "t",
  activity_at: "t",
  updated_at: "t",
};

const testReporter: NoticeReporter = { reportError: () => {}, publish: () => {} };

function toolApproval(id: string, sessionId: string): PendingCheckpoint {
  return {
    checkpointId: id,
    sessionId,
    kind: "tool_approval",
    status: "pending",
    issuedAt: "t",
    tool_approval: toolApprovalFixture({
      tool: "request_tools",
      title: "Approve request_tools",
      command: "mkdir",
    }),
  };
}

function mockClient(): import("../../api/client.ts").LycaonClient {
  return stubClient({
    resolveCheckpoint: vi.fn(),
    getApprovalsSettings: vi.fn(),
    updateApprovalsSettings: vi.fn(),
  });
}

describe("CheckpointCards dock", () => {
  it("keeps a review request until the reopening pane becomes interactive", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch,
      [toolApproval("cp-reopening", "parent-1")]);
    const { getByTestId } = render(() => <div data-testid="retained-pane" inert>
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards appStore={appStore} client={mockClient()} sessionId="parent-1" projectDir="/tmp/p" projects={mockProjectsStore()} />
      </NoticeReporterProvider>
    </div>);
    const target = { sessionId: "parent-1", checkpointId: "cp-reopening" };
    reviewPendingApproval(target);
    await Promise.resolve();
    expect(document.activeElement).not.toBe(getByTestId("tool-approval-card"));
    expect(approvalReviewTarget()).toBe(target);
    getByTestId("retained-pane").removeAttribute("inert");
    await waitFor(() => expect(document.activeElement).toBe(getByTestId("tool-approval-card")));
    expect(approvalReviewTarget()).toBeNull();
  });
  it("reviews a checkpoint beyond the visible queue and restores it from its minimized strip", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch,
      ["cp-1", "cp-2", "cp-3", "cp-4", "cp-5"].map(id => toolApproval(id, "parent-1")));
    const { getByTestId } = render(() => <NoticeReporterProvider reporter={testReporter}>
      <CheckpointCards appStore={appStore} client={mockClient()} sessionId="parent-1" projectDir="/tmp/p" projects={mockProjectsStore()} />
    </NoticeReporterProvider>);
    reviewPendingApproval({ sessionId: "parent-1", checkpointId: "cp-5" });
    await waitFor(() => expect(document.activeElement).toBe(getByTestId("tool-approval-card")));
    expect(dockVisibleCheckpointId()).toBe("cp-5");
    expect(approvalReviewTarget()).toBeNull();
    getByTestId("approval-card-minimize").click();
    await Promise.resolve();
    expect(getByTestId("tool-approval-card").hasAttribute("data-minimized")).toBe(true);
    reviewPendingApproval({ sessionId: "parent-1", checkpointId: "cp-5" });
    await waitFor(() => expect(getByTestId("tool-approval-card").hasAttribute("data-minimized")).toBe(false));
    expect(document.activeElement).toBe(getByTestId("tool-approval-card"));
  });
  it("renders a worker child checkpoint in the parent session's dock", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [
      toolApproval("cp-child", "child-1"),
    ]);

    const { getByTestId } = render(() => (
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards
          appStore={appStore}
          client={mockClient()}
          sessionId="parent-1"
          projectDir="/tmp/p"
          projects={mockProjectsStore()}
        />
      </NoticeReporterProvider>
    ));

    expect(getByTestId("tool-approval-card")).toBeTruthy();
  });

  it("expands one card and lists the rest as queue rows", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [
      toolApproval("cp-1", "parent-1"),
      toolApproval("cp-2", "parent-1"),
      toolApproval("cp-3", "parent-1"),
      toolApproval("cp-4", "parent-1"),
    ]);

    const { getAllByTestId, queryByTestId } = render(() => (
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards
          appStore={appStore}
          client={mockClient()}
          sessionId="parent-1"
          projectDir="/tmp/p"
          projects={mockProjectsStore()}
        />
      </NoticeReporterProvider>
    ));

    // One interactive card (oldest first); the other three are compact rows.
    expect(getAllByTestId("tool-approval-card")).toHaveLength(1);
    const rows = getAllByTestId("checkpoint-queue-row");
    expect(rows).toHaveLength(3);
    expect(rows.map((row) => row.getAttribute("data-checkpoint-id"))).toEqual([
      "cp-2",
      "cp-3",
      "cp-4",
    ]);
    expect(queryByTestId("checkpoint-dock-more")).toBeNull();
  });

  it("collapses queue rows beyond the cap into a count line", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints(
      "parent-1",
      appStore.state.sessionViewEpoch,
      appStore.state.checkpointEventEpoch,
      ["cp-1", "cp-2", "cp-3", "cp-4", "cp-5"].map((id) =>
        toolApproval(id, "parent-1"),
      ),
    );

    const { getAllByTestId, getByTestId } = render(() => (
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards
          appStore={appStore}
          client={mockClient()}
          sessionId="parent-1"
          projectDir="/tmp/p"
          projects={mockProjectsStore()}
        />
      </NoticeReporterProvider>
    ));

    expect(getAllByTestId("checkpoint-queue-row")).toHaveLength(3);
    expect(getByTestId("checkpoint-dock-more").textContent).toBe(
      "1 more approval waiting",
    );
  });

  it("clicking a queue row expands that checkpoint and publishes it as the redirect target", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [
      toolApproval("cp-1", "parent-1"),
      toolApproval("cp-2", "parent-1"),
    ]);

    const controller = createTranscriptViewportController({
      sessionId: () => "parent-1",
    });
    const { getAllByTestId, getByTestId } = render(() => (
      <TranscriptViewportProvider value={controller}>
        <div class="den-composer-chrome-slot">
          <NoticeReporterProvider reporter={testReporter}>
            <CheckpointCards
              appStore={appStore}
              client={mockClient()}
              sessionId="parent-1"
              projectDir="/tmp/p"
              projects={mockProjectsStore()}
            />
          </NoticeReporterProvider>
        </div>
      </TranscriptViewportProvider>
    ));

    expect(dockVisibleCheckpointId()).toBe("cp-1");
    const firstDetails = getByTestId("tool-approval-card");

    getByTestId("checkpoint-queue-row").click();
    await Promise.resolve();

    expect(controller.following()).toBe(true);
    expect(dockVisibleCheckpointId()).toBe("cp-2");
    const card = getAllByTestId("tool-approval-card");
    expect(card).toHaveLength(1);
    expect(card[0]?.getAttribute("data-checkpoint-id")).toBe("cp-2");
    expect(getByTestId("tool-approval-card")).not.toBe(firstDetails);
    expect(
      getByTestId("checkpoint-queue-row").getAttribute("data-checkpoint-id"),
    ).toBe("cp-1");
  });

  it("minimizes the only card to its peek strip on show in chat", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [
      {
        checkpointId: "cp-secret",
        sessionId: "parent-1",
        kind: "tool_approval",
        status: "pending",
        issuedAt: "t",
        tool_approval: toolApprovalFixture({
          tool: "web_search",
          title: "Credential detected before sending this web search",
          subject: {
            kind: "secret",
            title: "Credential detected before sending this web search",
            targets: [{ kind: "secret", label: "GitHub Personal Access Token" }],
          },
          presentation: {
            action: "Send web search",
            gate: "secret_outbound",
            impact: "This action would expose a credential outside protected secret storage.",
            location: {
              origin: "in query",
              destination: "web search providers",
              origin_kind: "field",
              destination_kind: "service",
              reveal_tool_call_id: "call_search_1",
            },
          },
        }),
      },
    ]);

    const { getByTestId, queryByTestId } = render(() => (
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards
          appStore={appStore}
          client={mockClient()}
          sessionId="parent-1"
          projectDir="/tmp/p"
          projects={mockProjectsStore()}
        />
      </NoticeReporterProvider>
    ));

    expect(getByTestId("tool-approval-card")).toBeTruthy();
    getByTestId("approval-location-origin").click();
    await Promise.resolve();

    // The card stays docked; only its body retracts.
    const card = getByTestId("tool-approval-card");
    expect(card.hasAttribute("data-minimized")).toBe(true);
    expect(queryByTestId("checkpoint-queue-row")).toBeNull();
    const strip = getByTestId("approval-card-expand");
    expect(strip.textContent).toContain("GitHub Personal Access Token");
    expect(strip.textContent).toContain("in query");

    strip.click();
    await Promise.resolve();
    expect(card.hasAttribute("data-minimized")).toBe(false);
  });

  it("restores the minimized card when a new checkpoint arrives", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(parentSession);
    const secret: PendingCheckpoint = {
      checkpointId: "cp-secret",
      sessionId: "parent-1",
      kind: "tool_approval",
      status: "pending",
      issuedAt: "t",
      tool_approval: toolApprovalFixture({
        tool: "web_search",
        title: "Credential detected before sending this web search",
        subject: {
          kind: "secret",
          title: "Credential detected before sending this web search",
          targets: [{ kind: "secret", label: "GitHub Personal Access Token" }],
        },
        presentation: {
          action: "Send web search",
          gate: "secret_outbound",
          impact: "This action would expose a credential outside protected secret storage.",
          location: {
            origin: "in query",
            destination: "web search providers",
            origin_kind: "field",
            destination_kind: "service",
            reveal_tool_call_id: "call_search_1",
          },
        },
      }),
    };
    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [secret]);

    const { getByTestId, queryByTestId } = render(() => (
      <NoticeReporterProvider reporter={testReporter}>
        <CheckpointCards
          appStore={appStore}
          client={mockClient()}
          sessionId="parent-1"
          projectDir="/tmp/p"
          projects={mockProjectsStore()}
        />
      </NoticeReporterProvider>
    ));

    getByTestId("approval-location-origin").click();
    await Promise.resolve();
    expect(getByTestId("tool-approval-card").hasAttribute("data-minimized")).toBe(
      true,
    );
    expect(queryByTestId("approval-card-minimize")).toBeNull();

    appStore.actions.setPendingCheckpoints("parent-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [
      secret,
      toolApproval("cp-new", "parent-1"),
    ]);
    await Promise.resolve();
    expect(getByTestId("tool-approval-card").hasAttribute("data-minimized")).toBe(
      false,
    );
  });
});
