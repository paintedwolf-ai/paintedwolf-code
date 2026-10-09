import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { sendDraft, typeDraft } from "../../test/composer-view-fixture.tsx";
import { ChatView } from "./ChatView.tsx";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";
import { chatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { CHAT_ATTACHMENT_DRAG_TYPE } from "../../chat/composer/chat-attachment-drag.ts";
import { registerComposerAttachmentSink } from "../../chat/composer/add-to-chat.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { stubClient, type ClientStubs } from "../../test/client-fixture.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { noticeActions, resetNoticeActionSinksForTest } from "../../notices/notice-actions.ts";
import { applyTurnOutcome, resetTurnOutcomesForTests } from "../../chat/recovery/turn-outcome.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import type { Message, PendingFeedback, Session, WorkflowRun, WorkflowSummary } from "../../api/types.ts";

const runRewind = vi.hoisted(() => vi.fn(async () => ({})));
vi.mock("../../chat/recovery/session-recovery.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../chat/recovery/session-recovery.ts")>()),
  runRewind,
}));
const alignRevealTarget = vi.hoisted(() => vi.fn());
vi.mock("../../chat/transcript/presentation/transcript-reveal.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../chat/transcript/presentation/transcript-reveal.ts")>()),
  alignRevealTarget,
}));

const SESSION = "sess-1";
const PROJECT = "proj-1";
const SESSION_SCOPE = { kind: "session", projectId: PROJECT, sessionId: SESSION } as const;

function message(id: string, role: "user" | "assistant", content: string, ord: number, runId?: string): Message {
  return {
    ...(runId ? { workflow_run_id: runId } : {}),
    id,
    role,
    origin: role === "user" ? "user" : "model",
    authority: role === "user" ? "user" : "none",
    trust_tier: "trusted",
    content,
    ord,
    seq: ord,
    created_at: "2026-01-01T00:00:00Z",
  };
}

const planWorkflow: WorkflowSummary = {
  id: "plan",
  version: "1.0.0",
  name: "Plan",
  icon: "route",
  featured: true,
  trigger: "/plan",
};

function workflowRun(pendingFeedback?: PendingFeedback): WorkflowRun {
  return {
    id: "run-1",
    session_id: SESSION,
    project_id: PROJECT,
    workflow_id: "plan",
    workflow_version: "1.0.0",
    revision: 3,
    status: "running",
    current_phase: "work",
    start_message_id: "ask-1",
    created_at: "t",
    updated_at: "t",
    ui: { current_phase_label: "Working", ...(pendingFeedback ? { pending_feedback: pendingFeedback } : {}) },
  };
}

function mountChat(options: { client?: ClientStubs; session?: Partial<Session> } = {}) {
  setLycaonClientForTest(stubClient(options.client ?? {}));
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  appStore.actions.setCurrentSession({
    id: SESSION,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: PROJECT,
    workspace_path: "/tmp/p",
    posture: "build",
    status: "idle",
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
    ...options.session,
  });
  const onSend = vi.fn(async () => true);
  const [surfaceActive, setSurfaceActive] = createSignal(true);
  render(() => (
    <ChatTabChromeProvider sessionKey={SESSION}>
      <ChatView
        appStore={appStore}
        recents={createRecentsStore()}
        projects={mockProjectsStore([wireProject("/tmp/p", PROJECT)])}
        projectId={PROJECT}
        projectDir="/tmp/p"
        sessionId={SESSION}
        hasInitialPrompt={false}
        surfaceActive={surfaceActive()}
        onOpenWorker={() => {}}
        onOpenFiles={() => {}}
        onSend={onSend}
        onStop={() => {}}
      />
    </ChatTabChromeProvider>
  ));
  const composer = () => screen.getByTestId("chat-composer") as HTMLTextAreaElement;
  const setWorkflows = (input: { catalog?: WorkflowSummary[]; active?: WorkflowRun }) =>
    appStore.actions.setWorkflowState(SESSION, appStore.state.sessionViewEpoch, {
      activeWorkflowRun: input.active,
      workflowRuns: input.active ? [input.active] : [],
      workflowCatalog: input.catalog ?? [],
      blueprints: [],
    });
  return { appStore, onSend, composer, setSurfaceActive, setWorkflows };
}

function seedTranscript(appStore: ReturnType<typeof createAppStore>, options: { reply?: boolean; runId?: string } = {}) {
  const ask = message("ask-1", "user", "Summarize the release notes", 1, options.runId);
  const rows = options.reply === false ? [ask] : [ask, message("reply-1", "assistant", "Working on it", 2, options.runId)];
  appStore.actions.installTranscriptBaseline(SESSION, rows, rows.length);
}

