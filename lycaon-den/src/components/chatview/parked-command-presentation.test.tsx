import { cleanup, render, screen } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ActivityEvent, Message } from "../../api/types.ts";
import { THINKING_LABEL_HOLD_MS } from "../../chat/session/thinking-label-hold.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { ComposerActivityIndicator } from "./ComposerActivityIndicator.tsx";
import { createChatViewActivity } from "./chat-view-activity.ts";

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
afterEach(() => { cleanup(); vi.useRealTimers(); });

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
