import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { Message, WorkerTask } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";

describe("SessionTranscript tools and progress", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("renders an attributed fallback for a visible future message kind", () => {
    render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-fallback"
        messages={[
          {
            id: "future-1",
            role: "system",
            origin: "host",
            authority: "system",
            trust_tier: "trusted",
            kind: "future_host_fact",
            content: "A newer host recorded this action.",
            created_at: "2026-01-01T00:00:00Z",
          } as unknown as Message,
        ]}
      />
    ));
    expect(screen.getByTestId("transcript-fallback").textContent).toContain(
      "Unrecognized future_host_fact activity",
    );
    expect(screen.getByText("A newer host recorded this action.")).toBeTruthy();
  });

  it("keeps a singleton activity span stable when another call joins", async () => {
    const singletonMessages: Message[] = [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "c0", name: "read", args: { path: "a.go" } }],
        created_at: "2026-01-01T00:00:00Z",
        seq: 1,
      },
      {
        id: "t0",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        tool_result: {
          tool_call_id: "c0",
          tool: "read",
          assistant_message_id: "a1",
          tool_args: { path: "a.go" },
          content: "ok",
        },
        created_at: "2026-01-01T00:00:01Z",
        seq: 2,
      },
    ];
    const expandedMessages: Message[] = [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "c0", name: "read", args: { path: "a.go" } },
          { id: "c1", name: "read", args: { path: "b.go" } },
        ],
        created_at: "2026-01-01T00:00:00Z",
        seq: 1,
      },
      singletonMessages[1]!,
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "",
        tool_result: {
          tool_call_id: "c1",
          tool: "read",
          assistant_message_id: "a1",
          tool_args: { path: "b.go" },
          content: "",
        },
        created_at: "2026-01-01T00:00:02Z",
        seq: 3,
      },
    ];
    const [messages, setMessages] = createSignal(singletonMessages);
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-exit" messages={messages()} />
    ));
    const singleton = container.querySelector('[data-testid="activity-span-card"]');
    expect(singleton?.getAttribute("data-count")).toBe("1");

    setMessages(expandedMessages);
    await Promise.resolve();
    await Promise.resolve();

    const expanded = container.querySelector('[data-testid="activity-span-card"]');
    expect(expanded).toBe(singleton);
    expect(expanded?.getAttribute("data-count")).toBe("2");
    expect(
      container.querySelector('.transcript-viewport-row[data-exiting="true"]'),
    ).toBeNull();
    expect(container.querySelector(".den-exit-fade")).toBeNull();
  });

  it("renders the new item's role when the item at a position is replaced", async () => {
    const userMsg: Message = {
      id: "u1",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "first prompt",
      created_at: "2026-01-01T00:00:00Z",
    };
    const secondUserMsg: Message = {
      ...userMsg,
      id: "u2",
      content: "second prompt",
    };
    const assistantMsg: Message = {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "**Project status** summary",
      created_at: "2026-01-01T00:00:01Z",
    };

    const [messages, setMessages] = createSignal<Message[]>([
      userMsg,
      secondUserMsg,
    ]);
    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages()} />
    ));

    expect(container.querySelectorAll(".bubble--user")).toHaveLength(2);
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    const userRowBefore = container.querySelector(
      '.transcript-viewport-row[data-msg-id="u1"]',
    );
    expect(userRowBefore).toBeTruthy();

    setMessages([userMsg, assistantMsg]);
    await Promise.resolve();

    expect(container.querySelector(".bubble--user")).toBeTruthy();
    expect(container.querySelector(".bubble--assistant")).toBeTruthy();
    expect(
      container.querySelector('.transcript-viewport-row[data-msg-id="u1"]'),
    ).toBeTruthy();
    expect(screen.getByText("Project status").tagName).toBe("STRONG");
    expect(container.querySelector(".bubble--user")?.textContent).toContain(
      "first prompt",
    );
  });

  it("shows coordinator plan via ProgressStrip only, not update_progress tool row", () => {
    const planLabel = "ChessCore module (rules, board state, moves)";
    const messages: Message[] = [
      {
        id: "pu1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update",
        progress_update: {
          initial: true,
          seq: 1,
          steps: [{ state: "pending", label: planLabel }],
        },
        created_at: "2026-06-28T04:45:10.611Z",
      },
      {
        id: "a-plan",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "transcript",
        tool_calls: [
          {
            id: "functions.update_progress:8",
            name: "update_progress",
            args: {
              content: `## Plan\n- [ ] ${planLabel}\n- [ ] Verify build`,
            },
          },
        ],
        created_at: "2026-06-28T04:45:10.618Z",
      },
      {
        id: "tr-plan",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"status":"updated"}',
        tool_result: {
          content: '{"status":"updated"}',
          tool_call_id: "functions.update_progress:8",
          outcome: "completed",
        },
        created_at: "2026-06-28T04:45:10.620Z",
      },
    ];
    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages} />
    ));
    expect(container.querySelector('[data-testid="task-card"]')).toBeNull();
    expect(
      container.querySelector('[data-tool="update_progress"]'),
    ).toBeNull();
    expect(screen.getByTestId("progress-strip-item")).toBeTruthy();
    expect(container.textContent).toContain(planLabel);
    expect(container.textContent).not.toContain("Worker ·");
  });

  it("keeps progress snapshot DOM stable while unrelated messages arrive", async () => {
    const progress: Message = {
      id: "pu-stable",
      role: "system",
      origin: "host",
      authority: "system",
      trust_tier: "trusted",
      content: "",
      kind: "progress_update",
      progress_update: {
        initial: true,
        seq: 1,
        steps: [{ state: "pending", label: "Build the lexer" }],
      },
      created_at: "2026-06-28T04:45:10.611Z",
    };
    const [messages, setMessages] = createSignal<Message[]>([progress]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-progress-stable"
        messages={messages()}
      />
    ));
    const strip = container.querySelector(
      '[data-testid="progress-strip-created"]',
    );
    const body = container.querySelector(".progress-strip__body");
    const list = container.querySelector(".progress-strip__list");

    setMessages([
      progress,
      {
        id: "a-later",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "Still working.",
        created_at: "2026-06-28T04:45:11.000Z",
      },
    ]);
    await Promise.resolve();
    await Promise.resolve();

    expect(
      container.querySelector('[data-testid="progress-strip-created"]'),
    ).toBe(strip);
    expect(container.querySelector(".progress-strip__body")).toBe(body);
    expect(container.querySelector(".progress-strip__list")).toBe(list);
  });

  it("keeps the initial plan card on its wire snapshot when live progress advances", () => {
    const messages: Message[] = [
      {
        id: "pu1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update",
        progress_update: {
          initial: true,
          seq: 1,
          steps: [{ state: "pending", label: "Ship auth" }],
        },
        created_at: "2026-06-28T04:45:10.611Z",
      },
    ];
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "sess", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    appStore.actions.setProgress("sess", appStore.state.sessionViewEpoch, {
      steps: [
        { state: "done", label: "Ship auth" },
        { state: "pending", label: "Verify build" },
      ],
      revision: 2,
    });
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={messages}
        checkpointAppStore={appStore}
      />
    ));
    const created = screen.getByTestId("progress-strip-created");
    expect(created.textContent).toContain("Ship auth");
    expect(created.textContent).not.toContain("Verify build");
    expect(container.querySelectorAll('[data-testid="progress-strip-item"]')).toHaveLength(1);
  });

  it("renders worker task card for BANNER_TASK_QUEUED web-researcher dispatch", () => {
    const messages: Message[] = [
      {
        id: "a80ece7e-a8f7-468c-899f-bec5e24fb94f",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: "05f095dd-8a07-4034-b1bd-fba45e694508",
        tool_calls: [
          {
            id: "functions.grep:5",
            name: "grep",
            args: { pattern: "ai", path: "." },
          },
          {
            id: "functions.task:6",
            name: "task",
            args: {
              agent_type: "web-researcher",
              brief: { goal: "Research the latest cutting-edge AI trends", done_when: ["Return results."] },
              scope: { mode: "read" },
            },
          },
        ],
        created_at: "2026-06-06T13:14:28.917774Z",
      },
      {
        id: "grep-tr",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "no matches",
        tool_result: {
          content: "no matches",
          outcome: "completed",
          tool: "grep",
          tool_call_id: "functions.grep:5",
          assistant_message_id: "a80ece7e-a8f7-468c-899f-bec5e24fb94f",
          tool_args: { pattern: "ai", path: "." },
        },
        created_at: "2026-06-06T13:14:29.000Z",
      },
      {
        id: "5b078902-122a-4d07-857e-45b4591da63e",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content:
          '{"agent_type":"web-researcher","job_id":"fd5c17f1-e76a-48d1-845e-9a9f35b0e8f4","status":"enqueued"}\n>>> Worker queued\nCode: BANNER_TASK_QUEUED\n',
        workflow_run_id: "05f095dd-8a07-4034-b1bd-fba45e694508",
        tool_result: {
          content:
            '{"agent_type":"web-researcher","job_id":"fd5c17f1-e76a-48d1-845e-9a9f35b0e8f4","status":"enqueued"}\n>>> Worker queued\nCode: BANNER_TASK_QUEUED\n',
          outcome: "completed",
          codes: ["BANNER_TASK_QUEUED"],
          ui_visibility: "normal",
          dispatch: { worker_id: "fd5c17f1-e76a-48d1-845e-9a9f35b0e8f4" },
          tool: "task",
          tool_call_id: "functions.task:6",
          assistant_message_id: "a80ece7e-a8f7-468c-899f-bec5e24fb94f",
          tool_args: {
            agent_type: "web-researcher",
            brief: { goal: "Research the latest cutting-edge AI trends", done_when: ["Return results."] },
            scope: { mode: "read" },
          },
        },
        created_at: "2026-06-06T13:14:29.063328Z",
      },
    ];
    const workers: WorkerTask[] = [
      {
        id: "fd5c17f1-e76a-48d1-845e-9a9f35b0e8f4",
        parent_session_id: "2464e5bf-18f9-4995-8c6b-740347362a49",
        agent_type: "web-researcher",
        status: "running",
        created_at: "2026-06-06T13:14:29.024243Z",
      },
    ];
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={messages}
        sessionId="2464e5bf-18f9-4995-8c6b-740347362a49"
        workers={workers}
      />
    ));
    expect(container.querySelectorAll('[data-testid="task-card"]').length).toBe(1);
    expect(container.querySelectorAll('[data-testid="tool-part-card"]').length).toBe(1);
    expect(container.textContent).toContain("Worker · web-researcher");
    expect(container.textContent).toContain(
      "Research the latest cutting-edge AI trends",
    );
  });

  it("updates task card progress when the matched worker budget ticks", async () => {
    const sessionId = "2464e5bf-18f9-4995-8c6b-740347362a49";
    const jobId = "fd5c17f1-e76a-48d1-845e-9a9f35b0e8f4";
    const messages: Message[] = [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "functions.task:6",
            name: "task",
            args: {
              agent_type: "web-researcher",
              brief: { goal: "Research AI trends", done_when: ["Return results."] },
              max_tool_loops: 10,
            },
          },
        ],
        created_at: "2026-06-06T13:14:28.917774Z",
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: JSON.stringify({
          agent_type: "web-researcher",
          job_id: jobId,
          status: "enqueued",
        }),
        tool_result: {
          content: JSON.stringify({
            agent_type: "web-researcher",
            job_id: jobId,
            status: "enqueued",
          }),
          outcome: "completed",
          dispatch: { worker_id: jobId },
          tool: "task",
          tool_call_id: "functions.task:6",
          assistant_message_id: "a1",
          tool_args: {
            agent_type: "web-researcher",
            brief: { goal: "Research AI trends", done_when: ["Return results."] },
            max_tool_loops: 10,
          },
        },
        created_at: "2026-06-06T13:14:29.063328Z",
      },
    ];
    const [workers, setWorkers] = createSignal<WorkerTask[]>([
      {
        id: jobId,
        parent_session_id: sessionId,
        agent_type: "web-researcher",
        status: "running",
        max_tool_loops: 10,
        tool_loops_used: 1,
        created_at: "2026-06-06T13:14:29.024243Z",
      },
    ]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={messages}
        sessionId={sessionId}
        workers={workers()}
      />
    ));
    const fill = () =>
      container.querySelector(".den-task-card-progress-fill") as HTMLElement;
    expect(fill()?.style.width).toBe("10%");

    setWorkers([
      {
        ...workers()[0]!,
        tool_loops_used: 4,
      },
    ]);
    await Promise.resolve();
    expect(fill()?.style.width).toBe("40%");
  });

});
