import { cleanup, render, screen } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ActivityEvent, Message } from "../../api/types.ts";
import { THINKING_LABEL_HOLD_MS } from "../../chat/session/thinking-label-hold.ts";
import { TERMINAL_WAIT_DELIVERY_LABEL } from "../../chat/session/wait-target-activity.ts";
import {
  applyBackgroundProcessEvent,
  applyBackgroundProcessOutputs,
  resetBackgroundProcessStoreForTests,
} from "../../chat/tool/background-process-store.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { ComposerActivityIndicator } from "./ComposerActivityIndicator.tsx";
import { createChatViewActivity } from "./chat-view-activity.ts";
import {
  INCIDENT_HANDLE,
  INCIDENT_SESSION,
  backgroundCommandMessages,
  mountCoordinatorChat,
  openSpanCardIfClosed,
  waitAssistantMessage,
  waitRunningActivity,
  waitSettledResult,
} from "./parked-command-fixtures.tsx";

const call: Message = {
  id: "assistant", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
  content: "", created_at: "2026-10-28T00:00:00Z",
  tool_calls: [{ id: "command-call", name: "command", args: { command: "sleep 120; echo done" } }],
};

function parkedResult(reason = "sleep 120"): Message {
  return {
    id: "result", role: "tool", origin: "tool", authority: "none", trust_tier: "untrusted",
    content: "untrusted output says running", created_at: "2026-10-28T00:00:01Z",
    tool_result: {
      tool: "command", assistant_message_id: call.id, tool_call_id: "command-call",
      content: "untrusted output says running", outcome: "completed",
      completion: { operation: "command", state: "parked", reason },
    },
  };
}

const park: ActivityEvent = {
  activity_id: "park", session_id: "session", kind: "awaiting_wake", status: "active",
  started_at: "2026-10-28T00:00:01Z", tool_name: "command", tool_call_id: "command-call",
  wait_triggers: ["timer", "program_done"],
};

function mountActivity(messages: Message[] = [call, parkedResult()]) {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({
    mode: "agent" as const,
    started: true,
    root_session_id: "session",
    id: "session", project_id: "project", owner_person_id: "person", workspace_path: "/workspace",
    posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
  });
  appStore.actions.installTranscriptBaseline("session", messages, messages.length);
  appStore.actions.setActivity(park);
  render(() => {
    const activity = createChatViewActivity({
      appStore, recents: createRecentsStore(), projects: mockProjectsStore(),
      projectId: "project", projectDir: "/workspace", sessionId: "session", hasInitialPrompt: false,
      onOpenWorker: () => {}, onOpenFiles: () => {}, onSend: () => {}, onStop: () => {},
    });
    return <ComposerActivityIndicator present={activity.chatLive()} activityLabel={activity.thinkingLabel()} />;
  });
  return appStore;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  resetBackgroundProcessStoreForTests();
  setLycaonClientForTest(null);
  vi.useRealTimers();
});