function dragData(types: string[], extra: Record<string, unknown> = {}) {
  return { dataTransfer: { types, ...extra } };
}

beforeEach(() => {
  runRewind.mockClear();
  alignRevealTarget.mockClear();
  resetTurnOutcomesForTests();
  resetNoticeActionSinksForTest();
});

afterEach(() => {
  setLycaonClientForTest(null);
});

describe("ChatView composer", () => {
  it("sends a typed prompt to the session", async () => {
    const { onSend, composer } = mountChat();
    await sendDraft(composer(), "Explain the build");
    expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "Explain the build" }));
  });

  it("arms a workflow from its slash command and starts it on the next send", async () => {
    const { onSend, composer, setWorkflows } = mountChat();
    setWorkflows({ catalog: [planWorkflow] });
    // Enter would accept the slash suggestion; the send button submits the line as typed.
    typeDraft(composer(), "/plan");
    fireEvent.click(screen.getByTestId("composer-send"));
    await waitFor(() => expect(screen.getByTestId("workflow-arm-chip").textContent).toContain("Plan"));
    expect(onSend).not.toHaveBeenCalled();

    await sendDraft(composer(), "Ship the release");
    expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "/plan Ship the release" }));
    expect(screen.queryByTestId("workflow-arm-chip")).toBeNull();
  });

  it("arms a workflow from the launcher and clears it from its chip", () => {
    const { setWorkflows } = mountChat();
    setWorkflows({ catalog: [planWorkflow] });
    fireEvent.click(screen.getByTestId("session-launcher-tile-plan"));
    expect(screen.getByTestId("workflow-arm-chip")).toBeTruthy();
    fireEvent.click(screen.getByTestId("workflow-arm-chip-clear"));
    expect(screen.queryByTestId("workflow-arm-chip")).toBeNull();
  });

  it("turns composer text into direction that rejects the pending tool approval", async () => {
    const resolveCheckpoint = vi.fn(async () => undefined);
    const { appStore, onSend, composer } = mountChat({ client: { resolveCheckpoint } });
    appStore.actions.setPendingCheckpoints(SESSION, appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [{
      checkpointId: "cp-1",
      sessionId: SESSION,
      kind: "tool_approval",
      status: "pending",
      issuedAt: "t",
      tool_approval: toolApprovalFixture({ tool: "request_tools", title: "Approve request_tools", command: "mkdir" }),
    }]);
    await sendDraft(composer(), "Use the scratch directory instead");
    expect(resolveCheckpoint).toHaveBeenCalledWith(SESSION, "cp-1", expect.objectContaining({
      action: "reject",
      guidance: "Use the scratch directory instead",
    }));
    expect(onSend).not.toHaveBeenCalled();
  });

  it("answers a pending workflow question from the composer", async () => {
    const resolveWorkflowFeedback = vi.fn(async () => ({}));
    const { onSend, composer, setWorkflows } = mountChat({ client: { resolveWorkflowFeedback } });
    setWorkflows({ active: workflowRun({ phase_id: "ask-phase", prompt: "Which branch?" }) });
    expect((await screen.findByTestId("workflow-feedback-prompt")).textContent).toContain("Which branch?");

    await sendDraft(composer(), "main");
    expect(resolveWorkflowFeedback).toHaveBeenCalledWith("run-1", "ask-phase", expect.anything());
    expect(onSend).not.toHaveBeenCalled();
  });

  it("presents the session queue and sends it on request", async () => {
    const updateSessionQueue = vi.fn(async () => ({ queue_items: [], hold: false, sending: true, revision: 2 }));
    const { appStore } = mountChat({ client: { updateSessionQueue } });
    appStore.actions.setQueueDraft(SESSION, appStore.state.sessionViewEpoch, {
      queue_items: [{ id: "q-1", text: "Then run the tests", submitted_by: "00000000-0000-4000-8000-000000000002", created_at: "t" }],
      hold: false,
      sending: false,
      revision: 1,
    });
    expect(screen.getByTestId("queue-popover-card").textContent).toContain("Then run the tests");
    fireEvent.click(screen.getByTestId("queue-send"));
    await waitFor(() => expect(updateSessionQueue).toHaveBeenCalledWith(SESSION, expect.objectContaining({ op: "send" })));
  });
});

