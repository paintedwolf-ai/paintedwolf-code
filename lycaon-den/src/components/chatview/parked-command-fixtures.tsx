import { fireEvent, render } from "@solidjs/testing-library";
import type { ActivityEvent, Message } from "../../api/types.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { ChatView } from "./ChatView.tsx";

export const INCIDENT_SESSION = "session-incident-34";
export const INCIDENT_PROJECT = "project-incident-34";
export const INCIDENT_HANDLE = "1f26f2b0-e3a7-4498-9b98-ff231be0d9be";

export function mountCoordinatorChat(initialMessages: Message[]) {
  setLycaonClientForTest(stubClient({ listSessionArtifacts: async () => ({ artifacts: [] }) }));
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  appStore.actions.setCurrentSession({
    mode: "agent",
    started: true,
    root_session_id: INCIDENT_SESSION,
    id: INCIDENT_SESSION,
    project_id: INCIDENT_PROJECT,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    workspace_path: "/tmp/p",
    posture: "build",
    status: "busy",
    created_at: "2026-11-23T17:00:00Z",
    activity_at: "2026-11-23T17:00:00Z",
    updated_at: "2026-11-23T17:00:00Z",
  });
  appStore.actions.installTranscriptBaseline(INCIDENT_SESSION, initialMessages, initialMessages.length);
  const view = render(() => (
    <ChatTabChromeProvider sessionKey={INCIDENT_SESSION}>
      <ChatView
        appStore={appStore}
        recents={createRecentsStore()}
        projects={mockProjectsStore([wireProject("/tmp/p", INCIDENT_PROJECT)])}
        projectId={INCIDENT_PROJECT}
        projectDir="/tmp/p"
        sessionId={INCIDENT_SESSION}
        hasInitialPrompt={false}
        onOpenWorker={() => {}}
        onOpenFiles={() => {}}
        onSend={async () => true}
        onStop={() => {}}
      />
    </ChatTabChromeProvider>
  ));
  return { appStore, container: view.container };
}

export function openSpanCardIfClosed(card: HTMLElement): void {
  const header = card.querySelector<HTMLElement>(":scope > .den-transcript-disclosure-header");
  if (header && header.getAttribute("aria-expanded") !== "true") {
    fireEvent.click(header);
  }
}

export function backgroundCommandMessages(handle = INCIDENT_HANDLE): Message[] {
  return [
    {
      id: "user-34",
      role: "user",
      origin: "user",
      authority: "user",
      trust_tier: "trusted",
      content: "Run the test suite in the background and wait for it",
      ord: 1,
      seq: 1,
      created_at: "2026-11-23T17:00:30Z",
    },
    {
      id: "assistant-34",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      ord: 2,
      seq: 2,
      created_at: "2026-11-23T17:00:38Z",
      tool_calls: [{ id: "cmd-34", name: "command", args: { command: "./task test", background: true } }],
    },
    {
      id: "result-34",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "started background process",
      ord: 3,
      seq: 3,
      created_at: "2026-11-23T17:00:39Z",
      tool_result: {
        tool: "command",
        assistant_message_id: "assistant-34",
        tool_call_id: "cmd-34",
        content: "started background process",
        outcome: "completed",
        process: { handle, running: true },
        command_program: {
          id: "program-cmd-34",
          parent_receipt_id: "parent-cmd-34",
          state: "completed",
          language_version: 1,
          catalog_digest: "catalog",
          source_digest: "source",
          source_ref: "source",
          detail_ref: "detail",
          progress_state: "unchanged",
          started_at: "2026-11-23T17:00:38Z",
          activities: [
            {
              id: "act-cmd-34",
              group: "Running tests",
              phase: "execute",
              label: "Run command",
              subject: "./task test",
              state: "completed",
              outcome: "Started",
              job_handle: handle,
              execution_ids: ["exec-cmd-34"],
              steps: [],
              spans: [],
            },
          ],
        },
        action: {
          label: "Run command",
          status: "running",
          ran: { text: "./task test", noun: "command" },
          facts: [{ kind: "process", state: "running" }],
          row: ["process"],
          links: [],
        },
      },
    },
    {
      id: "note-after-34",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "Launched the background job; checking files and waiting on completion.",
      ord: 4,
      seq: 4,
      created_at: "2026-11-23T17:01:00Z",
    },
  ];
}

export function waitAssistantMessage(
  callId: string,
  handles: string[],
  groupedWithPreceding = false,
): Message[] {
  const calls = groupedWithPreceding
    ? [
        { id: "prep-cmd", name: "command", args: { command: "pwd" } },
        { id: callId, name: "wait", args: { until_complete: true, conditions: [{ kind: "process_done", handles }] } },
      ]
    : [{ id: callId, name: "wait", args: { until_complete: true, conditions: [{ kind: "process_done", handles }] } }];
  const out: Message[] = [
    {
      id: `assistant-${callId}`,
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      ord: 5,
      seq: 5,
      created_at: "2026-11-23T17:01:24Z",
      tool_calls: calls,
    },
  ];
  if (groupedWithPreceding) {
    out.push({
      id: "result-prep-cmd",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "/tmp/p\n",
      ord: 6,
      seq: 6,
      created_at: "2026-11-23T17:01:24Z",
      tool_result: {
        tool: "command",
        assistant_message_id: `assistant-${callId}`,
        tool_call_id: "prep-cmd",
        content: "/tmp/p\n",
        outcome: "completed",
        command_program: {
          id: `program-prep-${callId}`,
          parent_receipt_id: `parent-prep-${callId}`,
          state: "completed",
          language_version: 1,
          catalog_digest: "catalog",
          source_digest: "source",
          source_ref: "source",
          detail_ref: "detail",
          progress_state: "unchanged",
          started_at: "2026-11-23T17:01:24Z",
          activities: [
            {
              id: `act-prep-${callId}`,
              group: "Inspecting environment",
              phase: "inspect",
              label: "Run command",
              subject: "pwd",
              state: "completed",
              outcome: "Completed",
              execution_ids: [`exec-prep-${callId}`],
              steps: [],
              spans: [],
            },
          ],
        },
        action: {
          label: "Run command",
          status: "done",
          ran: { text: "pwd", noun: "command" },
          facts: [{ kind: "result", outcome: "passed", exit_code: 0 }],
          row: ["result"],
          links: [],
        },
      },
    });
  }
  return out;
}

export function waitRunningActivity(callId: string, sessionId = INCIDENT_SESSION): ActivityEvent {
  return {
    activity_id: `act-${callId}`,
    session_id: sessionId,
    kind: "running_tool",
    status: "active",
    started_at: "2026-11-23T17:01:24Z",
    tool_name: "wait",
    tool_call_id: callId,
  };
}

export function waitSettledResult(callId: string, ord = 7): Message {
  return {
    id: `result-${callId}`,
    role: "tool",
    origin: "tool",
    authority: "none",
    trust_tier: "trusted",
    content: "{\"status\":\"completed\"}",
    ord,
    seq: ord,
    created_at: "2026-11-23T17:08:48Z",
    tool_result: {
      tool: "wait",
      assistant_message_id: `assistant-${callId}`,
      tool_call_id: callId,
      content: "{\"status\":\"completed\"}",
      outcome: "completed",
      ui_visibility: "benign",
    },
  };
}