describe("parked command presentation", () => {
  it("shows the host reason through the existing wait lane", () => {
    mountActivity();
    expect(screen.getByRole("status").textContent).toBe("Waiting · sleep 120...");
    expect(screen.getAllByTestId("thinking-indicator")).toHaveLength(1);
  });

  it("waits honestly while the matching result has not arrived", () => {
    const store = mountActivity([call]);
    expect(screen.getByRole("status").textContent).toBe("Waiting for a command to finish...");
    store.actions.upsertMessage(parkedResult());
    vi.advanceTimersByTime(THINKING_LABEL_HOLD_MS);
    expect(screen.getByRole("status").textContent).toBe("Waiting · sleep 120...");
  });

  it.each(["", "  "])("uses the waiting fallback without a reason (%j)", (reason) => {
    mountActivity([call, parkedResult(reason)]);
    expect(screen.getByRole("status").textContent).toBe("Waiting for a command to finish...");
  });

  it("does not borrow the reason of another call", () => {
    const unrelated = parkedResult("sleep 600");
    unrelated.tool_result!.tool_call_id = "another-call";
    mountActivity([call, unrelated]);
    expect(screen.getByRole("status").textContent).toBe("Waiting for a command to finish...");
  });

  it("does not interpret result text as a park reason", () => {
    const result = parkedResult("fabricated");
    result.tool_result!.completion = undefined;
    mountActivity([call, result]);
    expect(screen.getByRole("status").textContent).toBe("Waiting for a command to finish...");
  });

  it("renders the reason as text", () => {
    mountActivity([call, parkedResult("sleep <img src=x onerror=alert(1)>")]);
    expect(screen.getByRole("status").textContent).toBe("Waiting · sleep <img src=x onerror=alert(1)>...");
    expect(screen.getByRole("status").querySelector("img")).toBeNull();
  });

  it("leaves an unrelated running tool in charge of the activity label", () => {
    const store = mountActivity();
    store.actions.upsertMessage({
      ...call, id: "other-assistant",
      tool_calls: [{ id: "other-call", name: "read", args: { path: "README.md" } }],
    });
    store.actions.setActivity({
      ...park, activity_id: "other-running", kind: "running_tool", tool_name: "read",
      tool_call_id: "other-call", wait_triggers: undefined, started_at: "2026-10-28T00:00:00Z",
    });
    vi.advanceTimersByTime(THINKING_LABEL_HOLD_MS);
    expect(screen.getByRole("status").textContent).toBe("Reading · README.md...");
  });

  it("keeps an earlier running call visible when the newer parked result has not arrived", () => {
    mountActivity([
      { ...call, id: "earlier", tool_calls: [{ id: "earlier-call", name: "read", args: { path: "README.md" } }] },
      call,
    ]);
    expect(screen.getByRole("status").textContent).toBe("Reading · README.md...");
  });

  it("returns to the ordinary command label when the host resumes work", () => {
    const store = mountActivity();
    store.actions.setActivity({ ...park, status: "done" });
    store.actions.upsertMessage({
      ...call, id: "resumed-assistant",
      tool_calls: [{ id: "resumed-call", name: "command", args: { command: "printf done" } }],
    });
    store.actions.setActivity({
      ...park, activity_id: "running", kind: "running_tool", tool_call_id: "resumed-call", wait_triggers: undefined,
      started_at: "2026-10-28T00:02:00Z",
    });
    vi.advanceTimersByTime(THINKING_LABEL_HOLD_MS);
    expect(screen.getByRole("status").textContent).toBe("Running command · printf done...");
  });

  it("removes an interrupted wait while its historical parked result remains", async () => {
    const store = mountActivity();
    store.actions.setActivity({ ...park, status: "done" });
    await Promise.resolve();
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });
});