describe("ChatView transcript", () => {
  it("offers retry when an interrupted turn made no progress", async () => {
    const { appStore, onSend } = mountChat();
    seedTranscript(appStore, { reply: false });
    applyTurnOutcome({ id: SESSION, status: "idle", idle_disposition: "interrupted" });

    fireEvent.click(await screen.findByTestId("turn-interrupted-retry"));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith(expect.objectContaining({
      text: "Summarize the release notes",
      recovery: { action: "retry", after_message_id: "ask-1" },
    })));
  });

  it("offers to continue or rewind an interrupted turn that made progress", async () => {
    const previewSessionRewind = vi.fn(async () => ({ plan_digest: "digest-1", files: [], issues: [], truncated_message_count: 1 }));
    const { appStore, onSend } = mountChat({ client: { previewSessionRewind } });
    seedTranscript(appStore);
    applyTurnOutcome({ id: SESSION, status: "idle", idle_disposition: "interrupted" });

    fireEvent.click(await screen.findByTestId("turn-interrupted-keep-going"));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith(expect.objectContaining({
      text: "Keep going",
      recovery: { action: "continue", after_message_id: "reply-1" },
    })));
    fireEvent.click(screen.getByTestId("turn-interrupted-rewind-and-retry"));
    await waitFor(() => expect(runRewind).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: SESSION }),
      expect.objectContaining({ action: "rewind", messageId: "ask-1" }),
      "digest-1",
    ));
    await waitFor(() => expect(onSend).toHaveBeenLastCalledWith(expect.objectContaining({ text: "Summarize the release notes" })));
  });

  it("offers a proposed workflow start until it is dismissed", () => {
    mountChat({ session: { ui: { pending_workflow_start: { workflow_id: "plan", workflow_version: "1.0.0", label: "Plan" } } } });
    expect(screen.getByTestId("workflow-start-proposal-card")).toBeTruthy();
    fireEvent.click(screen.getByTestId("workflow-start-proposal-dismiss"));
    expect(screen.queryByTestId("workflow-start-proposal-card")).toBeNull();
  });

  it("aligns a workflow run's opening message when the rail jumps to it", async () => {
    const { appStore, setWorkflows } = mountChat();
    seedTranscript(appStore, { runId: "run-1" });
    setWorkflows({ active: workflowRun() });
    await waitFor(() => expect(document.querySelector("#msg-ask-1")).toBeTruthy());
    chatTabRailBindings()?.workflows.onJumpToRun(workflowRun());
    await waitFor(() => expect(alignRevealTarget).toHaveBeenCalledWith(document.querySelector("#msg-ask-1")));
  });

  it("hands focus entering the chat to the composer", async () => {
    const { composer } = mountChat();
    await waitFor(() => expect(focusRegion("chat")).toBe(true));
    expect(document.activeElement).toBe(composer());
  });
});

describe("ChatView recovery", () => {
  it("serves prompt notice actions for this chat", async () => {
    const { appStore, onSend } = mountChat();
    seedTranscript(appStore);
    const [retry] = noticeActions({ actions: ["prompt_retry"], scope: SESSION_SCOPE });
    retry?.run();
    await waitFor(() => expect(onSend).toHaveBeenCalledWith(expect.objectContaining({
      text: "Summarize the release notes",
      recovery: { action: "retry", after_message_id: "reply-1" },
    })));
  });

  it("asks for confirmation when rewind and retry cannot rewind silently, then rewinds", async () => {
    const previewSessionRewind = vi.fn()
      .mockResolvedValueOnce({ plan_digest: "", files: [], issues: [{ root_id: "r", path: "a.txt", code: "modified_after" }], truncated_message_count: 1 })
      .mockResolvedValue({ plan_digest: "digest-1", files: [], issues: [], truncated_message_count: 1 });
    const { appStore } = mountChat({ client: { previewSessionRewind } });
    seedTranscript(appStore);
    const [rewind] = noticeActions({ actions: ["prompt_rewind_and_retry"], scope: SESSION_SCOPE });
    rewind?.run();

    expect(await screen.findByTestId("session-recover-dialog")).toBeTruthy();
    const confirm = screen.getByTestId("session-recover-confirm") as HTMLButtonElement;
    await waitFor(() => expect(confirm.disabled).toBe(false));
    fireEvent.click(confirm);
    await waitFor(() => expect(screen.queryByTestId("session-recover-dialog")).toBeNull());
    expect(runRewind).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: SESSION, projectId: PROJECT }),
      expect.objectContaining({ action: "rewind", messageId: "ask-1" }),
      "digest-1",
    );
  });

  it("keeps the dialog open with the host's reason when a rewind fails", async () => {
    const previewSessionRewind = vi.fn()
      .mockResolvedValueOnce({ plan_digest: "", files: [], issues: [{ root_id: "r", path: "a.txt", code: "modified_after" }], truncated_message_count: 1 })
      .mockResolvedValue({ plan_digest: "digest-1", files: [], issues: [], truncated_message_count: 1 });
    runRewind.mockRejectedValueOnce(new Error("The session moved on."));
    const { appStore } = mountChat({ client: { previewSessionRewind } });
    seedTranscript(appStore);
    noticeActions({ actions: ["prompt_rewind_and_retry"], scope: SESSION_SCOPE })[0]?.run();

    const confirm = await screen.findByTestId("session-recover-confirm") as HTMLButtonElement;
    await waitFor(() => expect(confirm.disabled).toBe(false));
    fireEvent.click(confirm);
    expect((await screen.findByTestId("session-recover-error")).textContent).toBe("The session moved on.");
    fireEvent.click(screen.getByTestId("session-recover-cancel"));
    expect(screen.queryByTestId("session-recover-dialog")).toBeNull();
  });
});