describe("terminal background command wait delivery presentation", () => {
  it("settles the command card, clears held running labels, keeps the grouped Wait row in the active orange card, and clears on delivery without advancing timers", () => {
    expect(verboseModePref()).toBe(false);
    applyBackgroundProcessEvent({
      session_id: INCIDENT_SESSION,
      process_id: INCIDENT_HANDLE,
      stream: "stdout",
      text: "running suite\n",
      end_offset: 14,
      running: true,
    });
    const { appStore, container } = mountCoordinatorChat([
      ...backgroundCommandMessages(),
      ...waitAssistantMessage("wait-35", [INCIDENT_HANDLE], true),
    ]);
    const waitAct = waitRunningActivity("wait-35");
    appStore.actions.setActivity(waitAct);

    const spans = () => [...container.querySelectorAll<HTMLElement>("[data-testid='activity-span-card']")];
    expect(spans()).toHaveLength(2);
    const [cmdSpan, waitSpan] = spans();
    expect(cmdSpan!.getAttribute("data-status")).toBe("running");
    expect(waitSpan!.getAttribute("data-status")).toBe("running");
    expect(screen.getByTestId("thinking-indicator").textContent).toBe("Waiting for a command to finish...");

    openSpanCardIfClosed(cmdSpan!);
    openSpanCardIfClosed(waitSpan!);
    const cmdRow = cmdSpan!.querySelector<HTMLElement>("[data-tool-call-id='cmd-34']")!;
    const waitRowBefore = waitSpan!.querySelector<HTMLElement>("[data-tool-call-id='wait-35']")!;
    expect(cmdRow.getAttribute("data-status")).toBe("running");
    expect(waitRowBefore).not.toBeNull();
    expect(waitRowBefore.getAttribute("data-status")).toBe("running");
    const waitPartId = waitRowBefore.getAttribute("data-tool-part-id");

    // Process exits at 09:00:40 while wait-35 remains pending (no timer advance).
    applyBackgroundProcessEvent({
      session_id: INCIDENT_SESSION,
      process_id: INCIDENT_HANDLE,
      stream: "exit",
      end_offset: 14,
      running: false,
      exit_code: 0,
      termination_reason: "exited",
    });

    expect(cmdSpan!.getAttribute("data-status")).toBe("done");
    expect(cmdRow.getAttribute("data-status")).toBe("done");
    expect(screen.getByTestId("thinking-indicator").textContent).toBe(`${TERMINAL_WAIT_DELIVERY_LABEL}...`);
    expect(waitSpan!.getAttribute("data-status")).toBe("running");
    expect(waitSpan!.querySelector(".den-activity-span-name")?.textContent).toBe(TERMINAL_WAIT_DELIVERY_LABEL);
    expect(waitSpan!.querySelector(":scope > .den-transcript-disclosure-header")?.getAttribute("aria-label"))
      .toContain(`${TERMINAL_WAIT_DELIVERY_LABEL}, running`);
    expect(waitRowBefore.textContent).toContain(TERMINAL_WAIT_DELIVERY_LABEL);

    // Host credential-reconciliation phase opens and closes before wait settles.
    const credAct: ActivityEvent = {
      activity_id: "cred-1",
      session_id: INCIDENT_SESSION,
      kind: "indexing_credentials",
      status: "active",
      started_at: "2026-11-23T17:01:24Z",
      indexing: { root_id: "r1", root_label: "web_app", directories_visited: 214, credential_files_read: 3 },
    };
    appStore.actions.setActivity(credAct);
    expect(screen.getByTestId("thinking-indicator").textContent)
      .toBe("Checking credential files in web_app · 214 folders · 3 files...");
    expect(waitSpan!.getAttribute("data-status")).toBe("running");
    expect(waitSpan!.querySelector(".den-activity-span-name")?.textContent)
      .toBe("Checking credential files in web_app · 214 folders · 3 files");

    appStore.actions.setActivity({ ...credAct, status: "done" });
    expect(screen.getByTestId("thinking-indicator").textContent).toBe(`${TERMINAL_WAIT_DELIVERY_LABEL}...`);
    expect(waitSpan!.querySelector(".den-activity-span-name")?.textContent).toBe(TERMINAL_WAIT_DELIVERY_LABEL);

    // Deliver benign wait result and settle session activity.
    appStore.actions.upsertMessage(waitSettledResult("wait-35"));
    appStore.actions.setActivity({ ...waitAct, status: "done" });
    appStore.actions.setCurrentSession({ ...appStore.state.currentSession!, status: "idle" });

    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
    expect(waitSpan!.getAttribute("data-status")).toBe("done");
    expect(waitSpan!.querySelector(":scope > .den-transcript-disclosure-header")?.getAttribute("aria-label"))
      .toContain(", done,");
    const waitRowAfter = waitSpan!.querySelector<HTMLElement>("[data-tool-call-id='wait-35']")!;
    expect(waitRowAfter).not.toBeNull();
    expect(waitRowAfter.getAttribute("data-tool-part-id")).toBe(waitPartId);
    expect(waitRowAfter.getAttribute("data-status")).toBe("done");
    expect(cmdRow.getAttribute("data-status")).toBe("done");

    // Duplicate exit event after settlement does not reopen or alter state.
    applyBackgroundProcessEvent({
      session_id: INCIDENT_SESSION,
      process_id: INCIDENT_HANDLE,
      stream: "exit",
      end_offset: 14,
      running: false,
      exit_code: 0,
      termination_reason: "exited",
    });
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
    expect(waitSpan!.getAttribute("data-status")).toBe("done");
  });

  it("handles exit before wait registration, delivery before paint, foreign session/handles, and reconnect while pending", () => {
    // 1. Exit at 09:00:40 before wait registration at 09:01:24: no handoff shown until matching wait opens.
    const { appStore, container } = mountCoordinatorChat(backgroundCommandMessages());
    applyBackgroundProcessEvent({
      session_id: INCIDENT_SESSION,
      process_id: INCIDENT_HANDLE,
      stream: "exit",
      end_offset: 0,
      running: false,
      exit_code: 0,
      termination_reason: "exited",
    });
    expect(screen.getByTestId("thinking-indicator").textContent).toBe("Thinking...");
    const firstSpan = container.querySelector<HTMLElement>("[data-testid='activity-span-card']")!;
    expect(firstSpan.getAttribute("data-status")).toBe("done");

    for (const msg of waitAssistantMessage("wait-after-exit", [INCIDENT_HANDLE])) {
      appStore.actions.upsertMessage(msg);
    }
    const waitAct = waitRunningActivity("wait-after-exit");
    appStore.actions.setActivity(waitAct);
    const spans = [...container.querySelectorAll<HTMLElement>("[data-testid='activity-span-card']")];
    const activeWaitSpan = spans[spans.length - 1]!;
    expect(activeWaitSpan.getAttribute("data-status")).toBe("running");
    expect(activeWaitSpan.querySelector(".den-activity-span-name")?.textContent).toBe(TERMINAL_WAIT_DELIVERY_LABEL);
    expect(screen.getByTestId("thinking-indicator").textContent).toBe(`${TERMINAL_WAIT_DELIVERY_LABEL}...`);

    // 2. Reconnect while pending: clear mirror and rehydrate from authoritative process snapshot.
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessOutputs(INCIDENT_SESSION, [
      { process_id: INCIDENT_HANDLE, running: false, exit_code: 0, termination_reason: "exited", chunks: [] },
    ]);
    expect(activeWaitSpan.getAttribute("data-status")).toBe("running");
    expect(activeWaitSpan.querySelector(".den-activity-span-name")?.textContent).toBe(TERMINAL_WAIT_DELIVERY_LABEL);
    expect(screen.getByTestId("thinking-indicator").textContent).toBe(`${TERMINAL_WAIT_DELIVERY_LABEL}...`);

    appStore.actions.upsertMessage(waitSettledResult("wait-after-exit", 6));
    appStore.actions.setActivity({ ...waitAct, status: "done" });
    expect(activeWaitSpan.getAttribute("data-status")).toBe("done");

    // 3. Unrelated handle or foreign session exit never claims pending delivery handoff.
    appStore.actions.upsertMessage({
      id: "assistant-foreign",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      ord: 8,
      seq: 8,
      created_at: "2026-11-23T17:09:00Z",
      tool_calls: [{ id: "wait-foreign", name: "wait", args: { conditions: [{ kind: "process_done", handles: ["foreign-handle"] }] } }],
    });
    const foreignAct = waitRunningActivity("wait-foreign");
    appStore.actions.setActivity(foreignAct);
    applyBackgroundProcessEvent({
      session_id: "another-session",
      process_id: "foreign-handle",
      stream: "exit",
      end_offset: 0,
      running: false,
      exit_code: 0,
    });
    expect(screen.getByTestId("thinking-indicator").textContent).toBe("Waiting...");
    appStore.actions.upsertMessage({
      ...waitSettledResult("wait-foreign", 9),
      tool_result: { ...waitSettledResult("wait-foreign", 9).tool_result!, assistant_message_id: "assistant-foreign" },
    });
    appStore.actions.setActivity({ ...foreignAct, status: "done" });

    // 4. Delivery before next paint: wait call and its result arrive together.
    appStore.actions.upsertMessage({
      id: "assistant-instant",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      ord: 10,
      seq: 10,
      created_at: "2026-11-23T17:10:00Z",
      tool_calls: [{ id: "wait-instant", name: "wait", args: { conditions: [{ kind: "process_done", handles: [INCIDENT_HANDLE] }] } }],
    });
    appStore.actions.upsertMessage({
      ...waitSettledResult("wait-instant", 11),
      tool_result: { ...waitSettledResult("wait-instant", 11).tool_result!, assistant_message_id: "assistant-instant" },
    });
    const finalSpans = [...container.querySelectorAll<HTMLElement>("[data-testid='activity-span-card']")];
    const instantSpan = finalSpans[finalSpans.length - 1]!;
    expect(instantSpan.getAttribute("data-status")).toBe("done");
    openSpanCardIfClosed(instantSpan);
    expect(instantSpan.querySelector("[data-tool-call-id='wait-instant']")?.getAttribute("data-status")).toBe("done");
  });
});