describe("ChatView surface", () => {
  it("publishes rail bindings only while the surface is active", () => {
    const cancelWorker = vi.fn(async () => undefined);
    const { setSurfaceActive } = mountChat({ client: { cancelWorker } });
    const bindings = chatTabRailBindings();
    expect(bindings).not.toBeNull();
    bindings?.onCancelWorker("worker-1");
    expect(cancelWorker).toHaveBeenCalledWith("worker-1");
    expect(bindings?.cancellingWorkerId()).toBe("worker-1");
    setSurfaceActive(false);
    expect(chatTabRailBindings()).toBeNull();
  });

  it("attaches dropped files and in-app references to the composer", async () => {
    const uploadAttachment = vi.fn(async (_projectId: string, filename: string) => ({
      blob_id: "a".repeat(64), filename, mime: "text/plain", kind: "text", bytes: 5,
    }));
    mountChat({ client: { uploadAttachment } });
    // The shell owns the composer attachment sink for the visible chat.
    onTestFinished(registerComposerAttachmentSink({
      sessionId: () => SESSION,
      blockReason: () => null,
      projectId: () => PROJECT,
      projectRoots: () => [{ id: `${PROJECT}-root`, path: "/tmp/p" }],
      reveal: () => {},
    }));
    const chat = screen.getByTestId("chat-view");
    fireEvent.dragEnter(chat, dragData(["Files"]));
    expect(screen.getByTestId("chat-drop-overlay").textContent).toContain("Drop to add to chat");
    fireEvent.drop(chat, dragData(["Files"], { files: [new File(["hello"], "notes.txt", { type: "text/plain" })] }));
    expect(screen.queryByTestId("chat-drop-overlay")).toBeNull();
    await waitFor(() => expect(screen.getAllByTestId("composer-attachment-chip")).toHaveLength(1));

    const ref = { kind: "path-file", projectId: PROJECT, rootId: `${PROJECT}-root`, path: "src/main.ts", name: "main.ts" };
    const attachment = dragData([CHAT_ATTACHMENT_DRAG_TYPE], { getData: () => JSON.stringify(ref) });
    fireEvent.dragEnter(chat, attachment);
    expect(screen.getByTestId("chat-drop-overlay")).toBeTruthy();
    fireEvent.drop(chat, attachment);
    await waitFor(() => expect(screen.getAllByTestId("composer-attachment-chip")).toHaveLength(2));
    expect(uploadAttachment).toHaveBeenCalledTimes(1);
  });

  it("explains and refuses drops while the host is unreachable", () => {
    const uploadAttachment = vi.fn();
    const { appStore } = mountChat({ client: { uploadAttachment } });
    appStore.actions.setSidecarStatus("disconnected");
    const chat = screen.getByTestId("chat-view");
    fireEvent.dragEnter(chat, dragData(["Files"]));
    const reason = screen.getByTestId("chat-drop-overlay").textContent ?? "";
    expect(reason).not.toContain("Drop to add to chat");
    expect(reason.trim()).not.toBe("");
    fireEvent.drop(chat, dragData(["Files"], { files: [new File(["hello"], "notes.txt")] }));
    expect(screen.queryByTestId("chat-drop-overlay")).toBeNull();
    expect(uploadAttachment).not.toHaveBeenCalled();
  });
});
